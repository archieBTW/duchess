package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/duchess-devops/duchess-cli/internal/models"
)

func (m Model) View() string {
	if m.width < 60 || m.height < 18 {
		return "Terminal too small — please resize to at least 60x18 (press q to quit)."
	}

	header := m.viewHeader()
	nav := m.viewNavBar()
	footer := m.viewFooter()

	usedH := lipgloss.Height(header) + lipgloss.Height(nav) + lipgloss.Height(footer)
	bodyH := maxInt(8, m.height-usedH)

	var body string
	switch m.activeTab {
	case tabOverview:
		body = m.viewOverview(bodyH)
	case tabInspector:
		body = m.viewInspector(bodyH)
	case tabInventory:
		body = m.viewInventory(bodyH)
	case tabAutomation:
		body = m.viewAutomation(bodyH)
	case tabLogs:
		body = m.viewLogs(bodyH)
	}

	body = lipgloss.NewStyle().
		Width(m.width).
		Height(bodyH).
		MaxHeight(bodyH).
		Render(body)

	base := lipgloss.JoinVertical(lipgloss.Left, header, nav, body, footer)

	if m.modal != modalNone {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.viewModal())
	}
	return base
}

func (m Model) boxWidth() int {
	return maxInt(58, m.width-2)
}

func (m Model) innerWidth() int {
	// boxWidth() includes padding(0,1) = 2 chars; border adds 2 chars outside Width()
	return maxInt(54, m.width-4)
}

func (m Model) viewHeader() string {
	logo := lipgloss.NewStyle().Bold(true).Foreground(colorCyan).Render(
		"╔╦╗╦ ╦╔═╗╦ ╦╔═╗╔═╗╔═╗\n ║║║ ║║  ╠═╣║╣ ╚═╗╚═╗\n═╩╝╚═╝╚═╝╩ ╩╚═╝╚═╝╚═╝",
	)

	sshBadge := badge("SSH User: "+m.effectiveUser(m.selectedHost()), colorCyan)
	refreshBadge := badge("AUTO-REFRESH: OFF", colorMuted)
	if m.autoRefresh {
		refreshBadge = badge(fmt.Sprintf("LIVE %ds", m.wsCfg.PollIntervalSeconds), colorEmerald)
	}
	alertTotal, alertCrit := m.totalAlerts()
	alertBadge := ""
	if alertTotal > 0 {
		col := colorAmber
		if alertCrit > 0 {
			col = colorRose
		}
		alertBadge = " " + badge(fmt.Sprintf("! %d ALERT", alertTotal), col)
	}
	spinStr := ""
	if m.anyScanning() || m.runningExec {
		spinStr = " " + m.spin.View()
	}

	rightMax := maxInt(28, m.innerWidth()-25)
	right := lipgloss.JoinVertical(
		lipgloss.Left,
		clampLine(styleTitle.Render("DevOps Control Plane")+spinStr+alertBadge, rightMax),
		clampLine(styleSubtitle.Render("Inventory: "+m.cfg.InventoryPath), rightMax),
		clampLine(sshBadge+"  "+refreshBadge+"  "+styleMuted.Render(fmt.Sprintf("Hosts:%d  PB/SH:%d  Venvs:%d", len(m.hosts), len(m.automations), len(m.venvs))), rightMax),
	)

	content := lipgloss.JoinHorizontal(lipgloss.Center, logo, "   ", right)
	return styleBox.Width(m.boxWidth()).Render(content)
}

func (m Model) viewNavBar() string {
	tabs := []struct {
		id    mainTab
		label string
	}{
		{tabOverview, "1 Overview"},
		{tabInspector, "2 Server Inspector"},
		{tabInventory, "3 Ansible Inventory"},
		{tabAutomation, "4 Playbooks & Scripts"},
		{tabLogs, fmt.Sprintf("5 Exec Log (%d)", len(m.execLogs))},
	}
	var rendered []string
	for _, t := range tabs {
		if m.activeTab == t.id {
			rendered = append(rendered, styleTabActive.Render(t.label))
		} else {
			rendered = append(rendered, styleTabInactive.Render(t.label))
		}
	}
	return clampLine(strings.Join(rendered, " "), m.width)
}

func (m Model) viewFooter() string {
	status := styleSubtitle.Render(m.statusMsg)
	if m.searchActive {
		status = badge("SEARCH", colorCyan) + " " + m.searchInput.View() + "  " + styleMuted.Render("[Enter] Apply  [Esc] Clear")
	} else if m.statusErr != "" {
		status = lipgloss.NewStyle().Foreground(colorRose).Bold(true).Render("ERR: " + m.statusErr)
	} else if m.searchQuery != "" {
		status = badge("FILTER: "+m.searchQuery, colorAmber) + " " + status + "  " + styleMuted.Render("[Esc] Clear filter")
	}
	keys := styleMuted.Render("[1-5/Tab] Views  [/] Search  [!] SSH  [s/S] Scan  [A] Live  [k] Creds  [Ctrl+X] Cancel  [?] Help  [q] Quit")
	w := maxInt(40, m.width-2)
	return styleFooter.Width(m.width).Render(clampLine(status, w) + "\n" + clampLine(keys, w))
}

// ==================== [1] OVERVIEW ====================

