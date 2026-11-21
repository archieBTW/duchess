package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
)

// VenvInfo describes a local Python virtual environment containing ansible-playbook.
type VenvInfo struct {
	Path    string `json:"path"`
	BinPath string `json:"binPath"`
	Label   string `json:"label"`
	Version string `json:"version,omitempty"`
}

// ExpandVenvPath expands ~/ and resolves relative paths against workspaceDir.
func ExpandVenvPath(raw, workspaceDir string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	p = inventory.ExpandHome(p)
	if !filepath.IsAbs(p) && workspaceDir != "" {
		p = filepath.Join(workspaceDir, p)
	}
	return filepath.Clean(p)
}

// VenvAnsibleBinary returns the expected ansible-playbook binary path inside venvDir.
func VenvAnsibleBinary(venvDir string) string {
	return filepath.Join(venvDir, "bin", "ansible-playbook")
}

// ResolvePlaybookVenv resolves which Python virtualenv (if any) should run ansible-playbook.
// Precedence:
//  1. explicitVenv (--venv flag or TUI input)
//  2. itemVenv (# duchess:venv: header in playbook)
//  3. playbookConfigVenv (playbook_venvs match in duchess.yml)
//  4. serverConfigVenv (duchess_venv inventory var or server_venvs match in duchess.yml)
//  5. configVenv (global ansible_venv in duchess.yml or DUCHESS_ANSIBLE_VENV)
//  6. active $VIRTUAL_ENV (only if it contains bin/ansible-playbook)
//  7. workspace .venv / venv (only if it contains bin/ansible-playbook)
//
// If an explicit/pinned/configured venv does not contain bin/ansible-playbook,
// an explicit error is returned so Duchess never silently falls back to a different environment.
func ResolvePlaybookVenv(explicitVenv, itemVenv, configVenv, workspaceDir string) (venvPath, binPath string, isExplicit bool, err error) {
	v, b, isExp, _, e := ResolvePlaybookVenvLayered(VenvLookup{
		ExplicitVenv: explicitVenv,
		ItemVenv:     itemVenv,
		ConfigVenv:   configVenv,
		WorkspaceDir: workspaceDir,
	})
	return v, b, isExp, e
}

// VenvLookup holds all layered sources for resolving a controller virtualenv.
type VenvLookup struct {
	ExplicitVenv       string
	ItemVenv           string
	PlaybookConfigVenv string
	ServerConfigVenv   string
	ConfigVenv         string
	WorkspaceDir       string
}

// ResolvePlaybookVenvLayered resolves the controller venv across all priority layers
// and returns (venvPath, binPath, isExplicit, sourceLabel, err).
func ResolvePlaybookVenvLayered(in VenvLookup) (venvPath, binPath string, isExplicit bool, source string, err error) {
	checkRequired := func(raw, srcLabel string) (string, string, error) {
		resolved := ExpandVenvPath(raw, in.WorkspaceDir)
		bin := VenvAnsibleBinary(resolved)
		st, statErr := os.Stat(bin)
		if statErr != nil {
			return "", "", fmt.Errorf("%s venv %q does not contain ansible-playbook at %s (%v)", srcLabel, raw, bin, statErr)
		}
		if st.IsDir() {
			return "", "", fmt.Errorf("%s venv %q has a directory at %s instead of an executable", srcLabel, raw, bin)
		}
		return resolved, bin, nil
	}

	requiredLayers := []struct {
		val   string
		label string
	}{
		{in.ExplicitVenv, "specified"},
		{in.ItemVenv, "playbook header (# duchess:venv:)"},
		{in.PlaybookConfigVenv, "playbook_venvs (duchess.yml)"},
		{in.ServerConfigVenv, "server_venvs / inventory (duchess_venv)"},
		{in.ConfigVenv, "workspace config (ansible_venv)"},
	}
	for _, layer := range requiredLayers {
		if strings.TrimSpace(layer.val) != "" {
			v, b, e := checkRequired(layer.val, layer.label)
			return v, b, true, layer.label, e
		}
	}

	// Soft auto-detection: active $VIRTUAL_ENV, then workspace .venv / venv
	var softCandidates []struct {
		path  string
		label string
	}
	if active := strings.TrimSpace(os.Getenv("VIRTUAL_ENV")); active != "" {
		softCandidates = append(softCandidates, struct{ path, label string }{active, "active $VIRTUAL_ENV"})
	}
	if in.WorkspaceDir != "" {
		softCandidates = append(softCandidates,
			struct{ path, label string }{filepath.Join(in.WorkspaceDir, ".venv"), "workspace .venv"},
			struct{ path, label string }{filepath.Join(in.WorkspaceDir, "venv"), "workspace venv"},
		)
	}
	for _, c := range softCandidates {
		resolved := ExpandVenvPath(c.path, in.WorkspaceDir)
		bin := VenvAnsibleBinary(resolved)
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			return resolved, bin, false, c.label, nil
		}
	}
	return "", "", false, "", nil
}

