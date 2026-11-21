package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/duchess-devops/duchess-cli/internal/models"
)

func TestLoadWorkspaceConfigAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	yml := `
ansible_venv: /opt/venvs/ansible-2.17
remote_venv: /srv/app/.venv
poll_interval_seconds: 15
exec_timeout_seconds: 300
ssh:
  user: ops
  jump_host: bastion.internal
  strict_host_key: true
alerts:
  disk_warn_pct: 80
  cert_warn_days: 21
`
	if err := os.WriteFile(filepath.Join(dir, "duchess.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Load(dir)
	if cfg.AnsibleVenv != "/opt/venvs/ansible-2.17" {
		t.Fatalf("expected ansible_venv from yaml, got %q", cfg.AnsibleVenv)
	}
	if cfg.RemoteVenv != "/srv/app/.venv" {
		t.Fatalf("expected remote_venv from yaml, got %q", cfg.RemoteVenv)
	}
	if cfg.PollIntervalSeconds != 15 || cfg.ExecTimeoutSeconds != 300 {
		t.Fatalf("unexpected intervals: poll=%d timeout=%d", cfg.PollIntervalSeconds, cfg.ExecTimeoutSeconds)
	}
	if cfg.SSH.JumpHost != "bastion.internal" || !cfg.SSH.StrictHostKey {
		t.Fatalf("unexpected ssh config: %+v", cfg.SSH)
	}
	if cfg.Alerts.DiskWarnPct != 80 || cfg.Alerts.CertWarnDays != 21 || cfg.Alerts.MemWarnPct != 90 {
		t.Fatalf("unexpected alerts config: %+v", cfg.Alerts)
	}

	t.Setenv("DUCHESS_ANSIBLE_VENV", "/tmp/override-venv")
	cfg2 := Load(dir)
	if cfg2.AnsibleVenv != "/tmp/override-venv" {
		t.Fatalf("expected env override, got %q", cfg2.AnsibleVenv)
	}
}

func TestPerPlaybookAndPerServerVenvMatchingAndSave(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.AnsibleVenv = "/opt/venvs/default"
	cfg.PlaybookVenvs["deploy-*.yml"] = "/opt/venvs/deploy-all"
	cfg.PlaybookVenvs["deploy-bybl.yml"] = "/opt/venvs/bybl-exact"
	cfg.ServerVenvs["production"] = "/opt/venvs/prod-group"
	cfg.ServerVenvs["prod-db-01"] = "/opt/venvs/prod-db"
	cfg.ServerRemoteVenvs["prod-db-01"] = "/srv/db/.venv"

	saved, err := Save(dir, "", cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded := Load(dir)
	if loaded.ConfigPath != saved {
		t.Fatalf("expected ConfigPath=%q, got %q", saved, loaded.ConfigPath)
	}

	// Exact playbook match beats glob
	if got := loaded.MatchPlaybookVenv("/ws/playbooks/deploy-bybl.yml"); got != "/opt/venvs/bybl-exact" {
		t.Fatalf("expected exact playbook venv, got %q", got)
	}
	// Glob match works for other deploy-*.yml playbooks
	if got := loaded.MatchPlaybookVenv("/ws/playbooks/deploy-archbtw.yml"); got != "/opt/venvs/deploy-all" {
		t.Fatalf("expected glob playbook venv, got %q", got)
	}

	// Exact host match beats group match; inventory host.Venv beats server_venvs
	dbHost := &models.Host{Name: "prod-db-01", Address: "10.0.2.20", Groups: []string{"production"}}
	webHost := &models.Host{Name: "prod-web-01", Address: "10.0.1.11", Groups: []string{"production"}}
	invPinned := &models.Host{Name: "prod-web-02", Address: "10.0.1.12", Groups: []string{"production"}, Venv: "/opt/venvs/inv-host"}

	if got := loaded.MatchServerVenv([]*models.Host{dbHost}, "prod-db-01"); got != "/opt/venvs/prod-db" {
		t.Fatalf("expected exact server venv, got %q", got)
	}
	if got := loaded.MatchServerVenv([]*models.Host{webHost}, "prod-web-01"); got != "/opt/venvs/prod-group" {
		t.Fatalf("expected group server venv, got %q", got)
	}
	if got := loaded.MatchServerVenv([]*models.Host{invPinned}, "prod-web-02"); got != "/opt/venvs/inv-host" {
		t.Fatalf("expected inventory duchess_venv to win, got %q", got)
	}
	if got := loaded.MatchServerRemoteVenv([]*models.Host{dbHost}, "prod-db-01"); got != "/srv/db/.venv" {
		t.Fatalf("expected server remote venv, got %q", got)
	}
}