func (m Model) viewOverview(bodyH int) string {
	online, containers, webApps, services := 0, 0, 0, 0
	for _, r := range m.reports {
		if r.Error == "" && r.Stats != nil {
			online++
			containers += len(r.Containers)
			webApps += len(r.WebApps)
			services += len(r.Services)
		}
	}
	pbCount, scCount := 0, 0
	for _, a := range m.automations {
		if a.Kind == "playbook" {
			pbCount++
		} else {
			scCount++
		}
	}
	alertTotal, alertCrit := m.totalAlerts()
	alertSub := fmt.Sprintf("%d active services", services)
	if alertTotal > 0 {
		alertSub = fmt.Sprintf("! %d alert (%d crit)", alertTotal, alertCrit)
	}

	kpiContentW := maxInt(12, (m.width-11)/4)
	k1 := styleBox.Width(kpiContentW).Render(
		clampLine(styleMuted.Render("ENROLLED SERVERS"), kpiContentW-2) + "\n" +
			clampLine(lipgloss.NewStyle().Bold(true).Foreground(colorCyan).Render(fmt.Sprintf("%d hosts", len(m.hosts))), kpiContentW-2) + "\n" +
			clampLine(styleSubtitle.Render(fmt.Sprintf("%d online • %d groups", online, len(m.inv.Groups))), kpiContentW-2),
	)
	k2 := styleBox.Width(kpiContentW).Render(
		clampLine(styleMuted.Render("CONTAINERS"), kpiContentW-2) + "\n" +
			clampLine(lipgloss.NewStyle().Bold(true).Foreground(colorEmerald).Render(fmt.Sprintf("%d detected", containers)), kpiContentW-2) + "\n" +
			clampLine(styleSubtitle.Render("Docker & Podman"), kpiContentW-2),
	)
	k3 := styleBox.Width(kpiContentW).Render(
		clampLine(styleMuted.Render("HOSTED WEB APPS"), kpiContentW-2) + "\n" +
			clampLine(lipgloss.NewStyle().Bold(true).Foreground(colorViolet).Render(fmt.Sprintf("%d sites/apps", webApps)), kpiContentW-2) + "\n" +
			clampLine(styleSubtitle.Render("Nginx/Apache/Tomcat"), kpiContentW-2),
	)
	k4Color := colorAmber
	if alertCrit > 0 {
		k4Color = colorRose
	}
	k4 := styleBox.Width(kpiContentW).Render(
		clampLine(styleMuted.Render("AUTOMATION & HEALTH"), kpiContentW-2) + "\n" +
			clampLine(lipgloss.NewStyle().Bold(true).Foreground(k4Color).Render(fmt.Sprintf("%d PB / %d SH", pbCount, scCount)), kpiContentW-2) + "\n" +
			clampLine(styleSubtitle.Render(alertSub), kpiContentW-2),
	)
	kpiRow := lipgloss.JoinHorizontal(lipgloss.Top, k1, " ", k2, " ", k3, " ", k4)

	kpiH := lipgloss.Height(kpiRow)
	logBoxH := minInt(7, maxInt(4, (bodyH-kpiH)/3))
	fleetBoxH := maxInt(5, bodyH-kpiH-logBoxH)
	innerW := m.innerWidth()

	hosts := m.filteredHosts()
	var fleetLines []string
	fleetLines = append(fleetLines, clampLine(styleTitle.Render("ENROLLED FLEET (↑/↓ select • Enter inspect • ! SSH • a alerts • / search • s/S scan)"), innerW))
	fleetLines = append(fleetLines, styleMuted.Render(strings.Repeat("─", innerW)))

	if len(hosts) == 0 {
		msg := "No hosts enrolled. Press [3] to enroll servers via Ansible inventory."
		if m.searchQuery != "" {
			msg = fmt.Sprintf("No hosts match filter %q (press Esc to clear).", m.searchQuery)
		}
		fleetLines = append(fleetLines, clampLine(styleMuted.Render(msg), innerW))
	} else {
		for i, h := range hosts {
			prefix := "  "
			if i == m.hostIdx {
				prefix = "▶ "
			}
			rep := m.reports[h.Name]
			statusStr := badge("NOT SCANNED", colorMuted)
			metricsStr := styleMuted.Render("press [s] to collect live SSH stats & auto-detect")
			alertTag := ""
			if m.scanning[h.Name] {
				statusStr = badge("SCANNING...", colorCyan)
				metricsStr = styleSubtitle.Render("running SSH telemetry & discovery probe...")
			} else if rep != nil && rep.Error != "" {
				statusStr = badge("OFFLINE", colorRose)
				metricsStr = lipgloss.NewStyle().Foreground(colorRose).Render(rep.Error)
			} else if rep != nil && rep.Stats != nil {
				statusStr = badge("ONLINE", colorEmerald)
				if len(rep.Alerts) > 0 {
					aCol := colorAmber
					for _, a := range rep.Alerts {
						if a.Severity == "critical" {
							aCol = colorRose
							break
						}
					}
					alertTag = " " + badge(fmt.Sprintf("!%d", len(rep.Alerts)), aCol)
				}
				metricsStr = fmt.Sprintf(
					"CPU %s  RAM %s  C:%d  W:%d  S:%d  up %s",
					asciiGauge(rep.Stats.CPUUsagePercent, 10),
					asciiGauge(rep.Stats.MemUsagePercent, 10),
					len(rep.Containers),
					len(rep.WebApps),
					len(rep.Services),
					rep.Stats.UptimeHuman,
				)
			}

			line1 := fmt.Sprintf("%s%s %s %s%s %s",
				prefix,
				padCell(h.Name, 14),
				padCell(m.effectiveUser(h)+"@"+h.Address, 24),
				statusStr,
				alertTag,
				styleMuted.Render("["+strings.Join(h.Groups, ",")+"]"),
			)
			line1 = clampLine(line1, innerW)
			line2 := clampLine("    "+metricsStr, innerW)
			if i == m.hostIdx {
				line1 = styleSelectedRow.Width(innerW).Render(line1)
			}
			fleetLines = append(fleetLines, line1, line2)
		}
	}
	fleetInnerH := maxInt(3, fleetBoxH-2)
	fleetScroll := maxInt(0, m.hostIdx*2-(fleetInnerH-4))
	fleetContent := sliceVisibleLines(fleetLines, 0, minInt(2, fleetInnerH))
	if len(fleetLines) > 2 && fleetInnerH > 2 {
		fleetContent += "\n" + sliceVisibleLines(fleetLines[2:], fleetScroll, fleetInnerH-2)
	}
	fleetBox := styleBox.Width(m.boxWidth()).Height(fleetInnerH).MaxHeight(fleetBoxH).Render(fleetContent)

	var logLines []string
	logLines = append(logLines, clampLine(styleTitle.Render("RECENT EXECUTIONS (persisted history)"), innerW))
	if len(m.execLogs) == 0 {
		logLines = append(logLines, clampLine(styleMuted.Render("No playbooks, scripts, or SSH commands run yet."), innerW))
	} else {
		for _, e := range m.execLogs {
			st := badge("OK", colorEmerald)
			if !e.Succeeded() {
				st = badge(fmt.Sprintf("EXIT %d", e.ExitCode), colorRose)
			}
			logLines = append(logLines, clampLine(fmt.Sprintf("  %s  %s %s → %s (%s, %dms)",
				st, padCell(strings.ToUpper(e.Kind), 8), padCell(e.Title, 24), padCell(e.Target, 15), e.Engine, e.DurationMs), innerW))
		}
	}
	logInnerH := maxInt(2, logBoxH-2)
	logBox := styleBox.Width(m.boxWidth()).Height(logInnerH).MaxHeight(logBoxH).Render(sliceVisibleLines(logLines, 0, logInnerH))

	return lipgloss.JoinVertical(lipgloss.Left, kpiRow, fleetBox, logBox)
}

// ==================== [2] SERVER INSPECTOR ====================

