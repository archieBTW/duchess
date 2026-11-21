package config

import (
	"os"
	"path/filepath"
	"testing"
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
