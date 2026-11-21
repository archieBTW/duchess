package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
	"gopkg.in/yaml.v3"
)

// PlaybookOptions configures Ansible playbook execution, including virtualenv isolation,
// check/diff modes, tags, vault credentials, verbosity, and live output streaming.
type PlaybookOptions struct {
	Venv               string // Explicit controller Python virtualenv path (--venv / TUI override)
	ItemVenv           string // Playbook header (# duchess:venv:) virtualenv path
	PlaybookConfigVenv string // Pinned playbook venv from duchess.yml playbook_venvs
	ServerConfigVenv   string // Pinned server/group venv from duchess.yml server_venvs or inventory duchess_venv
	ConfigVenv         string // Global workspace default virtualenv path
	WorkspaceDir       string // Workspace root for resolving relative venv paths
	RemoteVenv         string // Remote host virtualenv path (sets ansible_python_interpreter)
	CheckMode          bool
	DiffMode           bool
	Become             bool
	Tags               string
	SkipTags           string
	VaultPassFile      string
	Verbosity          int               // 0..4 => -v..-vvvv
	OnOutput           func(line string) // Optional real-time line streaming callback
}

// ListItems discovers all playbooks or scripts in a directory and parses Duchess header metadata.
func ListItems(dir, kind string) ([]models.AutomationItem, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []models.AutomationItem
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if kind == "playbook" && ext != ".yml" && ext != ".yaml" {
			continue
		}
		if kind == "script" && ext != ".sh" && ext != ".bash" {
			continue
		}
		fullPath := filepath.Join(dir, name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		items = append(items, parseMetadata(name, fullPath, kind, string(data)))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].FileName < items[j].FileName })
	return items, nil
}

// ParseItemMetadata reads header metadata for a single playbook or script file.
func ParseItemMetadata(filePath, kind string) (models.AutomationItem, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return models.AutomationItem{}, err
	}
	return parseMetadata(filepath.Base(filePath), filePath, kind, string(data)), nil
}

func parseMetadata(fileName, fullPath, kind, content string) models.AutomationItem {
	item := models.AutomationItem{
		FileName:    fileName,
		Name:        strings.TrimSuffix(fileName, filepath.Ext(fileName)),
		Category:    "general",
		Kind:        kind,
		Path:        fullPath,
		DefaultVars: make(map[string]string),
		Content:     content,
	}
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# duchess:name:") {
			item.Name = strings.TrimSpace(strings.TrimPrefix(t, "# duchess:name:"))
		} else if strings.HasPrefix(t, "# duchess:description:") {
			item.Description = strings.TrimSpace(strings.TrimPrefix(t, "# duchess:description:"))
		} else if strings.HasPrefix(t, "# duchess:category:") {
			item.Category = strings.TrimSpace(strings.TrimPrefix(t, "# duchess:category:"))
		} else if strings.HasPrefix(t, "# duchess:venv:") {
			item.Venv = strings.TrimSpace(strings.TrimPrefix(t, "# duchess:venv:"))
		} else if strings.HasPrefix(t, "# duchess:remote_venv:") {
			item.RemoteVenv = strings.TrimSpace(strings.TrimPrefix(t, "# duchess:remote_venv:"))
		} else if strings.HasPrefix(t, "# duchess:vars:") {
			raw := strings.TrimSpace(strings.TrimPrefix(t, "# duchess:vars:"))
			for _, pair := range strings.Split(raw, ",") {
				if idx := strings.IndexByte(pair, '='); idx > 0 {
					item.DefaultVars[strings.TrimSpace(pair[:idx])] = strings.TrimSpace(pair[idx+1:])
				}
			}
		}
	}
	return item
}