func (m Model) viewInspector(bodyH int) string {
	innerW := m.innerWidth()
	host := m.selectedHost()
	if host == nil {
		return styleBox.Width(m.boxWidth()).Height(maxInt(4, bodyH-2)).Render("No servers enrolled. Switch to [3] Ansible Inventory to add one.")
	}
	rep := m.reports[host.Name]

	// Fixed-height top stats panel (always 9 lines total including borders, so switching tabs NEVER shifts layout)
	statsTotalH := minInt(9, maxInt(6, bodyH/2))
	statsInnerH := maxInt(4, statsTotalH-2)

	alertBadge := ""
	if rep != nil && len(rep.Alerts) > 0 {
		aCol := colorAmber
		for _, a := range rep.Alerts {
			if a.Severity == "critical" {
				aCol = colorRose
				break
			}
		}
		alertBadge = " " + badge(fmt.Sprintf("! %d alerts [a]", len(rep.Alerts)), aCol)
	}

	topBar := clampLine(fmt.Sprintf("%s %s (%s@%s:%d) %s%s %s",
		styleTitle.Render("SERVER:"),
		lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(host.Name),
		m.effectiveUser(host), host.Address, m.effectivePort(host),
		badge(strings.Join(host.Groups, ","), colorCyan),
		alertBadge,
		styleMuted.Render("[/]host !SSH s:scan"),
	), innerW)

	sep := styleMuted.Render(strings.Repeat("─", innerW))

	var statsBody string
	if m.scanning[host.Name] {
		statsBody = clampLine(m.spin.View()+" Connecting via SSH and probing stats, containers, services, TLS certs, and web apps...", innerW)
	} else if rep == nil {
		statsBody = clampLine(styleSubtitle.Render("Not scanned yet. Press [s] to collect live SSH stats & auto-detect workloads (press [k] for SSH user/key)."), innerW)
	} else if rep.Error != "" {
		statsBody = clampLine(badge("UNREACHABLE", colorRose)+" "+lipgloss.NewStyle().Foreground(colorRose).Render(rep.Error), innerW)
	} else {
		st := rep.Stats
		leftW := maxInt(28, (innerW-5)/2)
		rightW := maxInt(24, innerW-leftW-5)

		cpuSpark := asciiSparkline(m.cpuHistory[host.Name], 10)
		memSpark := asciiSparkline(m.memHistory[host.Name], 10)
		gaugeW := maxInt(8, minInt(14, leftW-32))

		leftLines := []string{
			clampLine(fmt.Sprintf("OS:   %s (%s %s)", st.OS, st.Kernel, st.Arch), leftW),
			clampLine(fmt.Sprintf("FQDN: %s  Up: %s", st.HostnameFQDN, st.UptimeHuman), leftW),
			clampLine(fmt.Sprintf("Load: %.2f / %.2f / %.2f (%d vCPU)", st.Load1, st.Load5, st.Load15, st.CPUCores), leftW),
			clampLine(fmt.Sprintf("CPU:  %s %s", asciiGauge(st.CPUUsagePercent, gaugeW), cpuSpark), leftW),
			clampLine(fmt.Sprintf("RAM:  %s %s %s/%s", asciiGauge(st.MemUsagePercent, gaugeW), memSpark, formatBytes(st.MemUsedBytes), formatBytes(st.MemTotalBytes)), leftW),
		}

		var rightLines []string
		rightLines = append(rightLines, clampLine(styleAccent.Render("DISKS & NETWORK"), rightW))
		diskGaugeW := maxInt(6, minInt(10, rightW-30))
		for _, d := range st.Disks[:minInt(len(st.Disks), 2)] {
			rightLines = append(rightLines, clampLine(fmt.Sprintf("%-6s %s %s/%s",
				truncate(d.MountPoint, 6), asciiGauge(d.UsePercent, diskGaugeW), formatBytes(d.UsedBytes), formatBytes(d.TotalBytes)), rightW))
		}
		for _, iface := range st.Interfaces[:minInt(len(st.Interfaces), 2)] {
			rightLines = append(rightLines, clampLine(fmt.Sprintf("NET %-5s %-15s R:%s T:%s",
				truncate(iface.Name, 5), iface.IPv4, formatBytes(iface.RxBytes), formatBytes(iface.TxBytes)), rightW))
		}
		for len(rightLines) < len(leftLines) {
			rightLines = append(rightLines, "")
		}

		leftCol := lipgloss.NewStyle().Width(leftW).Render(strings.Join(leftLines[:minInt(len(leftLines), statsInnerH-2)], "\n"))
		rightCol := lipgloss.NewStyle().Width(rightW).Render(strings.Join(rightLines[:minInt(len(rightLines), statsInnerH-2)], "\n"))
		statsBody = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  │  ", rightCol)
	}

	statsPanel := styleBox.
		Width(m.boxWidth()).
		Height(statsInnerH).
		MaxHeight(statsTotalH).
		Render(topBar + "\n" + sep + "\n" + statsBody)

	// Fixed-height detail box consuming the EXACT remaining height
	detailTotalH := maxInt(7, bodyH-lipgloss.Height(statsPanel))
	detailInnerH := maxInt(5, detailTotalH-2)
	subContentH := maxInt(3, detailInnerH-2)

	cCount, wCount, sCount := 0, 0, 0
	if rep != nil {
		cCount, wCount, sCount = len(rep.Containers), len(rep.WebApps), len(rep.Services)
	}
	subTabs := []struct {
		id    inspectorSubTab
		label string
	}{
		{subContainers, fmt.Sprintf("Containers (%d)", cCount)},
		{subWebApps, fmt.Sprintf("Websites & Apps (%d)", wCount)},
		{subServices, fmt.Sprintf("Services & Ports (%d)", sCount)},
		{subAdHocSSH, "Ad-hoc SSH"},
	}
	var stRendered []string
	for _, st := range subTabs {
		if m.inspectorSub == st.id {
			stRendered = append(stRendered, styleSubTabActive.Render(st.label))
		} else {
			stRendered = append(stRendered, styleSubTabInactive.Render(st.label))
		}
	}
	actionHint := "(←/→ tab • ↑/↓ select)"
	switch m.inspectorSub {
	case subContainers:
		actionHint = "[l]logs [R]restart [x]stop/start [E]exec"
	case subWebApps:
		actionHint = "[v]config [t]test [c]TLS cert"
	case subServices:
		actionHint = "[l]journal [R]restart [o]reload"
	case subAdHocSSH:
		actionHint = "[i]type [Ctrl+G]scope"
	}
	subHeader := clampLine(strings.Join(stRendered, " ")+"  "+styleMuted.Render(actionHint), innerW)

	var subBody string
	switch m.inspectorSub {
	case subContainers:
		subBody = m.renderContainersList(rep, innerW, subContentH)
	case subWebApps:
		subBody = m.renderWebAppsList(rep, innerW, subContentH)
	case subServices:
		subBody = m.renderServicesList(rep, innerW, subContentH)
	case subAdHocSSH:
		subBody = m.renderAdHocConsole(host, innerW, subContentH)
	}

	detailBox := styleBox.
		Width(m.boxWidth()).
		Height(detailInnerH).
		MaxHeight(detailTotalH).
		Render(subHeader + "\n" + sep + "\n" + subBody)

	return lipgloss.JoinVertical(lipgloss.Left, statsPanel, detailBox)
}

