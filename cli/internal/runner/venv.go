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
//  3. configVenv (duchess.yml ansible_venv or DUCHESS_ANSIBLE_VENV)
//  4. active $VIRTUAL_ENV (only if it contains bin/ansible-playbook)
//  5. workspace .venv / venv (only if it contains bin/ansible-playbook)
//
// If an explicit/item/config venv is specified and does not contain bin/ansible-playbook,
// an explicit error is returned so Duchess never silently falls back to a different environment.
func ResolvePlaybookVenv(explicitVenv, itemVenv, configVenv, workspaceDir string) (venvPath, binPath string, isExplicit bool, err error) {
	checkRequired := func(raw, source string) (string, string, error) {
		resolved := ExpandVenvPath(raw, workspaceDir)
		bin := VenvAnsibleBinary(resolved)
		st, statErr := os.Stat(bin)
		if statErr != nil {
			return "", "", fmt.Errorf("%s venv %q does not contain ansible-playbook at %s (%v)", source, raw, bin, statErr)
		}
		if st.IsDir() {
			return "", "", fmt.Errorf("%s venv %q has a directory at %s instead of an executable", source, raw, bin)
		}
		return resolved, bin, nil
	}

	if strings.TrimSpace(explicitVenv) != "" {
		v, b, e := checkRequired(explicitVenv, "specified")
		return v, b, true, e
	}
	if strings.TrimSpace(itemVenv) != "" {
		v, b, e := checkRequired(itemVenv, "playbook header (# duchess:venv:)")
		return v, b, true, e
	}
	if strings.TrimSpace(configVenv) != "" {
		v, b, e := checkRequired(configVenv, "workspace config")
		return v, b, true, e
	}

	// Soft auto-detection: active $VIRTUAL_ENV, then workspace .venv / venv
	var softCandidates []string
	if active := strings.TrimSpace(os.Getenv("VIRTUAL_ENV")); active != "" {
		softCandidates = append(softCandidates, active)
	}
	if workspaceDir != "" {
		softCandidates = append(softCandidates,
			filepath.Join(workspaceDir, ".venv"),
			filepath.Join(workspaceDir, "venv"),
		)
	}
	for _, c := range softCandidates {
		resolved := ExpandVenvPath(c, workspaceDir)
		bin := VenvAnsibleBinary(resolved)
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			return resolved, bin, false, nil
		}
	}
	return "", "", false, nil
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
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".venv"),
			filepath.Join(home, "venv"),
		)
		for _, parent := range []string{
			filepath.Join(home, "venvs"),
			filepath.Join(home, ".virtualenvs"),
			filepath.Join(home, ".local", "share", "virtualenvs"),
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