// AddFile copies an external playbook or script into the workspace directory.
func AddFile(srcPath, destDir string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(destDir, filepath.Base(srcPath))
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

// RunScript executes a shell script over SSH across all target hosts concurrently.
func RunScript(ctx context.Context, scriptPath string, hosts []*models.Host, envVars map[string]string, opts sshclient.Options) ([]models.ExecutionResult, error) {
	return RunScriptStreaming(ctx, scriptPath, hosts, envVars, opts, nil)
}

// RunScriptStreaming executes a shell script over SSH across all target hosts concurrently,
// invoking onOutput for each stdout/stderr line as it arrives.
func RunScriptStreaming(ctx context.Context, scriptPath string, hosts []*models.Host, envVars map[string]string, opts sshclient.Options, onOutput func(host, stream, line string)) ([]models.ExecutionResult, error) {
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("read script %s: %w", scriptPath, err)
	}
	content := string(data)
	results := make([]models.ExecutionResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(idx int, host *models.Host) {
			defer wg.Done()
			start := time.Now()
			client, err := sshclient.Connect(ctx, host, opts)
			if err != nil {
				if onOutput != nil {
					onOutput(host.Name, "stderr", "SSH error: "+err.Error())
				}
				results[idx] = models.ExecutionResult{
					HostName:   host.Name,
					Address:    host.Address,
					ExitCode:   -1,
					DurationMs: time.Since(start).Milliseconds(),
					Engine:     "ssh",
					Error:      err.Error(),
				}
				return
			}
			defer client.Close()
			var lineCB func(stream, line string)
			if onOutput != nil {
				lineCB = func(stream, line string) {
					onOutput(host.Name, stream, line)
				}
			}
			stdout, stderr, code, runErr := client.RunScriptStreaming(ctx, content, envVars, lineCB)
			errMsg := ""
			if runErr != nil {
				errMsg = runErr.Error()
			}
			results[idx] = models.ExecutionResult{
				HostName:   host.Name,
				Address:    host.Address,
				ExitCode:   code,
				Stdout:     stdout,
				Stderr:     stderr,
				DurationMs: time.Since(start).Milliseconds(),
				Engine:     "ssh",
				Error:      errMsg,
			}
		}(i, h)
	}
	wg.Wait()
	return results, nil
}

// RunPlaybook runs an Ansible playbook using local `ansible-playbook` when available,
// or falls back to Duchess's built-in SSH Playbook Engine when Ansible is not installed.
func RunPlaybook(ctx context.Context, inventoryPath, playbookPath, limit string, hosts []*models.Host, extraVars map[string]string, checkMode bool, opts sshclient.Options) ([]models.ExecutionResult, error) {
	return RunPlaybookWithOptions(ctx, inventoryPath, playbookPath, limit, hosts, extraVars, PlaybookOptions{
		CheckMode: checkMode,
	}, opts)
}

// RunPlaybookWithOptions runs an Ansible playbook with full virtualenv, flag, SSH override,
// and real-time streaming support.
func RunPlaybookWithOptions(
	ctx context.Context,
	inventoryPath, playbookPath, limit string,
	hosts []*models.Host,
	extraVars map[string]string,
	pbOpts PlaybookOptions,
	sshOpts sshclient.Options,
) ([]models.ExecutionResult, error) {
	// Auto-read playbook header metadata if caller didn't pre-populate ItemVenv/RemoteVenv
	if pbOpts.ItemVenv == "" || pbOpts.RemoteVenv == "" {
		if meta, err := ParseItemMetadata(playbookPath, "playbook"); err == nil {
			if pbOpts.ItemVenv == "" {
				pbOpts.ItemVenv = meta.Venv
			}
			if pbOpts.RemoteVenv == "" {
				pbOpts.RemoteVenv = meta.RemoteVenv
			}
		}
	}

	venvPath, binPath, isExplicit, _, err := ResolvePlaybookVenvLayered(VenvLookup{
		ExplicitVenv:       pbOpts.Venv,
		ItemVenv:           pbOpts.ItemVenv,
		PlaybookConfigVenv: pbOpts.PlaybookConfigVenv,
		ServerConfigVenv:   pbOpts.ServerConfigVenv,
		ConfigVenv:         pbOpts.ConfigVenv,
		WorkspaceDir:       pbOpts.WorkspaceDir,
	})
	if err != nil {
		// Explicit/configured venv is missing ansible-playbook -> fail fast, never silently fall back
		return nil, err
	}
	if binPath != "" {
		return runWithAnsibleBinary(ctx, binPath, venvPath, inventoryPath, playbookPath, limit, extraVars, pbOpts, sshOpts)
	}
	if !isExplicit {
		if bin, lookErr := exec.LookPath("ansible-playbook"); lookErr == nil {
			return runWithAnsibleBinary(ctx, bin, "", inventoryPath, playbookPath, limit, extraVars, pbOpts, sshOpts)
		}
	}
	return runWithBuiltinSSHEngine(ctx, playbookPath, hosts, extraVars, pbOpts, sshOpts)
}