func (m Model) renderContainersList(rep *models.DiscoveryReport, width, maxRows int) string {
	if rep == nil || len(rep.Containers) == 0 {
		return clampLine(styleMuted.Render("No Docker or Podman containers detected on this server."), width)
	}
	wRT, wState, wCPU := 7, 8, 7
	rem := maxInt(22, width-(2+wRT+wState+wCPU+6))
	wName := maxInt(10, rem*24/100)
	wImage := maxInt(12, rem*30/100)
	wMem := maxInt(10, rem*22/100)
	wPorts := maxInt(8, rem-wName-wImage-wMem)

	header := clampLine(styleMuted.Render("  "+strings.Join([]string{
		padCell("RUNTIME", wRT),
		padCell("NAME", wName),
		padCell("IMAGE", wImage),
		padCell("STATE", wState),
		padCell("CPU", wCPU),
		padCell("MEMORY", wMem),
		padCell("PORTS", wPorts),
	}, " ")), width)

	var rows []string
	for i, c := range rep.Containers {
		cur := "  "
		if i == m.inspectorRow {
			cur = "▶ "
		}
		stColor := colorAmber
		if strings.EqualFold(c.State, "running") {
			stColor = colorEmerald
		} else if strings.EqualFold(c.State, "dead") || strings.EqualFold(c.State, "restarting") {
			stColor = colorRose
		}
		row := cur + strings.Join([]string{
			padCell(strings.ToUpper(c.Runtime), wRT),
			padCell(c.Name, wName),
			padCell(c.Image, wImage),
			lipgloss.NewStyle().Foreground(stColor).Bold(true).Render(padCell(c.State, wState)),
			padCell(c.CPUPerc, wCPU),
			padCell(c.MemUsage, wMem),
			padCell(c.Ports, wPorts),
		}, " ")
		row = clampLine(row, width)
		if i == m.inspectorRow {
			row = clampLine(styleSelectedRow.Width(width).Render(row), width)
		}
		rows = append(rows, row)
	}
	dataH := maxInt(1, maxRows-1)
	scroll := adjustRowScroll(m.inspectorRow, m.inspectorScroll, dataH)
	return header + "\n" + sliceVisibleLines(rows, scroll, dataH)
}

func (m Model) renderWebAppsList(rep *models.DiscoveryReport, width, maxRows int) string {
	if rep == nil || len(rep.WebApps) == 0 {
		return clampLine(styleMuted.Render("No Nginx, Apache, Tomcat, Caddy, HAProxy, or HTTP apps detected."), width)
	}
	wSrv, wSSL, wPorts := 8, 8, 9
	rem := maxInt(22, width-(2+wSrv+wSSL+wPorts+5))
	wName := maxInt(12, rem*28/100)
	wDom := maxInt(12, rem*34/100)
	wTarget := maxInt(10, rem-wName-wDom)

	header := clampLine(styleMuted.Render("  "+strings.Join([]string{
		padCell("SERVER", wSrv),
		padCell("APP / SITE", wName),
		padCell("SSL/TLS", wSSL),
		padCell("PORTS", wPorts),
		padCell("DOMAINS / CONTEXT", wDom),
		padCell("PROXY / ROOT", wTarget),
	}, " ")), width)

	var rows []string
	for i, a := range rep.WebApps {
		cur := "  "
		if i == m.inspectorRow {
			cur = "▶ "
		}
		srvColor := colorViolet
		switch a.ServerType {
		case "nginx":
			srvColor = colorEmerald
		case "apache":
			srvColor = colorRose
		case "tomcat":
			srvColor = colorAmber
		case "caddy":
			srvColor = colorCyan
		}
		sslCell := padCell("no", wSSL)
		if a.CertExpiryDays != nil {
			col := colorEmerald
			if *a.CertExpiryDays <= 3 {
				col = colorRose
			} else if *a.CertExpiryDays <= m.wsCfg.Alerts.CertWarnDays {
				col = colorAmber
			}
			sslCell = lipgloss.NewStyle().Foreground(col).Bold(true).Render(padCell(fmt.Sprintf("%dd", *a.CertExpiryDays), wSSL))
		} else if a.SSL {
			sslCell = lipgloss.NewStyle().Foreground(colorEmerald).Bold(true).Render(padCell("HTTPS", wSSL))
		}
		target := a.ProxyTarget
		if target == "" {
			target = a.RootOrPath
		}
		row := cur + strings.Join([]string{
			lipgloss.NewStyle().Foreground(srvColor).Bold(true).Render(padCell(strings.ToUpper(a.ServerType), wSrv)),
			padCell(a.Name, wName),
			sslCell,
			padCell(strings.Join(a.Ports, ","), wPorts),
			padCell(strings.Join(a.Domains, ","), wDom),
			padCell(target, wTarget),
		}, " ")
		row = clampLine(row, width)
		if i == m.inspectorRow {
			row = clampLine(styleSelectedRow.Width(width).Render(row), width)
		}
		rows = append(rows, row)
	}
	dataH := maxInt(1, maxRows-1)
	scroll := adjustRowScroll(m.inspectorRow, m.inspectorScroll, dataH)
	return header + "\n" + sliceVisibleLines(rows, scroll, dataH)
}

func (m Model) renderServicesList(rep *models.DiscoveryReport, width, maxRows int) string {
	if rep == nil || len(rep.Services) == 0 {
		return clampLine(styleMuted.Render("No active or failed systemd services detected."), width)
	}
	wState, wPorts := 15, 11
	rem := maxInt(18, width-(2+wState+wPorts+3))
	wName := maxInt(14, minInt(28, rem*38/100))
	wDesc := maxInt(10, rem-wName)

	header := clampLine(styleMuted.Render("  "+strings.Join([]string{
		padCell("SERVICE", wName),
		padCell("STATE", wState),
		padCell("PORTS", wPorts),
		padCell("DESCRIPTION", wDesc),
	}, " ")), width)

	var rows []string
	for i, s := range rep.Services {
		cur := "  "
		if i == m.inspectorRow {
			cur = "▶ "
		}
		col := colorEmerald
		if s.ActiveState == "failed" {
			col = colorRose
		}
		ports := "-"
		if len(s.Ports) > 0 {
			ports = ":" + strings.Join(s.Ports, ",:")
		}
		row := cur + strings.Join([]string{
			padCell(s.Name, wName),
			lipgloss.NewStyle().Foreground(col).Render(padCell(s.ActiveState+"/"+s.SubState, wState)),
			padCell(ports, wPorts),
			padCell(s.Description, wDesc),
		}, " ")
		row = clampLine(row, width)
		if i == m.inspectorRow {
			row = clampLine(styleSelectedRow.Width(width).Render(row), width)
		}
		rows = append(rows, row)
	}
	dataH := maxInt(1, maxRows-1)
	scroll := adjustRowScroll(m.inspectorRow, m.inspectorScroll, dataH)
	return header + "\n" + sliceVisibleLines(rows, scroll, dataH)
}

