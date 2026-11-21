package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/duchess-devops/duchess-cli/internal/history"
	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/runner"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
	"github.com/duchess-devops/duchess-cli/internal/telemetry"
)

func telemetryDiscover(ctx context.Context, client *sshclient.Client, host *models.Host) *models.DiscoveryReport {
	return telemetry.Discover(ctx, client, host)
}

func telemetryCollectStats(ctx context.Context, client *sshclient.Client, host *models.Host) *models.ServerStats {
	return telemetry.CollectStats(ctx, client, host)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case externalProcDoneMsg:
		if msg.kind == "editor" {
			m.reloadWorkspace()
			if msg.err != nil {
				m.statusErr = fmt.Sprintf("Editor exited with error: %v", msg.err)
			} else {
				m.statusErr = ""
				m.statusMsg = "Reloaded workspace after editing"
			}
			return m, nil
		}
		if msg.err != nil {
			m.statusErr = fmt.Sprintf("Interactive session ended: %v", msg.err)
		} else {
			m.statusErr = ""
			m.statusMsg = "Returned from interactive SSH session"
		}
		return m, nil

	case autoRefreshTickMsg:
		if !m.autoRefresh {
			return m, nil
		}
		m.refreshTick++
		fullDiscovery := m.refreshTick%6 == 0 // Full container/service/webapp discovery every 6 cycles
		cmds := []tea.Cmd{m.tickAutoRefresh()}
		for _, h := range m.hosts {
			if m.scanning[h.Name] {
				continue
			}
			m.scanning[h.Name] = true
			existing := m.reports[h.Name]
			if fullDiscovery || existing == nil || existing.Error != "" {
				cmds = append(cmds, scanHostCmd(m.sshPool, h, m.sshOpts))
			} else {
				cmds = append(cmds, collectStatsCmd(m.sshPool, h, m.sshOpts))
			}
		}
		if fullDiscovery {
			m.statusMsg = "Auto-refreshing full fleet discovery..."
		} else {
			m.statusMsg = "Auto-refreshing lightweight /proc stats..."
		}
		return m, tea.Batch(cmds...)

	case statsDoneMsg:
		delete(m.scanning, msg.hostName)
		if msg.stats != nil && msg.stats.Error == "" {
			rep := m.reports[msg.hostName]
			if rep == nil {
				rep = &models.DiscoveryReport{
					HostName:    msg.stats.HostName,
					Address:     msg.stats.Address,
					CollectedAt: msg.stats.CollectedAt,
				}
				m.reports[msg.hostName] = rep
			}
			rep.Stats = msg.stats
			rep.Error = ""
			rep.CollectedAt = msg.stats.CollectedAt
			rep.Alerts = telemetry.EvaluateAlerts(rep, m.wsCfg.Alerts)
			m.cpuHistory[msg.hostName] = appendSample(m.cpuHistory[msg.hostName], msg.stats.CPUUsagePercent, 24)
			m.memHistory[msg.hostName] = appendSample(m.memHistory[msg.hostName], msg.stats.MemUsagePercent, 24)
			m.statusErr = ""
			m.statusMsg = fmt.Sprintf(
				"Stats updated %s: CPU %.1f%% • RAM %.1f%% (lightweight poll)",
				msg.hostName, msg.stats.CPUUsagePercent, msg.stats.MemUsagePercent,
			)
		} else if msg.stats != nil {
			if rep := m.reports[msg.hostName]; rep != nil {
				rep.Error = msg.stats.Error
			}
			m.statusErr = fmt.Sprintf("Stats %s failed: %s", msg.hostName, msg.stats.Error)
		}
		return m, nil

	case scanDoneMsg:
		m.autoScanPending = false
		delete(m.scanning, msg.hostName)
		if msg.report != nil && msg.report.Error == "" {
			msg.report.Alerts = telemetry.EvaluateAlerts(msg.report, m.wsCfg.Alerts)
		}
		m.reports[msg.hostName] = msg.report
		if msg.report != nil && msg.report.Error == "" && msg.report.Stats != nil {
			m.cpuHistory[msg.hostName] = appendSample(m.cpuHistory[msg.hostName], msg.report.Stats.CPUUsagePercent, 24)
			m.memHistory[msg.hostName] = appendSample(m.memHistory[msg.hostName], msg.report.Stats.MemUsagePercent, 24)
			m.statusErr = ""
			alertNote := ""
			if len(msg.report.Alerts) > 0 {
				alertNote = fmt.Sprintf(" • %d alert(s)", len(msg.report.Alerts))
			}
			m.statusMsg = fmt.Sprintf(
				"Discovered %s: %d container(s), %d web app(s), %d service(s)%s",
				msg.hostName, len(msg.report.Containers), len(msg.report.WebApps), len(msg.report.Services), alertNote,
			)
		} else if msg.report != nil {
			m.statusErr = fmt.Sprintf("Scan %s failed: %s", msg.hostName, msg.report.Error)
		}
		return m, nil

	case streamLineMsg:
		m.liveOutput = append(m.liveOutput, msg.line)
		if len(m.liveOutput) > 1000 {
			m.liveOutput = m.liveOutput[len(m.liveOutput)-1000:]
		}
		if m.streamCh != nil {
			return m, waitStreamCmd(m.streamCh)
		}
		return m, nil

	case execDoneMsg:
		m.runningExec = false
		if m.execCancel != nil {
			m.execCancel()
			m.execCancel = nil
		}
		m.streamCh = nil
		m.execLogs = append([]ExecLogEntry{msg.entry}, m.execLogs...)
		_ = history.Append(m.wsCfg.EffectiveHistoryDir(m.cfg.WorkspaceDir), msg.entry.toHistoryEntry())

		if msg.entry.Kind == "ssh" {
			copyEntry := msg.entry
			m.lastAdHocLog = &copyEntry
		} else if msg.entry.Kind == "playbook" || msg.entry.Kind == "script" {
			copyEntry := msg.entry
			m.lastAutoLog = &copyEntry
		}
		if msg.openModal {
			body := strings.TrimSpace(msg.entry.Stdout)
			if strings.TrimSpace(msg.entry.Stderr) != "" {
				if body != "" {
					body += "\n--- STDERR ---\n"
				}
				body += strings.TrimSpace(msg.entry.Stderr)
			}
			if msg.entry.Error != "" {
				if body != "" {
					body += "\n--- ERROR ---\n"
				}
				body += msg.entry.Error
			}
			if body == "" {
				body = "(no output)"
			}
			m.outputTitle = fmt.Sprintf("%s (%s • exit %d • %dms)", msg.entry.Title, msg.entry.Target, msg.entry.ExitCode, msg.entry.DurationMs)
			m.outputBody = body
			m.outputScroll = 0
			m.modal = modalOutput
		}
		if msg.entry.Succeeded() {
			m.statusErr = ""
			m.statusMsg = fmt.Sprintf("Completed %s on %s (%dms)", msg.entry.Title, msg.entry.Target, msg.entry.DurationMs)
		} else {
			m.statusErr = fmt.Sprintf("%s exited %d on %s", msg.entry.Title, msg.entry.ExitCode, msg.entry.Target)
		}
		return m, nil

	case tea.KeyMsg:
		// Modal captures all keystrokes
		if m.modal != modalNone {
			return m.updateModal(msg)
		}

		// Cancel in-flight streaming execution with Ctrl+X
		if msg.String() == "ctrl+x" && m.runningExec && m.execCancel != nil {
			m.execCancel()
			m.statusMsg = "Cancelling in-flight execution..."
			return m, nil
		}

		// Fuzzy search filter capture when active
		if m.searchActive {
			switch msg.String() {
			case "esc":
				m.searchActive = false
				m.searchQuery = ""
				m.searchInput.SetValue("")
				m.searchInput.Blur()
				return m, nil
			case "enter":
				m.searchActive = false
				m.searchInput.Blur()
				return m, nil
			default:
				var cmd tea.Cmd
				m.searchInput, cmd = m.searchInput.Update(msg)
				m.searchQuery = m.searchInput.Value()
				m.hostIdx = 0
				m.invCursor = 0
				m.autoCursor = 0
				m.logCursor = 0
				return m, cmd
			}
		}

		// Ad-hoc SSH command input capture when focused
		if m.activeTab == tabInspector && m.inspectorSub == subAdHocSSH && m.adHocFocus {
			switch msg.String() {
			case "esc":
				m.adHocFocus = false
				m.adHocInput.Blur()
				return m, nil
			case "ctrl+g":
				m.cycleAdHocScope()
				return m, nil
			case "enter":
				host := m.selectedHost()
				cmdStr := strings.TrimSpace(m.adHocInput.Value())
				if host != nil && cmdStr != "" && !m.runningExec {
					return m.startAdHocExecution(host, cmdStr)
				}
				return m, nil
			default:
				var cmd tea.Cmd
				m.adHocInput, cmd = m.adHocInput.Update(msg)
				return m, cmd
			}
		}

		// Global shortcuts
		switch msg.String() {
		case "ctrl+c", "q":
			if m.execCancel != nil {
				m.execCancel()
			}
			return m, tea.Quit
		case "esc":
			if m.searchQuery != "" {
				m.searchQuery = ""
				m.searchInput.SetValue("")
				m.statusMsg = "Cleared search filter"
				return m, nil
			}
		case "/":
			if m.activeTab != tabInspector {
				m.searchActive = true
				m.searchInput.Focus()
				return m, textinput.Blink
			}
		case "1":
			m.activeTab = tabOverview
			return m, nil
		case "2":
			m.activeTab = tabInspector
			return m, nil
		case "3":
			m.activeTab = tabInventory
			return m, nil
		case "4":
			m.activeTab = tabAutomation
			return m, nil
		case "5":
			m.activeTab = tabLogs
			return m, nil
		case "tab":
			m.activeTab = (m.activeTab + 1) % 5
			return m, nil
		case "shift+tab":
			m.activeTab = (m.activeTab + 4) % 5
			return m, nil
		case "k":
			m.openSshConfigModal()
			return m, textinput.Blink
		case "?":
			m.modal = modalHelp
			return m, nil
		case "r":
			m.reloadWorkspace()
			m.statusErr = ""
			m.statusMsg = fmt.Sprintf("Reloaded workspace (%d hosts, %d automations, %d venvs)", len(m.hosts), len(m.automations), len(m.venvs))
			return m, nil
		case "A":
			m.autoRefresh = !m.autoRefresh
			if m.autoRefresh {
				m.statusMsg = fmt.Sprintf("Live auto-refresh ENABLED (every %ds)", m.wsCfg.PollIntervalSeconds)
				return m, m.tickAutoRefresh()
			}
			m.statusMsg = "Live auto-refresh DISABLED"
			return m, nil
		case "S":
			return m.triggerScanAll()
		case "!":
			var target *models.Host
			if m.activeTab == tabInventory {
				target = m.selectedInventoryHost()
			} else {
				target = m.selectedHost()
			}
			if target != nil {
				return m, m.openInteractiveSSHCmd(target, "")
			}
		}

		// Tab-specific navigation & actions
		switch m.activeTab {
		case tabOverview:
			return m.updateOverview(msg)
		case tabInspector:
			return m.updateInspector(msg)
		case tabInventory:
			return m.updateInventory(msg)
		case tabAutomation:
			return m.updateAutomation(msg)
		case tabLogs:
			return m.updateLogs(msg)
		}
	}
	return m, nil
}

