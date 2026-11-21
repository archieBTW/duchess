package inventory

import (
	"path/filepath"
	"testing"
)

func TestParseINIAndResolve(t *testing.T) {
	sample := `
[production]
prod-app-01 ansible_host=127.0.0.1 ansible_user=ubuntu ansible_port=2222

[webservers]
prod-app-01 ansible_host=127.0.0.1

[all:vars]
ansible_python_interpreter=/usr/bin/python3
`
	inv, err := ParseINI(sample, "hosts.ini")
	if err != nil {
		t.Fatalf("ParseINI failed: %v", err)
	}
	h, ok := inv.Hosts["prod-app-01"]
	if !ok {
		t.Fatalf("expected host prod-app-01")
	}
	if h.Address != "127.0.0.1" || h.User != "ubuntu" || h.Port != 2222 {
		t.Fatalf("unexpected host fields: %+v", h)
	}
	if len(h.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %v", h.Groups)
	}
	byIP := ResolveTargets(inv, "127.0.0.1")
	if len(byIP) != 1 || byIP[0].Name != "prod-app-01" {
		t.Fatalf("expected resolve by IP to return prod-app-01, got %+v", byIP)
	}
	byGroup := ResolveTargets(inv, "webservers")
	if len(byGroup) != 1 {
		t.Fatalf("expected 1 host in webservers, got %d", len(byGroup))
	}
}

func TestParseYAMLAndSaveINI(t *testing.T) {
	yml := `
all:
  children:
    production:
      hosts:
        prod-app-01:
          ansible_host: 127.0.0.1
          ansible_user: root
          ansible_port: 22
`
	inv, err := ParseYAML(yml, "hosts.yml")
	if err != nil {
		t.Fatalf("ParseYAML failed: %v", err)
	}
	if len(inv.Hosts) != 1 || inv.Hosts["prod-app-01"].Address != "127.0.0.1" {
		t.Fatalf("unexpected yaml inventory: %+v", inv.Hosts)
	}
	outPath := filepath.Join(t.TempDir(), "saved.ini")
	if err := SaveINI(inv, outPath); err != nil {
		t.Fatalf("SaveINI failed: %v", err)
	}
	reloaded, err := Load(outPath)
	if err != nil {
		t.Fatalf("Load saved ini failed: %v", err)
	}
	if reloaded.Hosts["prod-app-01"].Address != "127.0.0.1" {
		t.Fatalf("round-trip failed: %+v", reloaded.Hosts["prod-app-01"])
	}
}