func (m Model) renderAdHocConsole(host *models.Host, width, maxRows int) string {
	scopeLabel := fmt.Sprintf("HOST: %s", host.Name)
	scopeColor := colorCyan
	switch m.adHocScope {
	case "group":
		grp := "all"
		if len(host.Groups) > 0 {
			grp = host.Groups[0]
		}
		scopeLabel = "GROUP: " + grp
		scopeColor = colorViolet
	case "all":
		scopeLabel = fmt.Sprintf("ALL (%d hosts)", len(m.hosts))
		scopeColor = colorAmber
	}
	promptHint := badge(scopeLabel, scopeColor) + " " + styleMuted.Render("[i/Enter] type • [Ctrl+G] scope • [Ctrl+X] cancel")
	if m.adHocFocus {
		promptHint = badge("TYPING", colorCyan) + " " + badge(scopeLabel, scopeColor) + " [Enter] run • [Ctrl+G] scope • [Esc] unfocus"
	}
	promptLine := fmt.Sprintf("%s@%s$ %s", m.effectiveUser(host), host.Address, m.adHocInput.View())

	outH := maxInt(1, maxRows-4)
	var outLines []string
	if m.runningExec && m.liveKind == "ssh" {
		outLines = append(outLines, clampLine(m.spin.View()+" Streaming SSH output... [Ctrl+X cancel]", width))
		start := maxInt(0, len(m.liveOutput)-(outH-1))
		for _, ln := range m.liveOutput[start:] {
			outLines = append(outLines, clampLine(ln, width))
		}
	} else if m.lastAdHocLog == nil {
		outLines = []string{clampLine(styleMuted.Render("Output will appear here (supports single host, group, or fleet fan-out)."), width)}
	} else {
		e := m.lastAdHocLog
		head := badge(fmt.Sprintf("EXIT %d • %s • %dms", e.ExitCode, e.Target, e.DurationMs), colorEmerald)
		if !e.Succeeded() {
			head = badge(fmt.Sprintf("EXIT %d • %s • %dms", e.ExitCode, e.Target, e.DurationMs), colorRose)
		}
		outLines = append(outLines, clampLine(head, width))
		raw := strings.TrimSpace(e.Stdout)
		if strings.TrimSpace(e.Stderr) != "" {
			raw += "\n--- STDERR ---\n" + strings.TrimSpace(e.Stderr)
		}
		if e.Error != "" {
			raw += "\n--- ERROR ---\n" + e.Error
		}
		for _, ln := range strings.Split(raw, "\n") {
			outLines = append(outLines, clampLine(ln, width))
		}
	}

	return strings.Join([]string{
		clampLine(promptHint, width),
		clampLine(promptLine, width),
		styleMuted.Render(strings.Repeat("─", width)),
		sliceVisibleLines(outLines, m.inspectorScroll, outH),
	}, "\n")
}

// ==================== [3] ANSIBLE INVENTORY ====================

func (m Model) viewInventory(bodyH int) string {
	innerW := m.innerWidth()
	innerH := maxInt(5, bodyH-2)

	wAlias, wAddr, wUser, wPort := 16, 16, 12, 5
	rem := maxInt(16, innerW-(2+wAlias+wAddr+wUser+wPort+5))
	wGroups := maxInt(10, rem*55/100)
	wKey := maxInt(8, rem-wGroups)

	header := clampLine(styleTitle.Render("ANSIBLE INVENTORY & ENROLLMENT")+"  "+
		styleMuted.Render("[a] Add [i] Import [d] Del [e] $EDITOR [v] Raw [!] SSH [/] Search [s] Scan"), innerW)
	sep := styleMuted.Render(strings.Repeat("─", innerW))
	colHead := clampLine(styleMuted.Render("  "+strings.Join([]string{
		padCell("HOST ALIAS", wAlias),
		padCell("ADDRESS / IP", wAddr),
		padCell("USER", wUser),
		padCell("PORT", wPort),
		padCell("GROUPS", wGroups),
		padCell("SSH KEY / JUMP", wKey),
	}, " ")), innerW)

	hosts := m.filteredHosts()
	var rows []string
	if len(hosts) == 0 {
		msg := "  (no servers enrolled — press [a] to add or [i] to import an Ansible inventory file)"
		if m.searchQuery != "" {
			msg = fmt.Sprintf("  (no servers match %q — press Esc to clear)", m.searchQuery)
		}
		rows = append(rows, clampLine(styleMuted.Render(msg), innerW))
	} else {
		for i, h := range hosts {
			cursor := "  "
			if i == m.invCursor {
				cursor = "▶ "
			}
			key := h.PrivateKey
			if key == "" {
				key = "(agent/default)"
			}
			if j := m.effectiveJump(h); j != "" {
				key = "via " + j
			}
			row := clampLine(cursor+strings.Join([]string{
				padCell(h.Name, wAlias),
				padCell(h.Address, wAddr),
				padCell(m.effectiveUser(h), wUser),
				padCell(fmt.Sprintf("%d", m.effectivePort(h)), wPort),
				padCell(strings.Join(h.Groups, ","), wGroups),
				padCell(key, wKey),
			}, " "), innerW)
			if i == m.invCursor {
				row = styleSelectedRow.Width(innerW).Render(row)
			}
			rows = append(rows, row)
		}
	}

	dataH := maxInt(1, innerH-3)
	scroll := adjustRowScroll(m.invCursor, 0, dataH)
	content := header + "\n" + sep + "\n" + colHead + "\n" + sliceVisibleLines(rows, scroll, dataH)
	return styleBox.Width(m.boxWidth()).Height(innerH).MaxHeight(bodyH).Render(content)
}

// ==================== [4] PLAYBOOKS & SCRIPTS ====================