func (m *Model) cycleAdHocScope() {
	switch m.adHocScope {
	case "host":
		m.adHocScope = "group"
	case "group":
		m.adHocScope = "all"
	default:
		m.adHocScope = "host"
	}
}

func (m Model) startAdHocExecution(host *models.Host, cmdStr string) (tea.Model, tea.Cmd) {
	var targets []*models.Host
	targetLabel := fmt.Sprintf("%s (%s)", host.Name, host.Address)
	switch m.adHocScope {
	case "group":
		if len(host.Groups) > 0 {
			grp := host.Groups[0]
			targets = inventory.ResolveTargets(m.inv, grp)
			targetLabel = fmt.Sprintf("group:%s (%d hosts)", grp, len(targets))
		}
	case "all":
		targets = m.hosts
		targetLabel = fmt.Sprintf("all (%d hosts)", len(targets))
	}
	if len(targets) == 0 {
		targets = []*models.Host{host}
	}

	ctx, cancel := m.newExecContext()
	ch := make(chan tea.Msg, 256)
	m.execCancel = cancel
	m.streamCh = ch
	m.liveOutput = nil
	m.liveKind = "ssh"
	m.runningExec = true
	m.statusErr = ""
	m.statusMsg = fmt.Sprintf("Running `%s` on %s... (Ctrl+X to cancel)", cmdStr, targetLabel)
	startAdHocFanOut(ctx, targets, targetLabel, cmdStr, m.sshOpts, ch)
	return m, waitStreamCmd(ch)
}