// BuildAnsibleArgs builds the ansible-playbook CLI arguments for testing and execution.
func BuildAnsibleArgs(inventoryPath, playbookPath, limit string, extraVars map[string]string, pbOpts PlaybookOptions, sshOpts sshclient.Options) []string {
	args := []string{"-i", inventoryPath, playbookPath}
	if limit != "" && limit != "all" {
		args = append(args, "-l", limit)
	}
	if pbOpts.CheckMode {
		args = append(args, "--check")
	}
	if pbOpts.DiffMode {
		args = append(args, "--diff")
	}
	if pbOpts.Become {
		args = append(args, "--become")
	}
	if strings.TrimSpace(pbOpts.Tags) != "" {
		args = append(args, "--tags", strings.TrimSpace(pbOpts.Tags))
	}
	if strings.TrimSpace(pbOpts.SkipTags) != "" {
		args = append(args, "--skip-tags", strings.TrimSpace(pbOpts.SkipTags))
	}
	if strings.TrimSpace(pbOpts.VaultPassFile) != "" {
		args = append(args, "--vault-password-file", inventory.ExpandHome(strings.TrimSpace(pbOpts.VaultPassFile)))
	}
	if pbOpts.Verbosity > 0 {
		v := pbOpts.Verbosity
		if v > 4 {
			v = 4
		}
		args = append(args, "-"+strings.Repeat("v", v))
	}

	// Forward session SSH overrides so TUI/CLI credentials apply consistently to ansible-playbook
	if sshOpts.User != "" {
		args = append(args, "-u", sshOpts.User)
	}
	if sshOpts.PrivateKey != "" {
		args = append(args, "--private-key", inventory.ExpandHome(sshOpts.PrivateKey))
	}
	if sshOpts.Port > 0 {
		args = append(args, "-e", fmt.Sprintf("ansible_port=%d", sshOpts.Port))
	}
	if sshOpts.Password != "" {
		args = append(args, "-e", fmt.Sprintf("ansible_password=%s", sshOpts.Password))
	}
	if sshOpts.JumpHost != "" {
		args = append(args, "--ssh-common-args=-J "+sshOpts.JumpHost)
	}
	if py := RemoteVenvPython(pbOpts.RemoteVenv); py != "" {
		args = append(args, "-e", "ansible_python_interpreter="+py)
	}

	// Deterministic ordering for extra vars
	keys := make([]string, 0, len(extraVars))
	for k := range extraVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, extraVars[k]))
	}
	return args
}

func runWithAnsibleBinary(
	ctx context.Context,
	bin, venvPath, inventoryPath, playbookPath, limit string,
	extraVars map[string]string,
	pbOpts PlaybookOptions,
	sshOpts sshclient.Options,
) ([]models.ExecutionResult, error) {
	args := BuildAnsibleArgs(inventoryPath, playbookPath, limit, extraVars, pbOpts, sshOpts)

	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, args...)
	env := BuildVenvEnv(os.Environ(), venvPath)
	// Auto-load ansible.cfg placed alongside the playbook (e.g. playbooks/ansible.cfg)
	if os.Getenv("ANSIBLE_CONFIG") == "" {
		if absPb, err := filepath.Abs(playbookPath); err == nil {
			cfgCandidate := filepath.Join(filepath.Dir(absPb), "ansible.cfg")
			if st, err := os.Stat(cfgCandidate); err == nil && !st.IsDir() {
				env = append(env, "ANSIBLE_CONFIG="+cfgCandidate)
			}
		}
	}
	cmd.Env = env

	engineLabel := "ansible-playbook"
	if venvPath != "" {
		engineLabel = fmt.Sprintf("ansible-playbook (%s)", filepath.Base(venvPath))
	}
	if pbOpts.OnOutput != nil {
		if venvPath != "" {
			pbOpts.OnOutput(fmt.Sprintf("[duchess] Using venv: %s (%s)", venvPath, bin))
		} else {
			pbOpts.OnOutput(fmt.Sprintf("[duchess] Running: %s", bin))
		}
	}

	var outBuf, errBuf bytes.Buffer
	var runErr error

	if pbOpts.OnOutput != nil {
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		var outMu sync.Mutex
		emit := func(line string) {
			outMu.Lock()
			pbOpts.OnOutput(line)
			outMu.Unlock()
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			readPipeLines(stdoutPipe, &outBuf, emit)
		}()
		go func() {
			defer wg.Done()
			readPipeLines(stderrPipe, &errBuf, emit)
		}()
		wg.Wait()
		runErr = cmd.Wait()
	} else {
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		runErr = cmd.Run()
	}

	code := 0
	errMsg := ""
	if runErr != nil {
		if ctx.Err() != nil {
			code = -1
			errMsg = ctx.Err().Error()
		} else if exitErr, ok := runErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
			errMsg = runErr.Error()
		}
	}
	return []models.ExecutionResult{{
		HostName:   limitOrAll(limit),
		ExitCode:   code,
		Stdout:     outBuf.String(),
		Stderr:     errBuf.String(),
		DurationMs: time.Since(start).Milliseconds(),
		Engine:     engineLabel,
		Venv:       venvPath,
		Error:      errMsg,
	}}, nil
}

