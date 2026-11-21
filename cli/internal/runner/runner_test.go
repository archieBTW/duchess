package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duchess-devops/duchess-cli/internal/sshclient"
)

func createFakeVenv(t *testing.T, root, name, scriptBody string) string {
	t.Helper()
	venvDir := filepath.Join(root, name)
	binDir := filepath.Join(venvDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "ansible-playbook")
	if err := os.WriteFile(bin, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	return venvDir
}

func TestResolvePlaybookVenvPrecedenceAndExecution(t *testing.T) {
	ws := t.TempDir()
	autoVenv := createFakeVenv(t, ws, ".venv", "#!/usr/bin/env bash\necho \"AUTO_VENV=$VIRTUAL_ENV ARGS=$*\"\n")
	explicitVenv := createFakeVenv(t, ws, "custom-venv", "#!/usr/bin/env bash\necho \"CUSTOM_VENV=$VIRTUAL_ENV\"\necho \"ARGS=$*\"\n")

	// 1. Auto-detect workspace .venv
	v, bin, isExp, err := ResolvePlaybookVenv("", "", "", ws)
	if err != nil || v != autoVenv || bin != filepath.Join(autoVenv, "bin", "ansible-playbook") || isExp {
		t.Fatalf("expected auto .venv, got v=%q bin=%q isExp=%v err=%v", v, bin, isExp, err)
	}

	// 2. Explicit venv overrides auto and header
	v, bin, isExp, err = ResolvePlaybookVenv(explicitVenv, autoVenv, "", ws)
	if err != nil || v != explicitVenv || !isExp {
		t.Fatalf("expected explicit venv, got v=%q bin=%q isExp=%v err=%v", v, bin, isExp, err)
	}

	// 2b. Layered precedence: PlaybookConfigVenv > ServerConfigVenv > ConfigVenv
	pbCfgVenv := createFakeVenv(t, ws, "pb-venv", "#!/usr/bin/env bash\necho pb\n")
	srvCfgVenv := createFakeVenv(t, ws, "srv-venv", "#!/usr/bin/env bash\necho srv\n")
	v, _, _, src, err := ResolvePlaybookVenvLayered(VenvLookup{
		PlaybookConfigVenv: pbCfgVenv,
		ServerConfigVenv:   srvCfgVenv,
		ConfigVenv:         autoVenv,
		WorkspaceDir:       ws,
	})
	if err != nil || v != pbCfgVenv || !strings.Contains(src, "playbook_venvs") {
		t.Fatalf("expected PlaybookConfigVenv to beat ServerConfigVenv, got v=%q src=%q err=%v", v, src, err)
	}
	v, _, _, src, err = ResolvePlaybookVenvLayered(VenvLookup{
		ServerConfigVenv: srvCfgVenv,
		ConfigVenv:       autoVenv,
		WorkspaceDir:     ws,
	})
	if err != nil || v != srvCfgVenv || !strings.Contains(src, "server_venvs") {
		t.Fatalf("expected ServerConfigVenv to beat ConfigVenv, got v=%q src=%q err=%v", v, src, err)
	}

	// 3. Explicit missing venv fails fast
	if _, _, _, err := ResolvePlaybookVenv(filepath.Join(ws, "nonexistent"), "", "", ws); err == nil {
		t.Fatalf("expected error for missing explicit venv")
	}

	// 4. Execute fake ansible-playbook inside custom venv with streaming + flags + SSH overrides
	pbFile := filepath.Join(ws, "test.yml")
	if err := os.WriteFile(pbFile, []byte("# duchess:venv: "+autoVenv+"\n# duchess:remote_venv: /opt/app/venv\n- hosts: all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	invFile := filepath.Join(ws, "hosts.ini")
	if err := os.WriteFile(invFile, []byte("[all]\napp-01 ansible_host=127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var streamed []string
	results, err := RunPlaybookWithOptions(
		context.Background(),
		invFile,
		pbFile,
		"app-01",
		nil,
		map[string]string{"env": "prod"},
		PlaybookOptions{
			Venv:         explicitVenv,
			WorkspaceDir: ws,
			CheckMode:    true,
			DiffMode:     true,
			Tags:         "deploy",
			Verbosity:    2,
			OnOutput: func(line string) {
				streamed = append(streamed, line)
			},
		},
		sshclient.Options{
			User:       "deploy",
			Port:       2222,
			PrivateKey: "/tmp/id_test",
			JumpHost:   "bastion.internal",
		},
	)
	if err != nil {
		t.Fatalf("RunPlaybookWithOptions failed: %v", err)
	}
	if len(results) != 1 || results[0].ExitCode != 0 {
		t.Fatalf("unexpected results: %+v", results)
	}
	out := results[0].Stdout
	for _, want := range []string{
		"CUSTOM_VENV=" + explicitVenv,
		"--check",
		"--diff",
		"--tags deploy",
		"-vv",
		"-u deploy",
		"--private-key /tmp/id_test",
		"ansible_port=2222",
		"--ssh-common-args=-J bastion.internal",
		"ansible_python_interpreter=/opt/app/venv/bin/python",
		"env=prod",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected stdout to contain %q, got:\n%s", want, out)
		}
	}
	if results[0].Venv != explicitVenv {
		t.Fatalf("expected result.Venv=%q, got %q", explicitVenv, results[0].Venv)
	}
	if len(streamed) < 2 {
		t.Fatalf("expected streamed lines, got %v", streamed)
	}
}

func TestParseVenvMetadata(t *testing.T) {
	ws := t.TempDir()
	pb := filepath.Join(ws, "deploy.yml")
	content := "---\n# duchess:name: Deploy\n# duchess:venv: ~/venvs/ans\n# duchess:remote_venv: /srv/.venv\n- hosts: all\n"
	if err := os.WriteFile(pb, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	item, err := ParseItemMetadata(pb, "playbook")
	if err != nil {
		t.Fatal(err)
	}
	if item.Venv != "~/venvs/ans" || item.RemoteVenv != "/srv/.venv" {
		t.Fatalf("unexpected venv metadata: venv=%q remote=%q", item.Venv, item.RemoteVenv)
	}
}

func TestBuildVenvEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin", "PYTHONHOME=/opt/old", "HOME=/home/test"}
	env := BuildVenvEnv(base, "/opt/venvs/ansible")
	var gotPath, gotVenv string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PYTHONHOME=") {
			t.Fatalf("PYTHONHOME should be stripped")
		}
		if strings.HasPrefix(kv, "PATH=") {
			gotPath = kv
		}
		if strings.HasPrefix(kv, "VIRTUAL_ENV=") {
			gotVenv = kv
		}
	}
	if gotVenv != "VIRTUAL_ENV=/opt/venvs/ansible" {
		t.Fatalf("unexpected VIRTUAL_ENV: %q", gotVenv)
	}
	if !strings.HasPrefix(gotPath, "PATH=/opt/venvs/ansible/bin:") {
		t.Fatalf("expected venv bin prepended to PATH, got %q", gotPath)
	}
}
