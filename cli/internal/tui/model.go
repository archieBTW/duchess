package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/duchess-devops/duchess-cli/internal/config"
	"github.com/duchess-devops/duchess-cli/internal/history"
	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/runner"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
)

type mainTab int

const (
	tabOverview mainTab = iota
	tabInspector
	tabInventory
	tabAutomation
	tabLogs
)

type inspectorSubTab int

const (
	subContainers inspectorSubTab = iota
	subWebApps
	subServices
	subAdHocSSH
)

type modalKind int

const (
	modalNone modalKind = iota
	modalSshConfig
	modalAddHost
	modalImportInv
	modalRawInv
	modalRunAuto
	modalGenerate
	modalAddAuto
	modalOutput
	modalHelp
)

// ExecLogEntry stores a single playbook, script, ops action, or ad-hoc SSH run.
type ExecLogEntry struct {
	Timestamp  time.Time
	Title      string
	Kind       string // playbook | script | ssh | ops | cp
	Target     string
	Engine     string
	Venv       string
	ExitCode   int
	DurationMs int64
	Stdout     string
	Stderr     string
	Error      string
}

func (e ExecLogEntry) Succeeded() bool {
	return e.ExitCode == 0 && e.Error == ""
}

func fromHistoryEntry(h history.Entry) ExecLogEntry {
	return ExecLogEntry{
		Timestamp:  h.Timestamp,
		Title:      h.Title,
		Kind:       h.Kind,
		Target:     h.Target,
		Engine:     h.Engine,
		Venv:       h.Venv,
		ExitCode:   h.ExitCode,
		DurationMs: h.DurationMs,
		Stdout:     h.Stdout,
		Stderr:     h.Stderr,
		Error:      h.Error,
	}
}

func (e ExecLogEntry) toHistoryEntry() history.Entry {
	return history.Entry{
		Timestamp:  e.Timestamp,
		Title:      e.Title,
		Kind:       e.Kind,
		Target:     e.Target,
		Engine:     e.Engine,
		Venv:       e.Venv,
		ExitCode:   e.ExitCode,
		DurationMs: e.DurationMs,
		Stdout:     e.Stdout,
		Stderr:     e.Stderr,
		Error:      e.Error,
	}
}

// Config configures the interactive TUI session.
type Config struct {
	WorkspaceDir      string
	InventoryPath     string
	SSHOptions        sshclient.Options
	WorkspaceConfig   config.WorkspaceConfig
	PromptSSHOnLaunch bool
}

// Model is the root Bubble Tea state for the Duchess DevOps TUI Dashboard.
type Model struct {
	cfg   Config
	wsCfg config.WorkspaceConfig

	width  int
	height int

	activeTab    mainTab
	inspectorSub inspectorSubTab
	modal        modalKind

	inv         *models.Inventory
	hosts       []*models.Host
	rawInvText  string
	automations []models.AutomationItem
	venvs       []runner.VenvInfo

	// Telemetry & discovery state
	reports    map[string]*models.DiscoveryReport
	cpuHistory map[string][]float64
	memHistory map[string][]float64
	scanning   map[string]bool

	// Navigation cursors & search
	hostIdx          int
	invCursor        int
	autoCursor       int
	autoFilter       string // all | playbook | script
	logFilter        string // all | failed | playbook | script | ssh | ops
	inspectorRow     int
	inspectorScroll  int
	autoSourceScroll int
	autoOutputScroll int
	logCursor        int
	logScroll        int
	searchActive     bool
	searchInput      textinput.Model
	searchQuery      string

	// Execution, live streaming & ad-hoc SSH state
	execLogs     []ExecLogEntry
	runningExec  bool
	execCancel   context.CancelFunc
	streamCh     chan tea.Msg
	liveOutput   []string
	liveKind     string // playbook | script | ssh | ops
	adHocInput   textinput.Model
	adHocFocus   bool
	adHocScope   string // host | group | all
	lastAdHocLog *ExecLogEntry
	lastAutoLog  *ExecLogEntry

	// Output viewer modal (quick ops, logs, configs, host alerts)
	outputTitle  string
	outputBody   string
	outputScroll int

	// Modal form fields
	inputs         []textinput.Model
	focusIdx       int
	genKind        string // playbook | script
	genTemplateIdx int
	genBecome      bool
	runCheckMode   bool
	runDiffMode    bool
	runBecome      bool
	runVerbosity   int
	runVenvIdx     int

	// Session SSH overrides & live auto-refresh
	sshOpts         sshclient.Options
	sshPool         *sshclient.Pool
	autoRefresh     bool
	refreshTick     int
	autoScanPending bool

	spin      spinner.Model
	statusMsg string
	statusErr string
}