func (m Model) newExecContext() (context.Context, context.CancelFunc) {
	if m.wsCfg.ExecTimeoutSeconds > 0 {
		return context.WithTimeout(context.Background(), time.Duration(m.wsCfg.ExecTimeoutSeconds)*time.Second)
	}
	return context.WithCancel(context.Background())
}

func (m Model) triggerScanSelected() (tea.Model, tea.Cmd) {
	h := m.selectedHost()
	if h == nil {
		m.statusErr = "No enrolled servers to scan"
		return m, nil
	}
	if m.scanning[h.Name] {
		return m, nil
	}
	m.scanning[h.Name] = true
	m.statusErr = ""
	m.statusMsg = fmt.Sprintf("Connecting via SSH to %s (%s)...", h.Name, h.Address)
	return m, scanHostCmd(m.sshPool, h, m.sshOpts)
}

func (m Model) triggerScanAll() (tea.Model, tea.Cmd) {
	if len(m.hosts) == 0 {
		m.statusErr = "No enrolled servers to scan"
		return m, nil
	}
	var cmds []tea.Cmd
	for _, h := range m.hosts {
		if !m.scanning[h.Name] {
			m.scanning[h.Name] = true
			cmds = append(cmds, scanHostCmd(m.sshPool, h, m.sshOpts))
		}
	}
	m.statusErr = ""
	m.statusMsg = fmt.Sprintf("Discovering %d server(s) over SSH...", len(cmds))
	return m, tea.Batch(cmds...)
}

func (m Model) updateOverview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	hosts := m.filteredHosts()
	switch msg.String() {
	case "up":
		if m.hostIdx > 0 {
			m.hostIdx--
		}
	case "down", "j":
		if m.hostIdx < len(hosts)-1 {
			m.hostIdx++
		}
	case "enter":
		if m.selectedHost() != nil {
			m.activeTab = tabInspector
		}
	case "s":
		return m.triggerScanSelected()
	case "a":
		return m.openAlertsModal(m.selectedHost()), nil
	}
	return m, nil
}

func (m Model) openAlertsModal(host *models.Host) Model {
	var b strings.Builder
	if host != nil {
		rep := m.reports[host.Name]
		if rep != nil && len(rep.Alerts) > 0 {
			for _, a := range rep.Alerts {
				b.WriteString(fmt.Sprintf("[%s] (%s) %s\n", strings.ToUpper(a.Severity), a.Category, a.Message))
			}
			m.outputTitle = fmt.Sprintf("HEALTH ALERTS — %s (%d)", host.Name, len(rep.Alerts))
		} else {
			b.WriteString("No active health alerts for " + host.Name + ".")
			m.outputTitle = fmt.Sprintf("HEALTH ALERTS — %s", host.Name)
		}
	} else {
		b.WriteString("No host selected.")
		m.outputTitle = "HEALTH ALERTS"
	}
	m.outputBody = b.String()
	m.outputScroll = 0
	m.modal = modalOutput
	return m
}

func (m Model) inspectorListLen() int {
	h := m.selectedHost()
	if h == nil {
		return 0
	}
	rep := m.reports[h.Name]
	if rep == nil {
		return 0
	}
	switch m.inspectorSub {
	case subContainers:
		return len(rep.Containers)
	case subWebApps:
		return len(rep.WebApps)
	case subServices:
		return len(rep.Services)
	}
	return 0
}

func (m Model) updateInspector(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	hosts := m.filteredHosts()
	switch msg.String() {
	case "[":
		if len(hosts) > 0 {
			m.hostIdx = (m.hostIdx - 1 + len(hosts)) % len(hosts)
			m.inspectorRow = 0
			m.inspectorScroll = 0
		}
	case "]":
		if len(hosts) > 0 {
			m.hostIdx = (m.hostIdx + 1) % len(hosts)
			m.inspectorRow = 0
			m.inspectorScroll = 0
		}
	case "left", "h":
		m.inspectorSub = (m.inspectorSub + 3) % 4
		m.inspectorRow = 0
		m.inspectorScroll = 0
	case "right":
		m.inspectorSub = (m.inspectorSub + 1) % 4
		m.inspectorRow = 0
		m.inspectorScroll = 0
	case "up":
		if m.inspectorSub == subAdHocSSH {
			if m.inspectorScroll > 0 {
				m.inspectorScroll--
			}
		} else if m.inspectorRow > 0 {
			m.inspectorRow--
			if m.inspectorRow < m.inspectorScroll {
				m.inspectorScroll = m.inspectorRow
			}
		}
	case "down", "j":
		if m.inspectorSub == subAdHocSSH {
			m.inspectorScroll++
		} else if maxLen := m.inspectorListLen(); m.inspectorRow < maxLen-1 {
			m.inspectorRow++
		}
	case "s":
		return m.triggerScanSelected()
	case "a":
		return m.openAlertsModal(m.selectedHost()), nil
	case "ctrl+g":
		if m.inspectorSub == subAdHocSSH {
			m.cycleAdHocScope()
		}
	case "enter", "i":
		if m.inspectorSub == subAdHocSSH {
			m.adHocFocus = true
			m.adHocInput.Focus()
			return m, textinput.Blink
		}
	case "l":
		// Actionable Logs for Containers & Services (and right sub-tab nav if on WebApps/AdHoc)
		if m.inspectorSub == subContainers || m.inspectorSub == subServices {
			return m.runInspectorAction("logs")
		}
		m.inspectorSub = (m.inspectorSub + 1) % 4
		m.inspectorRow = 0
		m.inspectorScroll = 0
	case "R":
		if m.inspectorSub == subContainers || m.inspectorSub == subServices {
			return m.runInspectorAction("restart")
		}
	case "x":
		if m.inspectorSub == subContainers {
			return m.runInspectorAction("toggle")
		}
	case "o":
		if m.inspectorSub == subServices {
			return m.runInspectorAction("reload")
		}
	case "E":
		if m.inspectorSub == subContainers {
			host := m.selectedHost()
			rep := m.reports[host.Name]
			if host != nil && rep != nil && m.inspectorRow < len(rep.Containers) {
				c := rep.Containers[m.inspectorRow]
				rt := c.Runtime
				if rt == "" {
					rt = "docker"
				}
				return m, m.openInteractiveSSHCmd(host, fmt.Sprintf("%s exec -it %s sh", rt, shellQuote(c.Name)))
			}
		}
	case "v":
		if m.inspectorSub == subWebApps {
			return m.runInspectorAction("view-config")
		}
	case "t":
		if m.inspectorSub == subWebApps {
			return m.runInspectorAction("test-config")
		}
	case "c":
		if m.inspectorSub == subWebApps {
			return m.runInspectorAction("cert-check")
		}
	}
	return m, nil
}

