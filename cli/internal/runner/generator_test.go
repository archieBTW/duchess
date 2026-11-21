package runner

import (
	"strings"
	"testing"
)

func TestGenerators(t *testing.T) {
	pb := GeneratePlaybookYAML(GeneratePlaybookOptions{
		Template: "nginx-proxy",
		Name:     "Proxy Portal",
		Hosts:    "webservers",
		Domain:   "portal.acme.dev",
		Upstream: "127.0.0.1:9000",
	})
	if !strings.Contains(pb, "portal.acme.dev") || !strings.Contains(pb, "nginx -t") {
		t.Fatalf("unexpected generated playbook:\n%s", pb)
	}

	sc := GenerateScriptContent(GenerateScriptOptions{
		Template:    "log-tail",
		Name:        "Tail API Logs",
		ServiceName: "api",
	})
	if !strings.Contains(sc, "journalctl -u") || !strings.Contains(sc, "api") {
		t.Fatalf("unexpected generated script:\n%s", sc)
	}
}