// BuildVenvEnv returns a process environment configured to activate venvPath
// (sets VIRTUAL_ENV, prepends <venv>/bin to PATH, strips PYTHONHOME, and enables unbuffered output).
func BuildVenvEnv(baseEnv []string, venvPath string) []string {
	var out []string
	var curPath string
	for _, kv := range baseEnv {
		if strings.HasPrefix(kv, "PYTHONHOME=") || strings.HasPrefix(kv, "VIRTUAL_ENV=") || strings.HasPrefix(kv, "PYTHONUNBUFFERED=") {
			continue
		}
		if strings.HasPrefix(kv, "PATH=") {
			curPath = strings.TrimPrefix(kv, "PATH=")
			continue
		}
		out = append(out, kv)
	}

	out = append(out, "PYTHONUNBUFFERED=1")
	if venvPath != "" {
		absVenv, err := filepath.Abs(venvPath)
		if err != nil {
			absVenv = venvPath
		}
		venvBin := filepath.Join(absVenv, "bin")
		out = append(out, "VIRTUAL_ENV="+absVenv)
		if curPath != "" {
			out = append(out, "PATH="+venvBin+string(os.PathListSeparator)+curPath)
		} else {
			out = append(out, "PATH="+venvBin)
		}
	} else if curPath != "" {
		out = append(out, "PATH="+curPath)
	}
	return out
}

// DiscoverVenvs scans common locations for Python virtual environments containing ansible-playbook.
func DiscoverVenvs(workspaceDir string) []VenvInfo {
	var candidates []string
	if active := strings.TrimSpace(os.Getenv("VIRTUAL_ENV")); active != "" {
		candidates = append(candidates, active)
	}
	if workspaceDir != "" {
		candidates = append(candidates,
			filepath.Join(workspaceDir, ".venv"),
			filepath.Join(workspaceDir, "venv"),
			filepath.Join(filepath.Dir(workspaceDir), ".venv"),
			filepath.Join(filepath.Dir(workspaceDir), "WE-IAC", ".venv"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".venv"),
			filepath.Join(home, "venv"),
		)
		for _, parent := range []string{
			filepath.Join(home, "venvs"),
			filepath.Join(home, ".venvs"),
			filepath.Join(home, ".virtualenvs"),
			filepath.Join(home, ".local", "share", "virtualenvs"),
			filepath.Join(home, ".local", "pipx", "venvs"),
			filepath.Join(home, ".pyenv", "versions"),
		} {
			entries, err := os.ReadDir(parent)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					candidates = append(candidates, filepath.Join(parent, e.Name()))
				}
			}
		}
	}

	seen := make(map[string]bool)
	var found []VenvInfo
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			abs = filepath.Clean(c)
		}
		if seen[abs] {
			continue
		}
		bin := VenvAnsibleBinary(abs)
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			seen[abs] = true
			label := filepath.Base(abs)
			if workspaceDir != "" {
				if rel, err := filepath.Rel(workspaceDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
					label = rel
				}
			}
			found = append(found, VenvInfo{
				Path:    abs,
				BinPath: bin,
				Label:   label,
			})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found
}

// ProbeAnsibleVersion runs `<bin> --version` with a short timeout and returns the first line.
func ProbeAnsibleVersion(binPath, venvPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "--version")
	cmd.Env = BuildVenvEnv(os.Environ(), venvPath)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	firstLine := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return firstLine
}

// RemoteVenvPython returns the python executable inside a remote virtualenv path.
func RemoteVenvPython(remoteVenv string) string {
	p := strings.TrimSpace(remoteVenv)
	if p == "" {
		return ""
	}
	p = strings.TrimRight(p, "/")
	if strings.HasSuffix(p, "/python") || strings.HasSuffix(p, "/python3") {
		return p
	}
	return p + "/bin/python"
}