func (m Model) runInspectorAction(action string) (tea.Model, tea.Cmd) {
	if m.runningExec {
		m.statusErr = "Another command is already running (press Ctrl+X to cancel)"
		return m, nil
	}
	host := m.selectedHost()
	if host == nil {
		return m, nil
	}
	rep := m.reports[host.Name]
	if rep == nil {
		m.statusErr = "Scan server first ([s]) before running quick actions"
		return m, nil
	}

	var title, remoteCmd string
	switch m.inspectorSub {
	case subContainers:
		if len(rep.Containers) == 0 || m.inspectorRow >= len(rep.Containers) {
			return m, nil
		}
		c := rep.Containers[m.inspectorRow]
		rt := c.Runtime
		if rt == "" {
			rt = "docker"
		}
		switch action {
		case "logs":
			title = fmt.Sprintf("%s logs %s", rt, c.Name)
			remoteCmd = fmt.Sprintf("%s logs --tail 100 %s 2>&1", rt, shellQuote(c.Name))
		case "restart":
			title = fmt.Sprintf("%s restart %s", rt, c.Name)
			remoteCmd = fmt.Sprintf("%s restart %s && %s ps --filter name=%s", rt, shellQuote(c.Name), rt, shellQuote(c.Name))
		case "toggle":
			if strings.EqualFold(c.State, "running") {
				title = fmt.Sprintf("%s stop %s", rt, c.Name)
				remoteCmd = fmt.Sprintf("%s stop %s", rt, shellQuote(c.Name))
			} else {
				title = fmt.Sprintf("%s start %s", rt, c.Name)
				remoteCmd = fmt.Sprintf("%s start %s", rt, shellQuote(c.Name))
			}
		}

	case subServices:
		if len(rep.Services) == 0 || m.inspectorRow >= len(rep.Services) {
			return m, nil
		}
		svc := rep.Services[m.inspectorRow]
		switch action {
		case "logs":
			title = fmt.Sprintf("journalctl %s", svc.Name)
			remoteCmd = fmt.Sprintf("journalctl -u %s -n 100 --no-pager 2>&1", shellQuote(svc.Name))
		case "restart":
			title = fmt.Sprintf("restart %s", svc.Name)
			remoteCmd = fmt.Sprintf("sudo systemctl restart %s && systemctl status %s --no-pager -n 15", shellQuote(svc.Name), shellQuote(svc.Name))
		case "reload":
			title = fmt.Sprintf("reload %s", svc.Name)
			remoteCmd = fmt.Sprintf("sudo systemctl reload %s && systemctl is-active %s", shellQuote(svc.Name), shellQuote(svc.Name))
		}

	case subWebApps:
		if len(rep.WebApps) == 0 || m.inspectorRow >= len(rep.WebApps) {
			return m, nil
		}
		app := rep.WebApps[m.inspectorRow]
		switch action {
		case "view-config":
			if app.ConfigPath == "" {
				m.statusErr = "No config file recorded for " + app.Name
				return m, nil
			}
			title = fmt.Sprintf("Config: %s", app.ConfigPath)
			remoteCmd = fmt.Sprintf("cat %s", shellQuote(app.ConfigPath))
		case "test-config":
			switch app.ServerType {
			case "nginx":
				title = "nginx -t"
				remoteCmd = "sudo nginx -t 2>&1"
			case "apache":
				title = "apachectl configtest"
				remoteCmd = "sudo apachectl configtest 2>&1 || sudo httpd -t 2>&1"
			case "caddy":
				title = "caddy validate"
				remoteCmd = "caddy validate --config /etc/caddy/Caddyfile 2>&1"
			case "haproxy":
				title = "haproxy -c"
				remoteCmd = "sudo haproxy -c -f /etc/haproxy/haproxy.cfg 2>&1"
			default:
				m.statusErr = "Config test not supported for " + app.ServerType
				return m, nil
			}
		case "cert-check":
			if app.CertPath != "" {
				title = fmt.Sprintf("TLS Cert: %s", app.Name)
				remoteCmd = fmt.Sprintf("openssl x509 -noout -subject -issuer -dates -in %s 2>&1", shellQuote(app.CertPath))
			} else if len(app.Domains) > 0 && app.Domains[0] != "default" {
				title = fmt.Sprintf("TLS Probe: %s", app.Domains[0])
				remoteCmd = fmt.Sprintf("echo | openssl s_client -servername %s -connect 127.0.0.1:443 2>/dev/null | openssl x509 -noout -subject -issuer -dates", shellQuote(app.Domains[0]))
			} else {
				m.statusErr = "No TLS certificate path or domain available for " + app.Name
				return m, nil
			}
		}
	}

	if remoteCmd == "" {
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	ch := make(chan tea.Msg, 256)
	m.execCancel = cancel
	m.streamCh = ch
	m.liveOutput = nil
	m.liveKind = "ops"
	m.runningExec = true
	m.statusErr = ""
	m.statusMsg = fmt.Sprintf("Running %s on %s...", title, host.Name)
	startQuickOp(ctx, host, title, remoteCmd, m.sshOpts, ch)
	return m, waitStreamCmd(ch)
}

func (m Model) updateInventory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	hosts := m.filteredHosts()
	switch msg.String() {
	case "up":
		if m.invCursor > 0 {
			m.invCursor--
		}
	case "down", "j":
		if m.invCursor < len(hosts)-1 {
			m.invCursor++
		}
	case "enter":
		if m.invCursor >= 0 && m.invCursor < len(hosts) {
			m.hostIdx = m.invCursor
			m.activeTab = tabInspector
		}
	case "s":
		if m.invCursor >= 0 && m.invCursor < len(hosts) {
			m.hostIdx = m.invCursor
			return m.triggerScanSelected()
		}
	case "a":
		m.openAddHostModal()
		return m, textinput.Blink
	case "i":
		m.openImportInvModal()
		return m, textinput.Blink
	case "e":
		return m, openEditorCmd(m.cfg.InventoryPath)
	case "v":
		m.modal = modalRawInv
	case "d":
		if target := m.selectedInventoryHost(); target != nil {
			if inventory.RemoveHost(m.inv, target.Name) {
				if err := inventory.SaveINI(m.inv, m.cfg.InventoryPath); err != nil {
					m.statusErr = fmt.Sprintf("Save inventory failed: %v", err)
				} else {
					delete(m.reports, target.Name)
					m.reloadWorkspace()
					m.statusErr = ""
					m.statusMsg = fmt.Sprintf("Removed %s (%s) from inventory", target.Name, target.Address)
				}
			}
		}
	}
	return m, nil
}

