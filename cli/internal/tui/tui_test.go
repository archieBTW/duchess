package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/duchess-devops/duchess-cli/internal/models"
)

func TestTuiRendersAllViewsAndDiscoveryData(t *testing.T) {
	wsRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	cfg := Config{
		WorkspaceDir:  wsRoot,
		InventoryPath: filepath.Join(wsRoot, "inventory", "hosts.ini"),
	}
	m := NewModel(cfg)
	m.width = 130
	m.height = 40

	// Overview renders 127.0.0.1 from hosts.ini
	out := m.View()
	if !strings.Contains(out, "127.0.0.1") || !strings.Contains(out, "prod-app-01") {
		t.Fatalf("expected overview to display prod-app-01 (127.0.0.1), got:\n%s", out)
	}

	// Simulate SSH discovery result arriving
	rep := &models.DiscoveryReport{
		HostName:    "prod-app-01",
		Address:     "127.0.0.1",
		CollectedAt: time.Now(),
		Stats: &models.ServerStats{
			HostName:        "prod-app-01",
			Address:         "127.0.0.1",
			HostnameFQDN:    "prod-app-01.internal",
			OS:              "Ubuntu 24.04 LTS",
			Kernel:          "6.8.0",
			Arch:            "x86_64",
			UptimeHuman:     "3d 4h 12m",
			CPUCores:        4,
			CPUUsagePercent: 42.5,
			MemTotalBytes:   8 * 1024 * 1024 * 1024,
			MemUsedBytes:    4 * 1024 * 1024 * 1024,
			MemUsagePercent: 50.0,
			Disks: []models.DiskUsage{{
				Filesystem: "/dev/sda1",
				MountPoint: "/",
				TotalBytes: 100 * 1024 * 1024 * 1024,
				UsedBytes:  40 * 1024 * 1024 * 1024,
				UsePercent: 40,
			}},
		},
		Containers: []models.ContainerInfo{{
			ID: "abc123", Name: "api-gw", Image: "nginx:alpine", Runtime: "docker", State: "running", CPUPerc: "2.1%",
		}},
		WebApps: []models.WebAppInfo{{
			Name: "portal.example.com", ServerType: "nginx", Domains: []string{"portal.example.com"}, Ports: []string{"443"}, SSL: true, ProxyTarget: "http://127.0.0.1:8080",
		}},
		Services: []models.ServiceInfo{{
			Name: "nginx", ActiveState: "active", SubState: "running", Ports: []string{"80", "443"}, Description: "Nginx HTTP Server",
		}},
	}

	updated, _ := m.Update(scanDoneMsg{hostName: "prod-app-01", report: rep})
	m = updated.(Model)

	// Switch to Inspector [2] and verify ASCII gauge + Containers + WebApps
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = updated.(Model)
	insp := m.View()
	if !strings.Contains(insp, "Ubuntu 24.04 LTS") || !strings.Contains(insp, "api-gw") || !strings.Contains(insp, "42.5%") {
		t.Fatalf("expected inspector to render stats and containers, got:\n%s", insp)
	}

	// Switch sub-tab to Websites & Apps
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	if !strings.Contains(m.View(), "portal.example.com") {
		t.Fatalf("expected Websites & Apps tab to show portal.example.com")
	}

	// Switch to Inventory [3]
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = updated.(Model)
	if !strings.Contains(m.View(), "ANSIBLE INVENTORY") {
		t.Fatalf("expected inventory view")
	}

	// Switch to Automation [4] and open generator modal [g]
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	m = updated.(Model)
	if !strings.Contains(m.View(), "system-health-check.yml") {
		t.Fatalf("expected automation library to list system-health-check.yml")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	genView := m.View()
	if !strings.Contains(genView, "GENERATOR WIZARD") || !strings.Contains(genView, "LIVE PREVIEW") {
		t.Fatalf("expected generator modal with live preview, got:\n%s", genView)
	}
}

func TestTuiLaunchPromptsForSSHUser(t *testing.T) {
	wsRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	m := NewModel(Config{
		WorkspaceDir:      wsRoot,
		InventoryPath:     filepath.Join(wsRoot, "inventory", "hosts.ini"),
		PromptSSHOnLaunch: true,
	})
	m.width = 120
	m.height = 36

	if m.modal != modalSshConfig {
		t.Fatalf("expected SSH setup modal on launch, got modal=%v", m.modal)
	}
	if !strings.Contains(m.View(), "SSH CONNECTION SETUP") {
		t.Fatalf("expected SSH CONNECTION SETUP dialog on launch")
	}

	// Type custom user "deploy" and press Enter
	for _, r := range "deploy" {
		up, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = up.(Model)
	}
	up, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = up.(Model)

	if m.modal != modalNone {
		t.Fatalf("expected modal to close after Enter")
	}
	if m.effectiveUser(m.selectedHost()) != "deploy" {
		t.Fatalf("expected effective SSH user 'deploy', got %q", m.effectiveUser(m.selectedHost()))
	}
	if !m.autoRefresh {
		t.Fatalf("expected autoRefresh to be enabled by default")
	}
	if !m.scanning["prod-app-01"] {
		t.Fatalf("expected all hosts to automatically begin scanning after launch SSH modal closes")
	}
	view := m.View()
	if strings.Contains(view, "DEVOPS UNRESTRICTED") {
		t.Fatalf("top bar should no longer show DEVOPS UNRESTRICTED")
	}
	if !strings.Contains(view, "SSH User: deploy") {
		t.Fatalf("expected top bar to show 'SSH User: deploy', got:\n%s", view)
	}
}

func TestInspectorSubTabsKeepFixedDimensions(t *testing.T) {
	wsRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	m := NewModel(Config{
		WorkspaceDir:  wsRoot,
		InventoryPath: filepath.Join(wsRoot, "inventory", "hosts.ini"),
	})

	// Populate 1 container, 2 webapps, and 60 long-description services
	var svcs []models.ServiceInfo
	for i := 0; i < 60; i++ {
		svcs = append(svcs, models.ServiceInfo{
			Name:        fmt.Sprintf("service-with-very-long-unit-name-%02d", i),
			ActiveState: "active",
			SubState:    "running",
			Ports:       []string{"80", "443", "8080"},
			Description: "A very long service description that would wrap on narrow terminals if not properly clamped to column width",
		})
	}
	rep := &models.DiscoveryReport{
		HostName:    "prod-app-01",
		Address:     "127.0.0.1",
		CollectedAt: time.Now(),
		Stats: &models.ServerStats{
			HostName:        "prod-app-01",
			Address:         "127.0.0.1",
			HostnameFQDN:    "prod-app-01.internal",
			OS:              "Ubuntu 24.04.1 LTS",
			Kernel:          "6.8.0-45-generic",
			Arch:            "x86_64",
			UptimeHuman:     "12d 3h 4m",
			CPUCores:        4,
			CPUUsagePercent: 25.0,
			MemTotalBytes:   8 * 1024 * 1024 * 1024,
			MemUsedBytes:    3 * 1024 * 1024 * 1024,
			MemUsagePercent: 37.5,
		},
		Containers: []models.ContainerInfo{{
			ID: "a1b2c3", Name: "api-gateway-long-name", Image: "ghcr.io/org/very-long-image-name:v1.2.3",
			Runtime: "docker", State: "running", CPUPerc: "1.5%", MemUsage: "128MiB / 8GiB", Ports: "0.0.0.0:8080->8080/tcp, 0.0.0.0:8443->8443/tcp",
		}},
		WebApps: []models.WebAppInfo{
			{Name: "portal.example.com", ServerType: "nginx", Domains: []string{"portal.example.com", "www.portal.example.com"}, Ports: []string{"80", "443"}, SSL: true, ProxyTarget: "http://127.0.0.1:8080"},
			{Name: "tomcat:billing", ServerType: "tomcat", Domains: []string{"/billing"}, Ports: []string{"8080"}, SSL: false, RootOrPath: "/opt/tomcat9/webapps/billing.war"},
		},
		Services: svcs,
	}
	up, _ := m.Update(scanDoneMsg{hostName: "prod-app-01", report: rep})
	m = up.(Model)
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = up.(Model)

	for _, sz := range [][2]int{{80, 26}, {120, 38}} {
		m.width, m.height = sz[0], sz[1]
		var baselineH int
		for sub := 0; sub < 4; sub++ {
			m.inspectorSub = inspectorSubTab(sub)
			view := m.View()
			h := lipgloss.Height(view)
			if sub == 0 {
				baselineH = h
			} else if h != baselineH {
				t.Fatalf("size %dx%d: sub-tab %d height changed from %d to %d", sz[0], sz[1], sub, baselineH, h)
			}
			if h != sz[1] {
				t.Fatalf("size %dx%d: sub-tab %d total height=%d, want exact terminal height %d", sz[0], sz[1], sub, h, sz[1])
			}
			for lnIdx, ln := range strings.Split(view, "\n") {
				if w := ansi.StringWidth(ln); w > sz[0] {
					t.Fatalf("size %dx%d: sub-tab %d line %d width=%d exceeds terminal width %d: %q", sz[0], sz[1], sub, lnIdx, w, sz[0], ln)
				}
			}
		}
	}
}

func TestTuiPlaybookVenvModalAlertsSearchAndStreaming(t *testing.T) {
	wsRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	m := NewModel(Config{
		WorkspaceDir:  wsRoot,
		InventoryPath: filepath.Join(wsRoot, "inventory", "hosts.ini"),
	})
	m.width = 130
	m.height = 40

	// 1. Playbook Run modal shows Python Venv, Tags, Diff, Become, Verbosity toggles
	up, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	m = up.(Model)
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = up.(Model)
	if m.modal != modalRunAuto {
		t.Fatalf("expected modalRunAuto, got %v", m.modal)
	}
	runModalView := m.View()
	for _, want := range []string{"Python Venv", "Tags (--tags", "Check [Ctrl+B]", "Diff [Ctrl+D]"} {
		if !strings.Contains(runModalView, want) {
			t.Fatalf("expected playbook run modal to contain %q, got:\n%s", want, runModalView)
		}
	}
	// Toggle --diff and verbosity
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = up.(Model)
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = up.(Model)
	if !m.runDiffMode || m.runVerbosity != 1 {
		t.Fatalf("expected diff=true verbosity=1, got diff=%v verb=%d", m.runDiffMode, m.runVerbosity)
	}
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = up.(Model)

	// 2. Live streaming output renders while running
	m.runningExec = true
	m.liveKind = "playbook"
	up, _ = m.Update(streamLineMsg{line: "TASK [Check health] ***"})
	m = up.(Model)
	if !strings.Contains(m.View(), "TASK [Check health]") {
		t.Fatalf("expected streamed line to render in automation view")
	}
	m.runningExec = false

	// 3. Proactive alerts surface in Overview and Inspector
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = up.(Model)
	rep := &models.DiscoveryReport{
		HostName:    "prod-app-01",
		Address:     "127.0.0.1",
		CollectedAt: time.Now(),
		Stats: &models.ServerStats{
			HostName:        "prod-app-01",
			Address:         "127.0.0.1",
			CPUUsagePercent: 96.0,
			MemUsagePercent: 92.0,
			Disks:           []models.DiskUsage{{MountPoint: "/", UsePercent: 97}},
		},
	}
	up, _ = m.Update(scanDoneMsg{hostName: "prod-app-01", report: rep})
	m = up.(Model)
	if len(m.reports["prod-app-01"].Alerts) < 3 {
		t.Fatalf("expected >=3 alerts evaluated, got %+v", m.reports["prod-app-01"].Alerts)
	}
	if !strings.Contains(m.View(), "ALERT") {
		t.Fatalf("expected header/overview to show alert badge")
	}

	// 4. Fuzzy search filter '/' works on Overview
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = up.(Model)
	for _, r := range "zzzz-nomatch" {
		up, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = up.(Model)
	}
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = up.(Model)
	if len(m.filteredHosts()) != 0 {
		t.Fatalf("expected 0 hosts matching zzzz-nomatch")
	}
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = up.(Model)
	if len(m.filteredHosts()) == 0 {
		t.Fatalf("expected Esc to clear search filter")
	}

	// 5. Ad-hoc SSH scope cycles host -> group -> all
	m.activeTab = tabInspector
	m.inspectorSub = subAdHocSSH
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = up.(Model)
	if m.adHocScope != "group" {
		t.Fatalf("expected adHocScope=group, got %q", m.adHocScope)
	}
	up, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = up.(Model)
	if m.adHocScope != "all" {
		t.Fatalf("expected adHocScope=all, got %q", m.adHocScope)
	}
}