func (m Model) viewAutomation(bodyH int) string {
	list := m.filteredAutomations()
	leftBoxW := maxInt(28, m.width/3)
	rightBoxW := maxInt(30, m.width-leftBoxW-5)
	leftInnerW := maxInt(24, leftBoxW-2)
	rightInnerW := maxInt(26, rightBoxW-2)
	innerH := maxInt(6, bodyH-2)

	var leftLines []string
	leftLines = append(leftLines, clampLine(styleTitle.Render("LIBRARY ")+badge(strings.ToUpper(m.autoFilter), colorCyan), leftInnerW))
	leftLines = append(leftLines, clampLine(styleMuted.Render("[f] filter [g] gen [e] edit [a] add [d] del [/] find"), leftInnerW))
	leftLines = append(leftLines, styleMuted.Render(strings.Repeat("─", leftInnerW)))

	var itemLines []string
	if len(list) == 0 {
		itemLines = append(itemLines, clampLine(styleMuted.Render("(none — press [g] to generate)"), leftInnerW))
	} else {
		for i, it := range list {
			cur := "  "
			if i == m.autoCursor {
				cur = "▶ "
			}
			tag := lipgloss.NewStyle().Foreground(colorViolet).Render("PB")
			if it.Kind == "script" {
				tag = lipgloss.NewStyle().Foreground(colorEmerald).Render("SH")
			}
			line := clampLine(fmt.Sprintf("%s%s %s", cur, tag, it.Name), leftInnerW)
			if i == m.autoCursor {
				line = styleSelectedRow.Width(leftInnerW).Render(line)
			}
			sub := it.FileName
			if it.Venv != "" {
				sub += " [venv]"
			}
			itemLines = append(itemLines, line, clampLine("     "+styleMuted.Render(sub), leftInnerW))
		}
	}
	leftScroll := maxInt(0, m.autoCursor*2-(innerH-5))
	leftContent := strings.Join(leftLines, "\n") + "\n" + sliceVisibleLines(itemLines, leftScroll, maxInt(2, innerH-3))
	leftPane := styleBox.Width(leftBoxW).Height(innerH).MaxHeight(bodyH).Render(leftContent)

	var rightLines []string
	if item := m.selectedAutomation(); item != nil {
		kindCol := colorViolet
		if item.Kind == "script" {
			kindCol = colorEmerald
		}
		venvBadge := ""
		if item.Venv != "" {
			venvBadge = " " + badge("venv:"+item.Venv, colorCyan)
		}
		rightLines = append(rightLines,
			clampLine(badge(strings.ToUpper(item.Kind), kindCol)+" "+lipgloss.NewStyle().Bold(true).Render(item.Name)+venvBadge+"  "+styleMuted.Render("[Enter] Run [e] Edit"), rightInnerW),
			clampLine(styleSubtitle.Render(item.Description), rightInnerW),
			styleMuted.Render(strings.Repeat("─", rightInnerW)),
		)
		srcH := maxInt(3, (innerH-5)/2)
		var srcLines []string
		for _, ln := range strings.Split(item.Content, "\n") {
			srcLines = append(srcLines, clampLine(ln, rightInnerW))
		}
		rightLines = append(rightLines, sliceVisibleLines(srcLines, m.autoSourceScroll, srcH))
		rightLines = append(rightLines, styleMuted.Render(strings.Repeat("─", rightInnerW)))

		outH := maxInt(2, innerH-4-srcH)
		if m.runningExec && (m.liveKind == "playbook" || m.liveKind == "script") {
			var liveLines []string
			liveLines = append(liveLines, clampLine(m.spin.View()+" Streaming live output... [Ctrl+X to cancel]", rightInnerW))
			start := maxInt(0, len(m.liveOutput)-(outH-1))
			for _, ln := range m.liveOutput[start:] {
				liveLines = append(liveLines, clampLine(ln, rightInnerW))
			}
			rightLines = append(rightLines, sliceVisibleLines(liveLines, 0, outH))
		} else if m.lastAutoLog != nil {
			e := m.lastAutoLog
			col := colorEmerald
			if !e.Succeeded() {
				col = colorRose
			}
			head := clampLine(
				badge(fmt.Sprintf("LAST RUN: %s EXIT %d (%dms)", e.Engine, e.ExitCode, e.DurationMs), col)+
					"  "+styleMuted.Render("[J/K/PgDn] Scroll  [o] Fullscreen"),
				rightInnerW,
			)
			wrapped := wrapTextLines(formatExecLogBody(*e), rightInnerW)
			bodyHOut := maxInt(1, outH-1)
			rightLines = append(rightLines, head)
			rightLines = append(rightLines, sliceVisibleLines(wrapped, m.autoOutputScroll, bodyHOut))
		} else {
			rightLines = append(rightLines, clampLine(styleMuted.Render("Press [Enter] or [x] to run this "+item.Kind+" (supports --venv, --check, --diff, --tags, live streaming)."), rightInnerW))
		}
	} else {
		rightLines = append(rightLines, clampLine(styleMuted.Render("Select a playbook or script, or press [g] to generate one."), rightInnerW))
	}
	rightPane := styleBox.Width(rightBoxW).Height(innerH).MaxHeight(bodyH).Render(strings.Join(rightLines, "\n"))

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane)
}

// ==================== [5] EXECUTION LOG ====================

func (m Model) viewLogs(bodyH int) string {
	innerH := maxInt(6, bodyH-2)
	logs := m.filteredLogs()
	if len(logs) == 0 {
		msg := "No playbooks, scripts, or SSH commands have been executed yet."
		if len(m.execLogs) > 0 {
			msg = fmt.Sprintf("No log entries match filter %q / %q (press [f] or [Esc]).", m.logFilter, m.searchQuery)
		}
		return styleBox.Width(m.boxWidth()).Height(innerH).MaxHeight(bodyH).Render(
			styleTitle.Render("EXECUTION AUDIT LOG ") + badge(strings.ToUpper(m.logFilter), colorCyan) + "\n\n" +
				styleMuted.Render(msg),
		)
	}
	leftBoxW := maxInt(30, m.width/3)
	rightBoxW := maxInt(30, m.width-leftBoxW-5)
	leftInnerW := maxInt(26, leftBoxW-2)
	rightInnerW := maxInt(26, rightBoxW-2)

	var leftHead []string
	leftHead = append(leftHead, clampLine(styleTitle.Render("HISTORY ")+badge(strings.ToUpper(m.logFilter), colorCyan)+styleMuted.Render(" [f] [/]"), leftInnerW))
	leftHead = append(leftHead, styleMuted.Render(strings.Repeat("─", leftInnerW)))
	var leftItems []string
	for i, e := range logs {
		cur := "  "
		if i == m.logCursor {
			cur = "▶ "
		}
		col := colorEmerald
		if !e.Succeeded() {
			col = colorRose
		}
		line := clampLine(fmt.Sprintf("%s%s %s", cur, badge(fmt.Sprintf("%d", e.ExitCode), col), e.Title), leftInnerW)
		if i == m.logCursor {
			line = styleSelectedRow.Width(leftInnerW).Render(line)
		}
		leftItems = append(leftItems, line, clampLine("    "+styleMuted.Render(fmt.Sprintf("%s • %s • %s", e.Timestamp.Format("01-02 15:04"), e.Kind, e.Target)), leftInnerW))
	}
	leftScroll := maxInt(0, m.logCursor*2-(innerH-4))
	leftPane := styleBox.Width(leftBoxW).Height(innerH).MaxHeight(bodyH).Render(
		strings.Join(leftHead, "\n") + "\n" + sliceVisibleLines(leftItems, leftScroll, maxInt(2, innerH-2)),
	)

	idx := m.logCursor
	if idx < 0 || idx >= len(logs) {
		idx = 0
	}
	e := logs[idx]
	col := colorEmerald
	if !e.Succeeded() {
		col = colorRose
	}
	bodyLines := wrapTextLines(formatExecLogBody(e), rightInnerW)
	viewH := maxInt(2, innerH-3)
	maxOff := maxInt(0, len(bodyLines)-viewH)
	effOff := m.logScroll
	if effOff > maxOff {
		effOff = maxOff
	}
	if effOff < 0 {
		effOff = 0
	}
	meta := fmt.Sprintf("Target: %s • %s • %dms • %s", e.Target, e.Engine, e.DurationMs, e.Timestamp.Format("15:04:05"))
	if e.Venv != "" {
		meta += " • Venv: " + e.Venv
	}
	scrollInfo := fmt.Sprintf("[J/K/PgDn] Scroll (%d/%d)  [Enter] Fullscreen", minInt(len(bodyLines), effOff+viewH), len(bodyLines))
	rightLines := []string{
		clampLine(badge(fmt.Sprintf("EXIT %d", e.ExitCode), col)+" "+lipgloss.NewStyle().Bold(true).Render(e.Title)+"  "+styleMuted.Render(scrollInfo), rightInnerW),
		clampLine(styleSubtitle.Render(meta), rightInnerW),
		styleMuted.Render(strings.Repeat("─", rightInnerW)),
		sliceVisibleLines(bodyLines, effOff, viewH),
	}
	rightPane := styleBox.Width(rightBoxW).Height(innerH).MaxHeight(bodyH).Render(strings.Join(rightLines, "\n"))

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane)
}

