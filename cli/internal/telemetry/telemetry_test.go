package telemetry

import (
	"testing"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/config"
	"github.com/duchess-devops/duchess-cli/internal/models"
)

func TestParseDiscoveryOutput(t *testing.T) {
	sample := `===DUCHESS:HOST===
prod-app-01.internal
===DUCHESS:OS===
Ubuntu 24.04.1 LTS
6.8.0-45-generic
x86_64
===DUCHESS:UPTIME===
172860.50 300000.00
===DUCHESS:LOAD===
0.42 0.35 0.28 1/312 12345
4
===DUCHESS:CPU===
cpu  1000 0 500 8500 0 0 0 0 0 0
cpu  1100 0 550 8850 0 0 0 0 0 0
===DUCHESS:MEM===
MemTotal:        8192000 kB
MemFree:         2048000 kB
MemAvailable:    4096000 kB
SwapTotal:       2048000 kB
SwapFree:        1536000 kB
===DUCHESS:DISK===
/dev/sda1 107374182400 42949672960 64424509440 40% /
===DUCHESS:NET_IP===
eth0 127.0.0.1/32
===DUCHESS:NET_DEV===
  eth0: 1048576 100 0 0 0 0 0 0 2097152 200 0 0 0 0 0 0
===DUCHESS:DOCKER_PS===
a1b2c3d4e5f6|api-gateway|ghcr.io/acme/api:1.4|running|Up 2 days|0.0.0.0:8080->8080/tcp
===DUCHESS:DOCKER_STATS===
a1b2c3d4e5f6|3.45%|128MiB / 7.8GiB|1.60%|12MB / 8MB
===DUCHESS:PODMAN_PS===
===DUCHESS:SYSTEMD===
nginx.service loaded active running A high performance web server
postgresql.service loaded active running PostgreSQL RDBMS
===DUCHESS:PORTS===
LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=900,fd=6))
LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("nginx",pid=900,fd=7))
LISTEN 0 128 127.0.0.1:8080 0.0.0.0:* users:(("java",pid=1200,fd=51))
===DUCHESS:NGINX===
---FILE:/etc/nginx/sites-enabled/portal.conf---
server {
    listen 443 ssl;
    server_name portal.example.com www.portal.example.com;
    proxy_pass http://127.0.0.1:8080;
}
===DUCHESS:APACHE===
---FILE:/etc/apache2/sites-enabled/legacy.conf---
<VirtualHost *:8081>
    ServerName legacy.example.com
    DocumentRoot /var/www/legacy
</VirtualHost>
===DUCHESS:TOMCAT===
---TOMCAT_DIR:/opt/tomcat9---
ROOT
billing.war
---SERVER_XML---
<Connector port="8080" protocol="HTTP/1.1" />
===DUCHESS:CADDY===
===DUCHESS:HAPROXY===
`
	host := &models.Host{Name: "prod-app-01", Address: "127.0.0.1"}
	rep := ParseDiscoveryOutput(host, sample)

	if rep.Stats.OS != "Ubuntu 24.04.1 LTS" || rep.Stats.CPUCores != 4 {
		t.Fatalf("unexpected stats: %+v", rep.Stats)
	}
	if rep.Stats.CPUUsagePercent != 30.0 {
		t.Fatalf("expected 30%% CPU, got %.1f", rep.Stats.CPUUsagePercent)
	}
	if rep.Stats.MemUsagePercent != 50.0 {
		t.Fatalf("expected 50%% Mem, got %.1f", rep.Stats.MemUsagePercent)
	}
	if len(rep.Containers) != 1 || rep.Containers[0].CPUPerc != "3.45%" {
		t.Fatalf("unexpected containers: %+v", rep.Containers)
	}
	if len(rep.Services) != 2 {
		t.Fatalf("expected 2 systemd services, got %d", len(rep.Services))
	}
	// Expect Nginx (1) + Apache (1) + Tomcat (2: ROOT, billing)
	if len(rep.WebApps) < 4 {
		t.Fatalf("expected >=4 web apps (nginx, apache, 2 tomcat), got %d: %+v", len(rep.WebApps), rep.WebApps)
	}
}

func TestEvaluateAlertsAndTLSExpiry(t *testing.T) {
	soon := time.Now().UTC().Add(5 * 24 * time.Hour).Format("Jan _2 15:04:05 2006 GMT")
	sample := `===DUCHESS:HOST===
alert-node
===DUCHESS:DISK===
/dev/sda1 100000 96000 4000 96% /
===DUCHESS:MEM===
MemTotal: 1000 kB
MemAvailable: 50 kB
===DUCHESS:SYSTEMD===
worker.service loaded failed failed Background Worker
===DUCHESS:DOCKER_PS===
abc|api|img|exited|Exited (137) 2 minutes ago|
===DUCHESS:NGINX===
---FILE:/etc/nginx/sites-enabled/site.conf---
server {
    listen 443 ssl;
    server_name secure.example.com;
    ssl_certificate /etc/letsencrypt/live/secure.example.com/fullchain.pem;
}
===DUCHESS:TLS_CERTS===
/etc/letsencrypt/live/secure.example.com/fullchain.pem|` + soon + "\n"

	rep := ParseDiscoveryOutput(&models.Host{Name: "alert-node"}, sample)
	alerts := EvaluateAlerts(rep, config.Default().Alerts)
	if len(alerts) < 4 {
		t.Fatalf("expected >=4 alerts (disk, mem, failed service, container, tls), got %+v", alerts)
	}
	foundCat := map[string]bool{}
	for _, a := range alerts {
		foundCat[a.Category] = true
	}
	for _, want := range []string{"disk", "memory", "service", "container", "tls"} {
		if !foundCat[want] {
			t.Fatalf("expected %q alert in %+v", want, alerts)
		}
	}
}