type scanDoneMsg struct {
	hostName string
	report   *models.DiscoveryReport
}

type statsDoneMsg struct {
	hostName string
	stats    *models.ServerStats
}

type streamLineMsg struct {
	line string
}

type execDoneMsg struct {
	entry     ExecLogEntry
	openModal bool
}

type externalProcDoneMsg struct {
	kind string // ssh | editor | container-exec
	err  error
}

type autoRefreshTickMsg time.Time

// NewModel builds an initialized TUI model loading workspace config, inventory, playbooks, scripts, venvs, and history.
func NewModel(cfg Config) Model {
	wsCfg := cfg.WorkspaceConfig
	if wsCfg.PollIntervalSeconds <= 0 {
		wsCfg = config.Load(cfg.WorkspaceDir)
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorCyan)

	adHoc := textinput.New()
	adHoc.Placeholder = "uname -a && uptime"
	adHoc.SetValue("uname -a && uptime")
	adHoc.Width = 56

	search := textinput.New()
	search.Placeholder = "filter hosts, groups, playbooks, or logs..."
	search.Width = 40

	m := Model{
		cfg:             cfg,
		wsCfg:           wsCfg,
		width:           120,
		height:          36,
		activeTab:       tabOverview,
		autoFilter:      "all",
		logFilter:       "all",
		adHocScope:      "host",
		reports:         make(map[string]*models.DiscoveryReport),
		cpuHistory:      make(map[string][]float64),
		memHistory:      make(map[string][]float64),
		scanning:        make(map[string]bool),
		adHocInput:      adHoc,
		searchInput:     search,
		sshOpts:         cfg.SSHOptions,
		sshPool:         sshclient.NewPool(),
		autoRefresh:     true,
		autoScanPending: true,
		genKind:         "playbook",
		genBecome:       true,
		spin:            sp,
		statusMsg:       fmt.Sprintf("Auto-refresh ON (%ds stats) — [!] SSH  [/] Search  [k] Creds  [?] Help", wsCfg.PollIntervalSeconds),
	}
	m.reloadWorkspace()
	m.loadPersistentHistory()

	if cfg.PromptSSHOnLaunch && cfg.SSHOptions.User == "" {
		m.openSshConfigModal()
	} else if len(m.hosts) > 0 {
		for _, h := range m.hosts {
			m.scanning[h.Name] = true
		}
		m.statusMsg = fmt.Sprintf("Scanning %d server(s) over SSH...", len(m.hosts))
	}
	return m
}

func (m *Model) loadPersistentHistory() {
	entries, err := history.LoadRecent(m.wsCfg.EffectiveHistoryDir(m.cfg.WorkspaceDir), 100)
	if err != nil || len(entries) == 0 {
		return
	}
	m.execLogs = make([]ExecLogEntry, 0, len(entries))
	for _, e := range entries {
		m.execLogs = append(m.execLogs, fromHistoryEntry(e))
	}
}

func (m Model) effectiveUser(h *models.Host) string {
	if m.sshOpts.User != "" {
		return m.sshOpts.User
	}
	if h != nil && h.User != "" {
		return h.User
	}
	return inventory.DefaultSSHUser()
}

func (m Model) effectivePort(h *models.Host) int {
	if m.sshOpts.Port > 0 {
		return m.sshOpts.Port
	}
	if h != nil && h.Port > 0 {
		return h.Port
	}
	return 22
}

func (m Model) effectiveJump(h *models.Host) string {
	if m.sshOpts.JumpHost != "" {
		return m.sshOpts.JumpHost
	}
	if h != nil && h.JumpHost != "" {
		return h.JumpHost
	}
	return ""
}