// ==================== MODALS ====================

func (m Model) viewModal() string {
	switch m.modal {
	case modalSshConfig:
		jumpLine := ""
		if len(m.inputs) >= 5 {
			jumpLine = "ProxyJump Bastion (optional):\n" + m.inputs[4].View()
		}
		return styleModal.Width(minInt(66, m.width-4)).Render(strings.Join([]string{
			styleTitle.Render("SSH CONNECTION SETUP"),
			styleSubtitle.Render("Specify SSH credentials & optional ProxyJump (~/.ssh/config is also supported)."),
			"",
			"SSH Username:\n" + m.inputs[0].View(),
			"SSH Port:\n" + m.inputs[1].View(),
			"Private Key Path:\n" + m.inputs[2].View(),
			"SSH Password / Key Passphrase (memory-only):\n" + m.inputs[3].View(),
			jumpLine,
			"",
			styleMuted.Render("[Tab/↑/↓] Next field  [Enter] Save  [Ctrl+S] Save & Scan All  [Esc] Skip"),
		}, "\n"))

	case modalAddHost:
		return styleModal.Width(minInt(60, m.width-4)).Render(strings.Join([]string{
			styleTitle.Render("ENROLL SERVER INTO ANSIBLE INVENTORY"),
			"",
			"Host Alias:\n" + m.inputs[0].View(),
			"IP Address / Hostname:\n" + m.inputs[1].View(),
			"SSH User:\n" + m.inputs[2].View(),
			"SSH Port:\n" + m.inputs[3].View(),
			"Groups (comma-separated):\n" + m.inputs[4].View(),
			"Private Key Path (optional):\n" + m.inputs[5].View(),
			"",
			styleMuted.Render("[Tab/↑/↓] Next field  [Enter] Enroll & Save  [Esc] Cancel"),
		}, "\n"))

	case modalImportInv:
		return styleModal.Width(minInt(66, m.width-4)).Render(strings.Join([]string{
			styleTitle.Render("IMPORT ANSIBLE INVENTORY FILE (INI / YAML)"),
			styleSubtitle.Render("Merges all hosts, groups, and vars into " + m.cfg.InventoryPath),
			"",
			"Path to inventory file:\n" + m.inputs[0].View(),
			"",
			styleMuted.Render("[Enter] Import & Merge  [Esc] Cancel"),
		}, "\n"))

	case modalRawInv:
		rawLines := strings.Split(m.rawInvText, "\n")
		w := minInt(88, m.width-8)
		var clamped []string
		for _, ln := range rawLines {
			clamped = append(clamped, clampLine(ln, w-6))
		}
		return styleModal.Width(w).Render(strings.Join([]string{
			styleTitle.Render("RAW ANSIBLE INVENTORY: " + m.cfg.InventoryPath),
			styleMuted.Render(strings.Repeat("─", maxInt(20, w-6))),
			sliceVisibleLines(clamped, 0, minInt(22, m.height-10)),
			"",
			styleMuted.Render("[Enter / Esc] Close  (press [e] in Inventory tab to edit in $EDITOR)"),
		}, "\n"))

	case modalOutput:
		w := minInt(108, m.width-4)
		h := maxInt(8, m.height-10)
		lines := wrapTextLines(m.outputBody, maxInt(20, w-6))
		maxOff := maxInt(0, len(lines)-h)
		effOff := m.outputScroll
		if effOff > maxOff {
			effOff = maxOff
		}
		if effOff < 0 {
			effOff = 0
		}
		rangeLabel := fmt.Sprintf("Lines %d-%d of %d", minInt(len(lines), effOff+1), minInt(len(lines), effOff+h), len(lines))
		return styleModal.Width(w).Render(strings.Join([]string{
			clampLine(styleTitle.Render(m.outputTitle), w-6),
			styleMuted.Render(strings.Repeat("─", maxInt(20, w-6))),
			sliceVisibleLines(lines, effOff, h),
			"",
			clampLine(styleMuted.Render(rangeLabel+"  •  [↑/↓/j/k/PgUp/PgDn/g/G/Wheel] Scroll  [Enter/Esc/q] Close"), w-6),
		}, "\n"))

	case modalRunAuto:
		item := m.selectedAutomation()
		title := "RUN AUTOMATION"
		if item != nil {
			title = fmt.Sprintf("RUN %s: %s", strings.ToUpper(item.Kind), item.FileName)
		}
		if item != nil && item.Kind == "playbook" && len(m.inputs) >= 4 {
			chk := badge("OFF", colorMuted)
			if m.runCheckMode {
				chk = badge("--check", colorAmber)
			}
			dif := badge("OFF", colorMuted)
			if m.runDiffMode {
				dif = badge("--diff", colorCyan)
			}
			bec := badge("OFF", colorMuted)
			if m.runBecome {
				bec = badge("--become", colorViolet)
			}
			verb := badge("0", colorMuted)
			if m.runVerbosity > 0 {
				verb = badge("-"+strings.Repeat("v", m.runVerbosity), colorEmerald)
			}
			venvHint := fmt.Sprintf("Python Venv [Ctrl+V cycle %d detected]:", len(m.venvs))
			return styleModal.Width(minInt(70, m.width-4)).Render(strings.Join([]string{
				styleTitle.Render(title),
				"",
				"Target Host / Group / IP:\n" + m.inputs[0].View(),
				"Extra Variables (k=v, k2=v2):\n" + m.inputs[1].View(),
				venvHint + "\n" + m.inputs[2].View(),
				"Tags (--tags, optional):\n" + m.inputs[3].View(),
				"",
				fmt.Sprintf("Check [Ctrl+B]: %s  Diff [Ctrl+D]: %s  Become [Ctrl+U]: %s  Verb [Ctrl+E]: %s", chk, dif, bec, verb),
				"",
				styleMuted.Render("[Tab] Field  [Ctrl+V] Venv  [Ctrl+B/D/U/E] Flags  [Enter] Execute  [Esc] Cancel"),
			}, "\n"))
		}
		return styleModal.Width(minInt(64, m.width-4)).Render(strings.Join([]string{
			styleTitle.Render(title),
			"",
			"Target Host / Group / IP:\n" + m.inputs[0].View(),
			"Env Variables (K=V, K2=V2):\n" + m.inputs[1].View(),
			"",
			styleMuted.Render("[Tab] Field  [Enter] Execute  [Esc] Cancel"),
		}, "\n"))

	case modalGenerate:
		tpls := m.currentGenTemplates()
		tpl := tpls[m.genTemplateIdx%len(tpls)]
		becomeStr := "false"
		if m.genBecome {
			becomeStr = "true"
		}
		left := strings.Join([]string{
			styleTitle.Render("GENERATOR WIZARD"),
			fmt.Sprintf("Kind [Ctrl+K]:     %s", badge(strings.ToUpper(m.genKind), colorCyan)),
			fmt.Sprintf("Template [Ctrl+T]: %s", badge(tpl, colorViolet)),
			fmt.Sprintf("Sudo [Ctrl+B]:     %s", badge(becomeStr, colorAmber)),
			"",
			"Name:\n" + m.inputs[0].View(),
			"Filename:\n" + m.inputs[1].View(),
			"Target Hosts:\n" + m.inputs[2].View(),
			"Param 1 (domain/svc/path/cmd):\n" + m.inputs[3].View(),
			"Param 2 (upstream):\n" + m.inputs[4].View(),
		}, "\n")
		var prevLines []string
		for _, ln := range strings.Split(m.previewGeneratedContent(), "\n") {
			prevLines = append(prevLines, clampLine(ln, 40))
		}
		preview := sliceVisibleLines(prevLines, 0, 18)
		right := styleBox.Width(44).Render(styleAccent.Render("LIVE PREVIEW") + "\n" + preview)
		body := lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(44).Render(left), "  ", right)
		return styleModal.Width(minInt(98, m.width-4)).Render(body + "\n\n" + styleMuted.Render("[Tab] Field  [Ctrl+K] Kind  [Ctrl+T] Template  [Ctrl+B] Sudo  [Enter] Save  [Esc] Cancel"))

	case modalAddAuto:
		return styleModal.Width(minInt(68, m.width-4)).Render(strings.Join([]string{
			styleTitle.Render("ADD / IMPORT PLAYBOOK OR SCRIPT"),
			fmt.Sprintf("Kind [Ctrl+K]: %s", badge(strings.ToUpper(m.genKind), colorCyan)),
			styleSubtitle.Render("Enter an existing .yml/.sh path to import, OR a new filename + command to create."),
			"",
			"File path or new filename:\n" + m.inputs[0].View(),
			"Command (for new file):\n" + m.inputs[1].View(),
			"",
			styleMuted.Render("[Tab] Field  [Ctrl+K] Playbook/Script  [Enter] Save  [Esc] Cancel"),
		}, "\n"))

	case modalHelp:
		return styleModal.Width(minInt(74, m.width-4)).Render(strings.Join([]string{
			styleTitle.Render("DUCHESS DEVOPS TUI — KEYBINDINGS"),
			styleMuted.Render(strings.Repeat("─", 66)),
			"Global:",
			"  1-5 / Tab       Switch Overview, Inspector, Inventory, Automation, Logs",
			"  /               Fuzzy filter hosts, playbooks, scripts, or logs",
			"  !               Open interactive TTY SSH shell to selected server",
			"  s / S           Scan selected server / Scan ALL servers via SSH",
			"  A               Toggle live auto-refresh polling",
			"  k               Configure SSH credentials & ProxyJump bastion",
			"  Ctrl+X          Cancel an in-flight streaming playbook/script/SSH run",
			"  r / q           Reload workspace / Quit",
			"",
			"Server Inspector [2]:",
			"  [ / ]  ← / →    Switch server • Switch sub-tab • [a] Host health alerts",
			"  Containers:     [l] logs • [R] restart • [x] stop/start • [E] exec shell",
			"  Web Apps:       [v] view config • [t] test config • [c] TLS cert info",
			"  Services:       [l] journalctl • [R] restart • [o] reload",
			"  Ad-hoc SSH:     [i] focus input • [Ctrl+G] toggle Host / Group / All fan-out",
			"",
			"Inventory [3]:    a Add • i Import • e $EDITOR • v Raw INI • d Delete • ! SSH",
			"Automation [4]:   Enter Run (--venv/--check/--diff/--tags) • e $EDITOR • g Gen",
			"Exec Log [5]:     f Filter (all/failed/kind) • [/] Search • [[/]] Scroll",
			"",
			styleMuted.Render("[Enter / Esc] Close"),
		}, "\n"))
	}
	return ""
}

