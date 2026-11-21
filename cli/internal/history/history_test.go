package history

import (
	"testing"
	"time"
)

func TestAppendAndLoadRecent(t *testing.T) {
	dir := t.TempDir()
	e1 := Entry{
		Timestamp:  time.Now().Add(-2 * time.Minute),
		Title:      "deploy-nginx.yml",
		Kind:       "playbook",
		Target:     "webservers",
		Engine:     "ansible-playbook (.venv)",
		Venv:       "/opt/venvs/ansible",
		ExitCode:   0,
		DurationMs: 1200,
		Stdout:     "PLAY RECAP ok=4",
	}
	e2 := Entry{
		Timestamp:  time.Now(),
		Title:      "restart-service.yml",
		Kind:       "playbook",
		Target:     "prod-app-01",
		Engine:     "ansible-playbook",
		ExitCode:   2,
		DurationMs: 450,
		Error:      "unreachable",
	}

	if err := Append(dir, e1); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, e2); err != nil {
		t.Fatal(err)
	}

	entries, err := LoadRecent(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Title != "restart-service.yml" || entries[0].Succeeded() {
		t.Fatalf("expected newest failed entry first, got %+v", entries[0])
	}
	if entries[1].Venv != "/opt/venvs/ansible" || !entries[1].Succeeded() {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}

	limited, err := LoadRecent(dir, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("expected limit 1, got %d (err=%v)", len(limited), err)
	}
}