// Run starts the full-screen Bubble Tea TUI program.
func Run(cfg Config) error {
	m := NewModel(cfg)
	defer m.sshPool.CloseAll()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick}
	if m.modal != modalNone {
		cmds = append(cmds, textinput.Blink)
		return tea.Batch(cmds...)
	}
	if m.autoScanPending && len(m.hosts) > 0 {
		for _, h := range m.hosts {
			cmds = append(cmds, scanHostCmd(m.sshPool, h, m.sshOpts))
		}
	}
	if m.autoRefresh {
		cmds = append(cmds, m.tickAutoRefresh())
	}
	return tea.Batch(cmds...)
}

func (m *Model) reloadWorkspace() {
	if loaded := config.Load(m.cfg.WorkspaceDir); loaded.ConfigPath != "" {
		m.wsCfg = loaded
	}
	inv, err := inventory.Load(m.cfg.InventoryPath)
	if err != nil {
		inv = inventory.New(m.cfg.InventoryPath)
		m.statusErr = fmt.Sprintf("Inventory load warning: %v", err)
	}
	m.inv = inv
	m.hosts = inventory.SortedHosts(inv)
	if raw, err := os.ReadFile(m.cfg.InventoryPath); err == nil {
		m.rawInvText = string(raw)
	} else {
		m.rawInvText = ""
	}
	if m.hostIdx >= len(m.hosts) {
		m.hostIdx = maxInt(0, len(m.hosts)-1)
	}
	if m.invCursor >= len(m.hosts) {
		m.invCursor = maxInt(0, len(m.hosts)-1)
	}

	pbs, _ := runner.ListItems(filepath.Join(m.cfg.WorkspaceDir, "playbooks"), "playbook")
	for i := range pbs {
		if pbs[i].Venv == "" {
			pbs[i].Venv = m.wsCfg.MatchPlaybookVenv(pbs[i].Path)
		}
	}
	scs, _ := runner.ListItems(filepath.Join(m.cfg.WorkspaceDir, "scripts"), "script")
	m.automations = append(pbs, scs...)
	m.venvs = runner.DiscoverVenvs(m.cfg.WorkspaceDir)

	filtered := m.filteredAutomations()
	if m.autoCursor >= len(filtered) {
		m.autoCursor = maxInt(0, len(filtered)-1)
	}
}

func (m Model) filteredHosts() []*models.Host {
	q := strings.ToLower(strings.TrimSpace(m.searchQuery))
	if q == "" {
		return m.hosts
	}
	var out []*models.Host
	for _, h := range m.hosts {
		if strings.Contains(strings.ToLower(h.Name), q) ||
			strings.Contains(strings.ToLower(h.Address), q) ||
			strings.Contains(strings.ToLower(strings.Join(h.Groups, ",")), q) {
			out = append(out, h)
		}
	}
	return out
}

func (m Model) selectedHost() *models.Host {
	list := m.filteredHosts()
	if len(list) == 0 {
		if len(m.hosts) == 0 {
			return nil
		}
		return m.hosts[0]
	}
	idx := m.hostIdx
	if idx < 0 || idx >= len(list) {
		idx = 0
	}
	return list[idx]
}

func (m Model) selectedInventoryHost() *models.Host {
	list := m.filteredHosts()
	if len(list) == 0 {
		return nil
	}
	idx := m.invCursor
	if idx < 0 || idx >= len(list) {
		idx = 0
	}
	return list[idx]
}

func (m Model) filteredAutomations() []models.AutomationItem {
	q := strings.ToLower(strings.TrimSpace(m.searchQuery))
	var out []models.AutomationItem
	for _, it := range m.automations {
		if m.autoFilter != "all" && it.Kind != m.autoFilter {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(it.Name), q) &&
			!strings.Contains(strings.ToLower(it.FileName), q) &&
			!strings.Contains(strings.ToLower(it.Category), q) &&
			!strings.Contains(strings.ToLower(it.Description), q) {
			continue
		}
		out = append(out, it)
	}
	return out
}