func readPipeLines(r io.Reader, buf *bytes.Buffer, emit func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 32*1024), 1024*1024)
	for scanner.Scan() {
		ln := scanner.Text()
		buf.WriteString(ln)
		buf.WriteByte('\n')
		if emit != nil {
			emit(ln)
		}
	}
}

type ansiblePlay struct {
	Name   string           `yaml:"name"`
	Hosts  string           `yaml:"hosts"`
	Become bool             `yaml:"become"`
	Vars   map[string]any   `yaml:"vars"`
	Tasks  []map[string]any `yaml:"tasks"`
}

func runWithBuiltinSSHEngine(ctx context.Context, playbookPath string, hosts []*models.Host, extraVars map[string]string, pbOpts PlaybookOptions, opts sshclient.Options) ([]models.ExecutionResult, error) {
	data, err := os.ReadFile(playbookPath)
	if err != nil {
		return nil, fmt.Errorf("read playbook %s: %w", playbookPath, err)
	}
	var plays []ansiblePlay
	if err := yaml.Unmarshal(data, &plays); err != nil {
		return nil, fmt.Errorf("parse playbook YAML: %w", err)
	}

	results := make([]models.ExecutionResult, len(hosts))
	var outMu sync.Mutex
	emit := func(line string) {
		if pbOpts.OnOutput != nil {
			outMu.Lock()
			pbOpts.OnOutput(line)
			outMu.Unlock()
		}
	}

	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(idx int, host *models.Host) {
			defer wg.Done()
			start := time.Now()
			var logOut, logErr strings.Builder
			writeLine := func(s string) {
				logOut.WriteString(s)
				logOut.WriteByte('\n')
				emit(fmt.Sprintf("[%s] %s", host.Name, s))
			}

			writeLine(fmt.Sprintf("[duchess-ssh-playbook] ansible-playbook not found locally; using built-in SSH runner on %s (%s)", host.Name, host.Address))

			client, err := sshclient.Connect(ctx, host, opts)
			if err != nil {
				emit(fmt.Sprintf("[%s] SSH error: %v", host.Name, err))
				results[idx] = models.ExecutionResult{
					HostName:   host.Name,
					Address:    host.Address,
					ExitCode:   -1,
					Stdout:     logOut.String(),
					DurationMs: time.Since(start).Milliseconds(),
					Engine:     "duchess-ssh-playbook",
					Error:      err.Error(),
				}
				return
			}
			defer client.Close()

			exitCode := 0
			for _, play := range plays {
				writeLine(fmt.Sprintf("PLAY [%s] *************************************************", play.Name))
				vars := map[string]string{"inventory_hostname": host.Name}
				for k, v := range play.Vars {
					vars[k] = fmt.Sprintf("%v", v)
				}
				for k, v := range extraVars {
					vars[k] = v
				}
				registered := make(map[string]string)

				for _, task := range play.Tasks {
					tName, _ := task["name"].(string)
					if tName == "" {
						tName = "Unnamed task"
					}
					writeLine(fmt.Sprintf("TASK [%s] ---", renderVars(tName, vars, registered)))

					become := play.Become || pbOpts.Become
					shellCmd, isDebug, debugMsg := translateTaskToShell(task, vars, registered, become)
					if isDebug {
						writeLine(fmt.Sprintf("  ok: [%s] => %s", host.Name, debugMsg))
						continue
					}
					if shellCmd == "" {
						writeLine(fmt.Sprintf("  skipping: [%s] (unsupported module in lightweight runner)", host.Name))
						continue
					}
					if pbOpts.CheckMode {
						writeLine(fmt.Sprintf("  check: [%s] would run: %s", host.Name, shellCmd))
						continue
					}
					out, errStr, code, runErr := client.Run(ctx, shellCmd)
					if regName, ok := task["register"].(string); ok && regName != "" {
						registered[regName+".stdout"] = strings.TrimSpace(out)
					}
					if out != "" {
						for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
							writeLine("  " + ln)
						}
					}
					if errStr != "" {
						logErr.WriteString(errStr)
					}
					if runErr != nil || code != 0 {
						exitCode = code
						if exitCode == 0 {
							exitCode = 1
						}
						writeLine(fmt.Sprintf("  FAILED: [%s] exit=%d", host.Name, exitCode))
						break
					}
					writeLine(fmt.Sprintf("  ok: [%s]", host.Name))
				}
				if exitCode != 0 {
					break
				}
			}

			results[idx] = models.ExecutionResult{
				HostName:   host.Name,
				Address:    host.Address,
				ExitCode:   exitCode,
				Stdout:     logOut.String(),
				Stderr:     logErr.String(),
				DurationMs: time.Since(start).Milliseconds(),
				Engine:     "duchess-ssh-playbook",
			}
		}(i, h)
	}
	wg.Wait()
	return results, nil
}