func adjustRowScroll(row, curScroll, viewHeight int) int {
	if viewHeight <= 0 {
		return 0
	}
	if row < curScroll {
		return row
	}
	if row >= curScroll+viewHeight {
		return row - viewHeight + 1
	}
	return curScroll
}

func sliceVisibleLines(lines []string, offset, height int) string {
	if height <= 0 {
		return ""
	}
	if len(lines) == 0 {
		return ""
	}
	maxOff := maxInt(0, len(lines)-height)
	if offset > maxOff {
		offset = maxOff
	}
	if offset < 0 {
		offset = 0
	}
	end := minInt(len(lines), offset+height)
	return strings.Join(lines[offset:end], "\n")
}

func formatExecLogBody(e ExecLogEntry) string {
	body := strings.TrimSpace(e.Stdout)
	if strings.TrimSpace(e.Stderr) != "" {
		if body != "" {
			body += "\n--- STDERR ---\n"
		}
		body += strings.TrimSpace(e.Stderr)
	}
	if e.Error != "" {
		if body != "" {
			body += "\n--- ERROR ---\n"
		}
		body += e.Error
	}
	if body == "" {
		return "(no output)"
	}
	return body
}

// wrapTextLines wraps long output lines cleanly to width cells so verbose
// Ansible JSON error messages are fully readable without horizontal truncation.
func wrapTextLines(text string, width int) []string {
	if width <= 4 {
		width = 20
	}
	var out []string
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if raw == "" {
			out = append(out, "")
			continue
		}
		runes := []rune(raw)
		for len(runes) > width {
			out = append(out, string(runes[:width]))
			runes = runes[width:]
		}
		out = append(out, string(runes))
	}
	return out
}