func (m Model) filteredLogs() []ExecLogEntry {
	q := strings.ToLower(strings.TrimSpace(m.searchQuery))
	var out []ExecLogEntry
	for _, e := range m.execLogs {
		switch m.logFilter {
		case "failed":
			if e.Succeeded() {
				continue
			}
		case "all", "":
		default:
			if !strings.EqualFold(e.Kind, m.logFilter) {
				continue
			}
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(e.Title), q) &&
			!strings.Contains(strings.ToLower(e.Target), q) &&
			!strings.Contains(strings.ToLower(e.Engine), q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (m Model) selectedAutomation() *models.AutomationItem {
	list := m.filteredAutomations()
	if len(list) == 0 {
		return nil
	}
	idx := m.autoCursor
	if idx < 0 || idx >= len(list) {
		idx = 0
	}
	item := list[idx]
	return &item
}

func (m Model) anyScanning() bool {
	return len(m.scanning) > 0
}

func (m Model) totalAlerts() (total, critical int) {
	for _, r := range m.reports {
		if r == nil || r.Error != "" {
			continue
		}
		for _, a := range r.Alerts {
			total++
			if a.Severity == "critical" {
				critical++
			}
		}
	}
	return total, critical
}

func (m Model) tickAutoRefresh() tea.Cmd {
	sec := m.wsCfg.PollIntervalSeconds
	if sec <= 0 {
		sec = 10
	}
	return tea.Tick(time.Duration(sec)*time.Second, func(t time.Time) tea.Msg {
		return autoRefreshTickMsg(t)
	})
}

func waitStreamCmd(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func scanHostCmd(pool *sshclient.Pool, host *models.Host, opts sshclient.Options) tea.Cmd {
	hCopy := *host
	return func() tea.Msg {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = 12 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout+15*time.Second)
		defer cancel()
		client, err := pool.Get(ctx, &hCopy, opts)
		if err != nil {
			return scanDoneMsg{
				hostName: hCopy.Name,
				report: &models.DiscoveryReport{
					HostName:    hCopy.Name,
					Address:     hCopy.Address,
					CollectedAt: time.Now(),
					Error:       err.Error(),
				},
			}
		}
		rep := telemetryDiscover(ctx, client, &hCopy)
		if rep.Error != "" {
			pool.Invalidate(&hCopy, opts)
		}
		return scanDoneMsg{hostName: hCopy.Name, report: rep}
	}
}

func collectStatsCmd(pool *sshclient.Pool, host *models.Host, opts sshclient.Options) tea.Cmd {
	hCopy := *host
	return func() tea.Msg {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
		defer cancel()
		client, err := pool.Get(ctx, &hCopy, opts)
		if err != nil {
			return statsDoneMsg{
				hostName: hCopy.Name,
				stats: &models.ServerStats{
					HostName:    hCopy.Name,
					Address:     hCopy.Address,
					CollectedAt: time.Now(),
					Error:       err.Error(),
				},
			}
		}
		st := telemetryCollectStats(ctx, client, &hCopy)
		if st.Error != "" {
			pool.Invalidate(&hCopy, opts)
		}
		return statsDoneMsg{hostName: hCopy.Name, stats: st}
	}
}

// startAdHocFanOut streams an ad-hoc SSH command across 1 or many hosts concurrently.
func startAdHocFanOut(ctx context.Context, hosts []*models.Host, targetLabel, command string, opts sshclient.Options, ch chan<- tea.Msg) {
	go func() {
		defer close(ch)
		start := time.Now()
		results := make([]models.ExecutionResult, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func(idx int, host *models.Host) {
				defer wg.Done()
				hStart := time.Now()
				client, err := sshclient.Connect(ctx, host, opts)
				if err != nil {
					ch <- streamLineMsg{line: fmt.Sprintf("[%s] SSH error: %v", host.Name, err)}
					results[idx] = models.ExecutionResult{
						HostName:   host.Name,
						Address:    host.Address,
						ExitCode:   -1,
						DurationMs: time.Since(hStart).Milliseconds(),
						Engine:     "ssh-exec",
						Error:      err.Error(),
					}
					return
				}
				defer client.Close()
				stdout, stderr, code, runErr := client.RunStreaming(ctx, command, func(stream, line string) {
					if len(hosts) > 1 {
						ch <- streamLineMsg{line: fmt.Sprintf("[%s] %s", host.Name, line)}
					} else {
						ch <- streamLineMsg{line: line}
					}
				})
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
					DurationMs: time.Since(hStart).Milliseconds(),
					Engine:     "ssh-exec",
					Error:      errMsg,
				}
			}(i, h)
		}
		wg.Wait()

		var outBuf, errBuf strings.Builder
		exitCode := 0
		firstErr := ""
		for _, r := range results {
			if len(hosts) > 1 {
				outBuf.WriteString(fmt.Sprintf("=== %s (%s) exit=%d (%dms) ===\n", r.HostName, r.Address, r.ExitCode, r.DurationMs))
			}
			if strings.TrimSpace(r.Stdout) != "" {
				outBuf.WriteString(strings.TrimSpace(r.Stdout) + "\n")
			}
			if strings.TrimSpace(r.Stderr) != "" {
				if len(hosts) > 1 {
					errBuf.WriteString(fmt.Sprintf("[%s] %s\n", r.HostName, strings.TrimSpace(r.Stderr)))
				} else {
					errBuf.WriteString(strings.TrimSpace(r.Stderr) + "\n")
				}
			}
			if r.Error != "" {
				if firstErr == "" {
					firstErr = r.Error
				}
				errBuf.WriteString(fmt.Sprintf("[%s] %s\n", r.HostName, r.Error))
			}
			if r.ExitCode != 0 {
				exitCode = r.ExitCode
			}
		}

		ch <- execDoneMsg{entry: ExecLogEntry{
			Timestamp:  time.Now(),
			Title:      command,
			Kind:       "ssh",
			Target:     targetLabel,
			Engine:     "ssh-exec",
			ExitCode:   exitCode,
			DurationMs: time.Since(start).Milliseconds(),
			Stdout:     outBuf.String(),
			Stderr:     errBuf.String(),
			Error:      firstErr,
		}}
	}()
}

// startQuickOp runs a quick inspector operation (container logs/restart, journalctl, nginx -t, etc.).
func startQuickOp(ctx context.Context, host *models.Host, title, command string, opts sshclient.Options, ch chan<- tea.Msg) {
	hCopy := *host
	go func() {
		defer close(ch)
		start := time.Now()
		client, err := sshclient.Connect(ctx, &hCopy, opts)
		if err != nil {
			ch <- execDoneMsg{
				openModal: true,
				entry: ExecLogEntry{
					Timestamp:  time.Now(),
					Title:      title,
					Kind:       "ops",
					Target:     hCopy.Name,
					Engine:     "ssh-ops",
					ExitCode:   -1,
					DurationMs: time.Since(start).Milliseconds(),
					Error:      err.Error(),
				},
			}
			return
		}
		defer client.Close()
		stdout, stderr, code, runErr := client.RunStreaming(ctx, command, func(stream, line string) {
			ch <- streamLineMsg{line: line}
		})
		errMsg := ""
		if runErr != nil {
			errMsg = runErr.Error()
		}
		ch <- execDoneMsg{
			openModal: true,
			entry: ExecLogEntry{
				Timestamp:  time.Now(),
				Title:      title,
				Kind:       "ops",
				Target:     hCopy.Name,
				Engine:     "ssh-ops",
				ExitCode:   code,
				DurationMs: time.Since(start).Milliseconds(),
				Stdout:     stdout,
				Stderr:     stderr,
				Error:      errMsg,
			},
		}
	}()
}

// startAutomationStreaming runs a playbook or script with real-time line streaming and cancellation.
func startAutomationStreaming(
	ctx context.Context,
	inv *models.Inventory,
	invPath string,
	item models.AutomationItem,
	target string,
	vars map[string]string,
	pbOpts runner.PlaybookOptions,
	opts sshclient.Options,
	ch chan<- tea.Msg,
) {
	hosts := inventory.ResolveTargets(inv, target)
	go func() {
		defer close(ch)
		start := time.Now()
		if len(hosts) == 0 {
			ch <- execDoneMsg{entry: ExecLogEntry{
				Timestamp:  time.Now(),
				Title:      item.Name,
				Kind:       item.Kind,
				Target:     target,
				Engine:     "duchess",
				ExitCode:   -1,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      fmt.Sprintf("No enrolled hosts matched target %q", target),
			}}
			return
		}

		var results []models.ExecutionResult
		var runErr error
		if item.Kind == "playbook" {
			pbOpts.ItemVenv = item.Venv
			if pbOpts.RemoteVenv == "" {
				pbOpts.RemoteVenv = item.RemoteVenv
			}
			pbOpts.OnOutput = func(line string) {
				ch <- streamLineMsg{line: line}
			}
			results, runErr = runner.RunPlaybookWithOptions(ctx, invPath, item.Path, target, hosts, vars, pbOpts, opts)
		} else {
			results, runErr = runner.RunScriptStreaming(ctx, item.Path, hosts, vars, opts, func(host, stream, line string) {
				ch <- streamLineMsg{line: fmt.Sprintf("[%s] %s", host, line)}
			})
		}

		if runErr != nil {
			ch <- execDoneMsg{entry: ExecLogEntry{
				Timestamp:  time.Now(),
				Title:      item.Name,
				Kind:       item.Kind,
				Target:     target,
				Engine:     item.Kind,
				Venv:       pbOpts.Venv,
				ExitCode:   -1,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      runErr.Error(),
			}}
			return
		}

		var outBuf, errBuf strings.Builder
		exitCode := 0
		engine := item.Kind
		usedVenv := ""
		firstErr := ""
		for _, r := range results {
			if r.Engine != "" {
				engine = r.Engine
			}
			if r.Venv != "" {
				usedVenv = r.Venv
			}
			outBuf.WriteString(fmt.Sprintf("=== %s [%s] exit=%d (%dms) ===\n", r.HostName, r.Engine, r.ExitCode, r.DurationMs))
			if strings.TrimSpace(r.Stdout) != "" {
				outBuf.WriteString(strings.TrimSpace(r.Stdout) + "\n")
			}
			if strings.TrimSpace(r.Stderr) != "" {
				errBuf.WriteString(fmt.Sprintf("[%s] %s\n", r.HostName, strings.TrimSpace(r.Stderr)))
			}
			if r.Error != "" {
				if firstErr == "" {
					firstErr = r.Error
				}
				errBuf.WriteString(fmt.Sprintf("[%s] %s\n", r.HostName, r.Error))
			}
			if r.ExitCode != 0 {
				exitCode = r.ExitCode
			}
		}

		ch <- execDoneMsg{entry: ExecLogEntry{
			Timestamp:  time.Now(),
			Title:      item.Name,
			Kind:       item.Kind,
			Target:     target,
			Engine:     engine,
			Venv:       usedVenv,
			ExitCode:   exitCode,
			DurationMs: time.Since(start).Milliseconds(),
			Stdout:     outBuf.String(),
			Stderr:     errBuf.String(),
			Error:      firstErr,
		}}
	}()
}

// openInteractiveSSHCmd suspends the TUI and drops into a real TTY SSH session.
func (m Model) openInteractiveSSHCmd(host *models.Host, remoteCmd string) tea.Cmd {
	args := []string{}
	port := m.effectivePort(host)
	if port > 0 && port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	key := host.PrivateKey
	if m.sshOpts.PrivateKey != "" {
		key = m.sshOpts.PrivateKey
	}
	if key != "" {
		args = append(args, "-i", inventory.ExpandHome(key))
	}
	if jump := m.effectiveJump(host); jump != "" {
		args = append(args, "-J", jump)
	}
	dest := fmt.Sprintf("%s@%s", m.effectiveUser(host), host.Address)
	if remoteCmd != "" {
		args = append(args, "-t", dest, remoteCmd)
	} else {
		args = append(args, dest)
	}
	kind := "ssh"
	if remoteCmd != "" {
		kind = "container-exec"
	}
	c := exec.Command("ssh", args...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return externalProcDoneMsg{kind: kind, err: err}
	})
}

// openEditorCmd suspends the TUI and opens filePath in $EDITOR / $VISUAL / vi.
func openEditorCmd(filePath string) tea.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		for _, cand := range []string{"nvim", "vim", "nano", "vi"} {
			if p, err := exec.LookPath(cand); err == nil {
				editor = p
				break
			}
		}
	}
	if editor == "" {
		editor = "vi"
	}
	fields := strings.Fields(editor)
	args := append(fields[1:], filePath)
	c := exec.Command(fields[0], args...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return externalProcDoneMsg{kind: "editor", err: err}
	})
}

func makeInput(placeholder, value string, width int, password bool) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.Width = width
	if password {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	return ti
}

func parseVarsString(raw string) map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		if idx := strings.IndexByte(pair, '='); idx > 0 {
			k := strings.TrimSpace(pair[:idx])
			v := strings.TrimSpace(pair[idx+1:])
			if k != "" {
				out[k] = v
			}
		}
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