func translateTaskToShell(task map[string]any, vars, reg map[string]string, become bool) (cmd string, isDebug bool, debugMsg string) {
	prefix := ""
	if become {
		prefix = "sudo "
	}
	for _, key := range []string{"ansible.builtin.debug", "debug"} {
		if d, ok := task[key]; ok {
			return "", true, renderVars(fmt.Sprintf("%v", d), vars, reg)
		}
	}
	for _, key := range []string{"ansible.builtin.command", "command", "ansible.builtin.shell", "shell"} {
		if v, ok := task[key]; ok {
			if s, ok := v.(string); ok {
				return prefix + renderVars(s, vars, reg), false, ""
			}
		}
	}
	for _, key := range []string{"ansible.builtin.systemd", "systemd", "ansible.builtin.service", "service"} {
		if m, ok := task[key].(map[string]any); ok {
			name := renderVars(fmt.Sprintf("%v", m["name"]), vars, reg)
			state := renderVars(fmt.Sprintf("%v", m["state"]), vars, reg)
			action := "status"
			switch state {
			case "restarted":
				action = "restart"
			case "reloaded":
				action = "reload"
			case "started":
				action = "start"
			case "stopped":
				action = "stop"
			}
			return fmt.Sprintf("%ssystemctl %s %s", prefix, action, name), false, ""
		}
	}
	for _, key := range []string{"ansible.builtin.apt", "apt"} {
		if m, ok := task[key].(map[string]any); ok {
			pkg := renderVars(fmt.Sprintf("%v", m["name"]), vars, reg)
			return fmt.Sprintf("%sapt-get update && %sapt-get install -y %s", prefix, prefix, pkg), false, ""
		}
	}
	for _, key := range []string{"ansible.builtin.file", "file"} {
		if m, ok := task[key].(map[string]any); ok {
			dest := renderVars(fmt.Sprintf("%v", m["dest"]), vars, reg)
			if dest == "" {
				dest = renderVars(fmt.Sprintf("%v", m["path"]), vars, reg)
			}
			state := fmt.Sprintf("%v", m["state"])
			if state == "link" {
				src := renderVars(fmt.Sprintf("%v", m["src"]), vars, reg)
				return fmt.Sprintf("%sln -sfn %s %s", prefix, src, dest), false, ""
			}
			if state == "directory" {
				return fmt.Sprintf("%smkdir -p %s", prefix, dest), false, ""
			}
		}
	}
	return "", false, ""
}

func renderVars(input string, vars, reg map[string]string) string {
	out := input
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{ "+k+" }}", v)
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	for k, v := range reg {
		out = strings.ReplaceAll(out, "{{ "+k+" }}", v)
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

func limitOrAll(limit string) string {
	if limit == "" {
		return "all"
	}
	return limit
}