func (m Model) updateAutomation(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	list := m.filteredAutomations()
	switch msg.String() {
	case "up":
		if m.autoCursor > 0 {
			m.autoCursor--
			m.autoSourceScroll = 0
		}
	case "down", "j":
		if m.autoCursor < len(list)-1 {
			m.autoCursor++
			m.autoSourceScroll = 0
		}
	case "pgup", "[":
		if m.autoSourceScroll > 0 {
			m.autoSourceScroll = maxInt(0, m.autoSourceScroll-4)
		}
	case "pgdown", "]":
		m.autoSourceScroll += 4
	case "f":
		switch m.autoFilter {
		case "all":
			m.autoFilter = "playbook"
		case "playbook":
			m.autoFilter = "script"
		default:
			m.autoFilter = "all"
		}
		m.autoCursor = 0
		m.autoSourceScroll = 0
	case "enter", "x":
		if item := m.selectedAutomation(); item != nil {
			m.openRunAutoModal(*item)
			return m, textinput.Blink
		}
	case "e":
		if item := m.selectedAutomation(); item != nil {
			return m, openEditorCmd(item.Path)
		}
	case "g":
		m.openGenerateModal()
		return m, textinput.Blink
	case "a":
		m.openAddAutoModal()
		return m, textinput.Blink
	case "d":
		if item := m.selectedAutomation(); item != nil {
			if err := os.Remove(item.Path); err != nil {
				m.statusErr = fmt.Sprintf("Delete failed: %v", err)
			} else {
				m.reloadWorkspace()
				m.statusErr = ""
				m.statusMsg = fmt.Sprintf("Deleted %s", item.FileName)
			}
		}
	}
	return m, nil
}

func (m Model) updateLogs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	logs := m.filteredLogs()
	switch msg.String() {
	case "up":
		if m.logCursor > 0 {
			m.logCursor--
			m.logScroll = 0
		}
	case "down", "j":
		if m.logCursor < len(logs)-1 {
			m.logCursor++
			m.logScroll = 0
		}
	case "pgup", "[":
		m.logScroll = maxInt(0, m.logScroll-5)
	case "pgdown", "]":
		m.logScroll += 5
	case "f":
		order := []string{"all", "failed", "playbook", "script", "ops", "ssh"}
		next := "all"
		for i, v := range order {
			if v == m.logFilter {
				next = order[(i+1)%len(order)]
				break
			}
		}
		m.logFilter = next
		m.logCursor = 0
		m.logScroll = 0
	}
	return m, nil
}

// ==================== Modal Builders & Handlers ====================

func (m *Model) focusModalField(idx int) {
	if len(m.inputs) == 0 {
		return
	}
	if idx < 0 {
		idx = len(m.inputs) - 1
	}
	if idx >= len(m.inputs) {
		idx = 0
	}
	m.focusIdx = idx
	for i := range m.inputs {
		if i == idx {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
}

func (m *Model) openSshConfigModal() {
	portVal := ""
	if m.sshOpts.Port > 0 {
		portVal = strconv.Itoa(m.sshOpts.Port)
	}
	defUser := inventory.DefaultSSHUser()
	m.inputs = []textinput.Model{
		makeInput(fmt.Sprintf("type SSH user (blank = %s)", defUser), m.sshOpts.User, 44, false),
		makeInput("e.g. 22 (blank = inventory default)", portVal, 44, false),
		makeInput("e.g. ~/.ssh/id_ed25519 (blank = agent/~/.ssh)", m.sshOpts.PrivateKey, 44, false),
		makeInput("optional session password (memory-only)", m.sshOpts.Password, 44, true),
		makeInput("optional bastion e.g. user@jump:22 (ProxyJump)", m.sshOpts.JumpHost, 44, false),
	}
	m.modal = modalSshConfig
	m.focusModalField(0)
}

func (m *Model) openAddHostModal() {
	defUser := inventory.DefaultSSHUser()
	m.inputs = []textinput.Model{
		makeInput("prod-app-02", "", 44, false),
		makeInput("10.0.1.20 or domain", "", 44, false),
		makeInput(defUser, defUser, 44, false),
		makeInput("22", "22", 44, false),
		makeInput("production,webservers", "production,webservers", 44, false),
		makeInput("~/.ssh/id_ed25519 (optional)", "", 44, false),
	}
	m.modal = modalAddHost
	m.focusModalField(0)
}

func (m *Model) openImportInvModal() {
	defaultPath := filepath.Join(m.cfg.WorkspaceDir, "inventory", "hosts.yml")
	m.inputs = []textinput.Model{
		makeInput("/path/to/hosts.ini or hosts.yml", defaultPath, 54, false),
	}
	m.modal = modalImportInv
	m.focusModalField(0)
}

func (m *Model) openRunAutoModal(item models.AutomationItem) {
	defaultTarget := "all"
	if h := m.selectedHost(); h != nil {
		defaultTarget = h.Name
	}
	var pairs []string
	for k, v := range item.DefaultVars {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
	}
	m.runCheckMode = false
	m.runDiffMode = false
	m.runBecome = false
	m.runVerbosity = 0
	m.runVenvIdx = -1

	if item.Kind == "playbook" {
		targetHosts := inventory.ResolveTargets(m.inv, defaultTarget)
		defaultVenv := item.Venv
		if defaultVenv == "" {
			defaultVenv = m.wsCfg.MatchPlaybookVenv(item.Path)
		}
		if defaultVenv == "" {
			defaultVenv = m.wsCfg.MatchServerVenv(targetHosts, defaultTarget)
		}
		if defaultVenv == "" {
			defaultVenv = m.wsCfg.AnsibleVenv
		}
		if defaultVenv == "" && len(m.venvs) > 0 {
			defaultVenv = m.venvs[0].Path
			m.runVenvIdx = 0
		}
		m.inputs = []textinput.Model{
			makeInput("all, group name, host alias, or IP", defaultTarget, 50, false),
			makeInput("key=value, key2=value2", strings.Join(pairs, ", "), 50, false),
			makeInput("blank=system PATH, or /path/to/venv (Ctrl+V cycle)", defaultVenv, 50, false),
			makeInput("optional --tags (e.g. deploy,nginx)", "", 50, false),
		}
	} else {
		m.inputs = []textinput.Model{
			makeInput("all, group name, host alias, or IP", defaultTarget, 50, false),
			makeInput("KEY=VAL, KEY2=VAL2", strings.Join(pairs, ", "), 50, false),
		}
	}
	m.modal = modalRunAuto
	m.focusModalField(0)
}

func (m *Model) openGenerateModal() {
	m.genKind = "playbook"
	m.genTemplateIdx = 0
	m.genBecome = true
	m.inputs = []textinput.Model{
		makeInput("Display name", "Nginx Reverse Proxy", 40, false),
		makeInput("Filename (e.g. nginx-proxy.yml)", "nginx-proxy.yml", 40, false),
		makeInput("Target hosts/group (playbooks)", "webservers", 40, false),
		makeInput("Param 1 (domain / service / path / url / packages / cmd)", "app.example.com", 40, false),
		makeInput("Param 2 (upstream for nginx-proxy, optional)", "127.0.0.1:8080", 40, false),
	}
	m.modal = modalGenerate
	m.focusModalField(0)
}

func (m *Model) openAddAutoModal() {
	m.genKind = "playbook"
	m.inputs = []textinput.Model{
		makeInput("Existing file path OR new filename (e.g. my-task.yml)", "", 54, false),
		makeInput("Shell command if creating new file inline (optional)", "uptime && df -h /", 54, false),
	}
	m.modal = modalAddAuto
	m.focusModalField(0)
}

func (m Model) updateModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "esc" {
		wasLaunchSsh := m.modal == modalSshConfig && m.autoScanPending
		m.modal = modalNone
		if wasLaunchSsh {
			m.autoScanPending = false
			nextM, scanCmd := m.triggerScanAll()
			if m.autoRefresh {
				return nextM, tea.Batch(scanCmd, m.tickAutoRefresh())
			}
			return nextM, scanCmd
		}
		return m, nil
	}
	if m.modal == modalOutput {
		switch key {
		case "up", "k":
			if m.outputScroll > 0 {
				m.outputScroll--
			}
		case "down", "j":
			m.outputScroll++
		case "pgup":
			m.outputScroll = maxInt(0, m.outputScroll-10)
		case "pgdown":
			m.outputScroll += 10
		case "enter", "q":
			m.modal = modalNone
		}
		return m, nil
	}
	if m.modal == modalHelp || m.modal == modalRawInv {
		if key == "enter" || key == "q" || key == "?" {
			m.modal = modalNone
		}
		return m, nil
	}

	switch key {
	case "tab", "down":
		m.focusModalField(m.focusIdx + 1)
		return m, nil
	case "shift+tab", "up":
		m.focusModalField(m.focusIdx - 1)
		return m, nil
	case "ctrl+k":
		if m.modal == modalGenerate || m.modal == modalAddAuto {
			if m.genKind == "playbook" {
				m.genKind = "script"
			} else {
				m.genKind = "playbook"
			}
			m.genTemplateIdx = 0
			m.syncGeneratorDefaults()
			return m, nil
		}
	case "ctrl+t":
		if m.modal == modalGenerate {
			tpls := m.currentGenTemplates()
			m.genTemplateIdx = (m.genTemplateIdx + 1) % len(tpls)
			m.syncGeneratorDefaults()
			return m, nil
		}
	case "ctrl+b":
		if m.modal == modalGenerate {
			m.genBecome = !m.genBecome
			return m, nil
		}
		if m.modal == modalRunAuto {
			m.runCheckMode = !m.runCheckMode
			return m, nil
		}
	case "ctrl+d":
		if m.modal == modalRunAuto {
			m.runDiffMode = !m.runDiffMode
			return m, nil
		}
	case "ctrl+u":
		if m.modal == modalRunAuto {
			m.runBecome = !m.runBecome
			return m, nil
		}
	case "ctrl+e":
		if m.modal == modalRunAuto {
			m.runVerbosity = (m.runVerbosity + 1) % 4
			return m, nil
		}
	case "ctrl+v":
		if m.modal == modalRunAuto && len(m.inputs) >= 3 {
			if len(m.venvs) > 0 {
				m.runVenvIdx++
				if m.runVenvIdx >= len(m.venvs) {
					m.runVenvIdx = -1
					m.inputs[2].SetValue("")
				} else {
					m.inputs[2].SetValue(m.venvs[m.runVenvIdx].Path)
				}
			}
			return m, nil
		}
	case "ctrl+s":
		if m.modal == modalSshConfig {
			m.applySshModalInputs()
			m.sshPool.CloseAll()
			wasLaunch := m.autoScanPending
			m.autoScanPending = false
			m.modal = modalNone
			nextM, scanCmd := m.triggerScanAll()
			if wasLaunch && m.autoRefresh {
				return nextM, tea.Batch(scanCmd, m.tickAutoRefresh())
			}
			return nextM, scanCmd
		}
	case "enter":
		return m.submitModal()
	}

	if m.focusIdx >= 0 && m.focusIdx < len(m.inputs) {
		var cmd tea.Cmd
		m.inputs[m.focusIdx], cmd = m.inputs[m.focusIdx].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) applySshModalInputs() {
	if len(m.inputs) >= 4 {
		m.sshOpts.User = strings.TrimSpace(m.inputs[0].Value())
		m.sshOpts.Port, _ = strconv.Atoi(strings.TrimSpace(m.inputs[1].Value()))
		m.sshOpts.PrivateKey = strings.TrimSpace(m.inputs[2].Value())
		m.sshOpts.Password = m.inputs[3].Value()
	}
	if len(m.inputs) >= 5 {
		m.sshOpts.JumpHost = strings.TrimSpace(m.inputs[4].Value())
	}
}

func (m Model) currentGenTemplates() []string {
	if m.genKind == "script" {
		return runner.ScriptTemplates()
	}
	return runner.PlaybookTemplates()
}

func (m *Model) syncGeneratorDefaults() {
	if len(m.inputs) < 5 {
		return
	}
	tpls := m.currentGenTemplates()
	tpl := tpls[m.genTemplateIdx%len(tpls)]
	ext := ".yml"
	if m.genKind == "script" {
		ext = ".sh"
	}
	m.inputs[0].SetValue(strings.ReplaceAll(strings.Title(strings.ReplaceAll(tpl, "-", " ")), "  ", " "))
	m.inputs[1].SetValue(tpl + ext)
	switch tpl {
	case "nginx-proxy":
		m.inputs[3].SetValue("app.example.com")
		m.inputs[4].SetValue("127.0.0.1:8080")
	case "systemd-service", "docker-deploy", "log-tail", "docker-restart":
		m.inputs[3].SetValue("nginx")
		m.inputs[4].SetValue("")
	case "package-install":
		m.inputs[3].SetValue("htop,curl,jq")
		m.inputs[4].SetValue("")
	case "cache-clear", "backup-dir":
		m.inputs[3].SetValue("/var/cache/nginx")
		m.inputs[4].SetValue("")
	case "http-probe":
		m.inputs[3].SetValue("http://127.0.0.1:80/")
		m.inputs[4].SetValue("")
	default:
		m.inputs[3].SetValue("uname -a && uptime")
		m.inputs[4].SetValue("")
	}
}

func (m Model) previewGeneratedContent() string {
	if len(m.inputs) < 5 {
		return ""
	}
	tpls := m.currentGenTemplates()
	tpl := tpls[m.genTemplateIdx%len(tpls)]
	name := m.inputs[0].Value()
	hosts := m.inputs[2].Value()
	p1 := m.inputs[3].Value()
	p2 := m.inputs[4].Value()

	if m.genKind == "playbook" {
		return runner.GeneratePlaybookYAML(runner.GeneratePlaybookOptions{
			Template:    tpl,
			Name:        name,
			Hosts:       hosts,
			Become:      m.genBecome,
			Domain:      p1,
			Upstream:    p2,
			ServiceName: p1,
			Packages:    strings.Split(p1, ","),
			CustomCmd:   p1,
			Venv:        m.wsCfg.AnsibleVenv,
		})
	}
	return runner.GenerateScriptContent(runner.GenerateScriptOptions{
		Template:    tpl,
		Name:        name,
		ServiceName: p1,
		TargetPath:  p1,
		URL:         p1,
		CustomCmd:   p1,
	})
}

func (m Model) submitModal() (tea.Model, tea.Cmd) {
	switch m.modal {
	case modalSshConfig:
		m.applySshModalInputs()
		m.sshPool.CloseAll()
		wasLaunch := m.autoScanPending
		m.autoScanPending = false
		m.modal = modalNone
		m.statusErr = ""
		if wasLaunch {
			nextM, scanCmd := m.triggerScanAll()
			if m.autoRefresh {
				return nextM, tea.Batch(scanCmd, m.tickAutoRefresh())
			}
			return nextM, scanCmd
		}
		m.statusMsg = "Updated session SSH user/credentials"
		return m, nil

	case modalAddHost:
		alias := strings.TrimSpace(m.inputs[0].Value())
		addr := strings.TrimSpace(m.inputs[1].Value())
		if alias == "" {
			alias = addr
		}
		if addr == "" {
			addr = alias
		}
		if alias == "" {
			m.statusErr = "Host alias or IP address is required"
			return m, nil
		}
		user := strings.TrimSpace(m.inputs[2].Value())
		if user == "" {
			user = inventory.DefaultSSHUser()
		}
		port, err := strconv.Atoi(strings.TrimSpace(m.inputs[3].Value()))
		if err != nil || port <= 0 {
			port = 22
		}
		var groups []string
		for _, g := range strings.Split(m.inputs[4].Value(), ",") {
			if t := strings.TrimSpace(g); t != "" {
				groups = append(groups, t)
			}
		}
		if len(groups) == 0 {
			groups = []string{"production"}
		}
		inventory.AddHost(m.inv, &models.Host{
			Name:       alias,
			Address:    addr,
			User:       user,
			Port:       port,
			PrivateKey: strings.TrimSpace(m.inputs[5].Value()),
			Groups:     groups,
		})
		if err := inventory.SaveINI(m.inv, m.cfg.InventoryPath); err != nil {
			m.statusErr = fmt.Sprintf("Save inventory failed: %v", err)
		} else {
			m.reloadWorkspace()
			m.statusErr = ""
			m.statusMsg = fmt.Sprintf("Enrolled %s (%s) into %s", alias, addr, m.cfg.InventoryPath)
		}
		m.modal = modalNone
		return m, nil

	case modalImportInv:
		src := inventory.ExpandHome(strings.TrimSpace(m.inputs[0].Value()))
		incoming, err := inventory.Load(src)
		if err != nil {
			m.statusErr = fmt.Sprintf("Import failed: %v", err)
			return m, nil
		}
		added := inventory.Merge(m.inv, incoming)
		if err := inventory.SaveINI(m.inv, m.cfg.InventoryPath); err != nil {
			m.statusErr = fmt.Sprintf("Save failed: %v", err)
			return m, nil
		}
		m.reloadWorkspace()
		m.modal = modalNone
		m.statusErr = ""
		m.statusMsg = fmt.Sprintf("Imported %d new host(s) from %s", added, src)
		return m, nil

	case modalRunAuto:
		item := m.selectedAutomation()
		if item == nil {
			m.modal = modalNone
			return m, nil
		}
		target := strings.TrimSpace(m.inputs[0].Value())
		if target == "" {
			target = "all"
		}
		vars := parseVarsString(m.inputs[1].Value())
		targetHosts := inventory.ResolveTargets(m.inv, target)
		remoteVenv := m.wsCfg.MatchServerRemoteVenv(targetHosts, target)
		if remoteVenv == "" {
			remoteVenv = m.wsCfg.RemoteVenv
		}
		pbOpts := runner.PlaybookOptions{
			PlaybookConfigVenv: m.wsCfg.MatchPlaybookVenv(item.Path),
			ServerConfigVenv:   m.wsCfg.MatchServerVenv(targetHosts, target),
			ConfigVenv:         m.wsCfg.AnsibleVenv,
			WorkspaceDir:       m.cfg.WorkspaceDir,
			RemoteVenv:         remoteVenv,
			CheckMode:          m.runCheckMode,
			DiffMode:           m.runDiffMode,
			Become:             m.runBecome,
			Verbosity:          m.runVerbosity,
		}
		if item.Kind == "playbook" && len(m.inputs) >= 4 {
			pbOpts.Venv = strings.TrimSpace(m.inputs[2].Value())
			pbOpts.Tags = strings.TrimSpace(m.inputs[3].Value())
		}

		ctx, cancel := m.newExecContext()
		ch := make(chan tea.Msg, 256)
		m.execCancel = cancel
		m.streamCh = ch
		m.liveOutput = nil
		m.liveKind = item.Kind
		m.modal = modalNone
		m.runningExec = true
		m.statusErr = ""
		m.statusMsg = fmt.Sprintf("Running %s on %s... (Ctrl+X to cancel)", item.FileName, target)
		startAutomationStreaming(ctx, m.inv, m.cfg.InventoryPath, *item, target, vars, pbOpts, m.sshOpts, ch)
		return m, waitStreamCmd(ch)

	case modalGenerate:
		fileName := strings.TrimSpace(m.inputs[1].Value())
		if fileName == "" {
			fileName = "generated"
		}
		dir := filepath.Join(m.cfg.WorkspaceDir, "playbooks")
		if m.genKind == "script" {
			dir = filepath.Join(m.cfg.WorkspaceDir, "scripts")
			if !strings.HasSuffix(fileName, ".sh") {
				fileName += ".sh"
			}
		} else if !strings.HasSuffix(fileName, ".yml") && !strings.HasSuffix(fileName, ".yaml") {
			fileName += ".yml"
		}
		content := m.previewGeneratedContent()
		dest, err := runner.SaveGenerated(dir, fileName, content)
		if err != nil {
			m.statusErr = fmt.Sprintf("Save generated file failed: %v", err)
			return m, nil
		}
		m.reloadWorkspace()
		m.modal = modalNone
		m.statusErr = ""
		m.statusMsg = fmt.Sprintf("Generated and saved %s", dest)
		return m, nil

	case modalAddAuto:
		srcOrName := inventory.ExpandHome(strings.TrimSpace(m.inputs[0].Value()))
		if srcOrName == "" {
			m.statusErr = "Provide an existing file path or a new filename"
			return m, nil
		}
		dir := filepath.Join(m.cfg.WorkspaceDir, "playbooks")
		if m.genKind == "script" || strings.HasSuffix(srcOrName, ".sh") {
			dir = filepath.Join(m.cfg.WorkspaceDir, "scripts")
		}
		if st, err := os.Stat(srcOrName); err == nil && !st.IsDir() {
			dest, err := runner.AddFile(srcOrName, dir)
			if err != nil {
				m.statusErr = fmt.Sprintf("Import failed: %v", err)
				return m, nil
			}
			m.reloadWorkspace()
			m.modal = modalNone
			m.statusErr = ""
			m.statusMsg = fmt.Sprintf("Imported %s", dest)
			return m, nil
		}
		cmdText := strings.TrimSpace(m.inputs[1].Value())
		var content string
		if strings.HasSuffix(srcOrName, ".sh") || m.genKind == "script" {
			if !strings.HasSuffix(srcOrName, ".sh") {
				srcOrName += ".sh"
			}
			content = runner.GenerateScriptContent(runner.GenerateScriptOptions{
				Template:  "custom",
				Name:      strings.TrimSuffix(filepath.Base(srcOrName), ".sh"),
				CustomCmd: cmdText,
			})
		} else {
			if !strings.HasSuffix(srcOrName, ".yml") && !strings.HasSuffix(srcOrName, ".yaml") {
				srcOrName += ".yml"
			}
			content = runner.GeneratePlaybookYAML(runner.GeneratePlaybookOptions{
				Template:  "custom",
				Name:      strings.TrimSuffix(filepath.Base(srcOrName), filepath.Ext(srcOrName)),
				Hosts:     "all",
				Become:    true,
				CustomCmd: cmdText,
			})
		}
		dest, err := runner.SaveGenerated(dir, srcOrName, content)
		if err != nil {
			m.statusErr = fmt.Sprintf("Create failed: %v", err)
			return m, nil
		}
		m.reloadWorkspace()
		m.modal = modalNone
		m.statusErr = ""
		m.statusMsg = fmt.Sprintf("Created %s", dest)
		return m, nil
	}

	m.modal = modalNone
	return m, nil
}

func appendSample(list []float64, val float64, maxLen int) []float64 {
	list = append(list, val)
	if len(list) > maxLen {
		list = list[len(list)-maxLen:]
	}
	return list
}
