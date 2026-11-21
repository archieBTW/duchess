package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/config"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
)

// StatsProbeScript collects OS, CPU, memory, disk, and network stats in one
// ultra-lightweight SSH round-trip using kernel /proc reads + local-only df.
const StatsProbeScript = `
echo "===DUCHESS:HOST==="
hostname -f 2>/dev/null || hostname
echo "===DUCHESS:OS==="
if [ -f /etc/os-release ]; then . /etc/os-release && echo "${PRETTY_NAME:-$NAME}"; else uname -s; fi
uname -r
uname -m
echo "===DUCHESS:UPTIME==="
cat /proc/uptime 2>/dev/null || echo "0 0"
echo "===DUCHESS:LOAD==="
cat /proc/loadavg 2>/dev/null || echo "0 0 0"
nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo 2>/dev/null || echo 1
echo "===DUCHESS:CPU==="
head -n 1 /proc/stat 2>/dev/null || true
sleep 0.15
head -n 1 /proc/stat 2>/dev/null || true
echo "===DUCHESS:MEM==="
cat /proc/meminfo 2>/dev/null || true
echo "===DUCHESS:DISK==="
df -B1 -Pl 2>/dev/null | grep -vE '^(tmpfs|devtmpfs|udev|overlay|squashfs|none|shm|Filesystem)' || true
echo "===DUCHESS:NET_IP==="
ip -o -4 addr show 2>/dev/null | awk '{print $2, $4}' || true
echo "===DUCHESS:NET_DEV==="
tail -n +3 /proc/net/dev 2>/dev/null || true
`

// DiscoveryProbeScript auto-detects containers (Docker/Podman), systemd services,
// listening TCP ports, and hosted websites/apps (Nginx, Apache, Tomcat, Caddy, HAProxy).
const DiscoveryProbeScript = StatsProbeScript + `
echo "===DUCHESS:DOCKER_PS==="
if command -v docker >/dev/null 2>&1; then
  docker ps -a --format '{{.ID}}|{{.Names}}|{{.Image}}|{{.State}}|{{.Status}}|{{.Ports}}' 2>/dev/null || true
fi
echo "===DUCHESS:DOCKER_STATS==="
if command -v docker >/dev/null 2>&1; then
  if command -v timeout >/dev/null 2>&1; then
    timeout 4 docker stats --no-stream --format '{{.ID}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}|{{.NetIO}}' 2>/dev/null || true
  else
    docker stats --no-stream --format '{{.ID}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}|{{.NetIO}}' 2>/dev/null || true
  fi
fi
echo "===DUCHESS:PODMAN_PS==="
if command -v podman >/dev/null 2>&1; then
  podman ps -a --format '{{.ID}}|{{.Names}}|{{.Image}}|{{.State}}|{{.Status}}|{{.Ports}}' 2>/dev/null || true
fi
echo "===DUCHESS:SYSTEMD==="
if command -v systemctl >/dev/null 2>&1; then
  systemctl list-units --type=service --all --no-pager --no-legend --plain 2>/dev/null | head -n 120 || true
fi
echo "===DUCHESS:PORTS==="
ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || true
echo "===DUCHESS:NGINX==="
if command -v nginx >/dev/null 2>&1 || [ -d /etc/nginx ]; then
  for f in /etc/nginx/sites-enabled/* /etc/nginx/conf.d/*.conf; do
    if [ -f "$f" ]; then
      echo "---FILE:$f---"
      cat "$f" 2>/dev/null || true
    fi
  done
fi
echo "===DUCHESS:APACHE==="
for f in /etc/apache2/sites-enabled/*.conf /etc/httpd/conf.d/*.conf /etc/httpd/sites-enabled/*.conf; do
  if [ -f "$f" ]; then
    echo "---FILE:$f---"
    cat "$f" 2>/dev/null || true
  fi
done
echo "===DUCHESS:TOMCAT==="
pgrep -af catalina 2>/dev/null || true
for d in /opt/tomcat* /var/lib/tomcat* /usr/share/tomcat* /srv/tomcat* /home/*/tomcat*; do
  if [ -d "$d/webapps" ]; then
    echo "---TOMCAT_DIR:$d---"
    ls -1 "$d/webapps" 2>/dev/null || true
    if [ -f "$d/conf/server.xml" ]; then
      echo "---SERVER_XML---"
      grep -E '<Connector ' "$d/conf/server.xml" 2>/dev/null || true
    fi
  fi
done
echo "===DUCHESS:CADDY==="
if [ -f /etc/caddy/Caddyfile ]; then
  echo "---FILE:/etc/caddy/Caddyfile---"
  cat /etc/caddy/Caddyfile 2>/dev/null || true
fi
echo "===DUCHESS:HAPROXY==="
if [ -f /etc/haproxy/haproxy.cfg ]; then
  echo "---FILE:/etc/haproxy/haproxy.cfg---"
  cat /etc/haproxy/haproxy.cfg 2>/dev/null || true
fi
echo "===DUCHESS:TLS_CERTS==="
if command -v openssl >/dev/null 2>&1; then
  certs=$(grep -hERo 'ssl_certificate[[:space:]]+[^;]+;|SSLCertificateFile[[:space:]]+[^[:space:]]+' /etc/nginx/sites-enabled /etc/nginx/conf.d /etc/apache2/sites-enabled /etc/httpd/conf.d 2>/dev/null | awk '{print $2}' | tr -d '";'\''' | sort -u | head -n 20)
  for c in $certs; do
    if [ -r "$c" ]; then
      end=$(openssl x509 -noout -enddate -in "$c" 2>/dev/null | cut -d= -f2)
      [ -n "$end" ] && echo "$c|$end"
    fi
  done
fi
`

// CollectStats gathers live server metrics over SSH.
func CollectStats(ctx context.Context, client *sshclient.Client, host *models.Host) *models.ServerStats {
	stdout, stderr, _, err := client.Run(ctx, StatsProbeScript)
	if err != nil {
		return &models.ServerStats{
			HostName:    host.Name,
			Address:     host.Address,
			CollectedAt: time.Now(),
			Error:       fmt.Sprintf("%v %s", err, strings.TrimSpace(stderr)),
		}
	}
	return ParseStatsOutput(host, stdout)
}

// Discover gathers server stats plus auto-detected containers, services, ports, and hosted web apps.
func Discover(ctx context.Context, client *sshclient.Client, host *models.Host) *models.DiscoveryReport {
	stdout, stderr, _, err := client.Run(ctx, DiscoveryProbeScript)
	if err != nil {
		return &models.DiscoveryReport{
			HostName:    host.Name,
			Address:     host.Address,
			CollectedAt: time.Now(),
			Error:       fmt.Sprintf("%v %s", err, strings.TrimSpace(stderr)),
		}
	}
	return ParseDiscoveryOutput(host, stdout)
}

// ParseStatsOutput parses the delimited output of StatsProbeScript.
func ParseStatsOutput(host *models.Host, raw string) *models.ServerStats {
	sections := splitSections(raw)
	st := &models.ServerStats{
		HostName:    host.Name,
		Address:     host.Address,
		CollectedAt: time.Now(),
	}

	if lines := nonEmptyLines(sections["HOST"]); len(lines) > 0 {
		st.HostnameFQDN = lines[0]
	}
	if lines := nonEmptyLines(sections["OS"]); len(lines) > 0 {
		st.OS = lines[0]
		if len(lines) > 1 {
			st.Kernel = lines[1]
		}
		if len(lines) > 2 {
			st.Arch = lines[2]
		}
	}
	if lines := nonEmptyLines(sections["UPTIME"]); len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) > 0 {
			if sec, err := strconv.ParseFloat(fields[0], 64); err == nil {
				st.UptimeSeconds = sec
				st.UptimeHuman = formatUptime(sec)
			}
		}
	}
	if lines := nonEmptyLines(sections["LOAD"]); len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) >= 3 {
			st.Load1, _ = strconv.ParseFloat(fields[0], 64)
			st.Load5, _ = strconv.ParseFloat(fields[1], 64)
			st.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
		if len(lines) > 1 {
			st.CPUCores, _ = strconv.Atoi(strings.TrimSpace(lines[1]))
		}
	}
	st.CPUUsagePercent = parseCPUUsage(sections["CPU"])
	parseMemInfo(sections["MEM"], st)
	st.Disks = parseDisks(sections["DISK"])
	st.Interfaces = parseNetInterfaces(sections["NET_IP"], sections["NET_DEV"])
	return st
}

// ParseDiscoveryOutput parses full discovery output including containers, services, ports, and web apps.
func ParseDiscoveryOutput(host *models.Host, raw string) *models.DiscoveryReport {
	sections := splitSections(raw)
	report := &models.DiscoveryReport{
		HostName:    host.Name,
		Address:     host.Address,
		Stats:       ParseStatsOutput(host, raw),
		CollectedAt: time.Now(),
	}

	// Containers: Docker + Podman
	statsMap := parseDockerStats(sections["DOCKER_STATS"])
	report.Containers = append(report.Containers, parseContainers(sections["DOCKER_PS"], "docker", statsMap)...)
	report.Containers = append(report.Containers, parseContainers(sections["PODMAN_PS"], "podman", nil)...)

	// Listening ports
	report.ListeningPorts = parseListeningPorts(sections["PORTS"])

	// Systemd services
	report.Services = parseSystemdServices(sections["SYSTEMD"], report.ListeningPorts)

	// Hosted websites / web apps
	report.WebApps = append(report.WebApps, parseNginxConfigs(sections["NGINX"])...)
	report.WebApps = append(report.WebApps, parseApacheConfigs(sections["APACHE"])...)
	report.WebApps = append(report.WebApps, parseTomcat(sections["TOMCAT"])...)
	report.WebApps = append(report.WebApps, parseCaddy(sections["CADDY"])...)
	report.WebApps = append(report.WebApps, parseHAProxy(sections["HAPROXY"])...)
	report.WebApps = append(report.WebApps, inferHTTPAppsFromPorts(report.ListeningPorts, report.WebApps)...)

	// Attach TLS certificate expiry days if probed
	attachCertExpiry(report.WebApps, sections["TLS_CERTS"])

	// Evaluate proactive health alerts using default thresholds
	report.Alerts = EvaluateAlerts(report, config.Default().Alerts)

	return report
}

func splitSections(raw string) map[string]string {
	res := make(map[string]string)
	var curKey string
	var buf strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "===DUCHESS:") && strings.HasSuffix(trimmed, "===") {
			if curKey != "" {
				res[curKey] = buf.String()
			}
			curKey = strings.TrimSuffix(strings.TrimPrefix(trimmed, "===DUCHESS:"), "===")
			buf.Reset()
			continue
		}
		if curKey != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	if curKey != "" {
		res[curKey] = buf.String()
	}
	return res
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func formatUptime(sec float64) string {
	total := int64(sec)
	days := total / 86400
	hours := (total % 86400) / 3600
	mins := (total % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func parseCPUUsage(raw string) float64 {
	lines := nonEmptyLines(raw)
	if len(lines) < 2 {
		return 0
	}
	t1, i1 := parseCPUStatLine(lines[0])
	t2, i2 := parseCPUStatLine(lines[1])
	dt := t2 - t1
	di := i2 - i1
	if dt <= 0 {
		return 0
	}
	pct := float64(dt-di) / float64(dt) * 100.0
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return float64(int(pct*10)) / 10
}

func parseCPUStatLine(line string) (total uint64, idle uint64) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, 0
	}
	for i := 1; i < len(fields); i++ {
		v, _ := strconv.ParseUint(fields[i], 10, 64)
		total += v
		if i == 4 || i == 5 { // idle + iowait
			idle += v
		}
	}
	return total, idle
}

func parseMemInfo(raw string, st *models.ServerStats) {
	kv := make(map[string]uint64)
	for _, line := range nonEmptyLines(raw) {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err == nil {
			kv[strings.TrimSpace(parts[0])] = v * 1024 // KiB -> bytes
		}
	}
	st.MemTotalBytes = kv["MemTotal"]
	if avail, ok := kv["MemAvailable"]; ok {
		st.MemAvailBytes = avail
	} else {
		st.MemAvailBytes = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	if st.MemTotalBytes >= st.MemAvailBytes {
		st.MemUsedBytes = st.MemTotalBytes - st.MemAvailBytes
	}
	if st.MemTotalBytes > 0 {
		st.MemUsagePercent = float64(int(float64(st.MemUsedBytes)/float64(st.MemTotalBytes)*1000)) / 10
	}
	st.SwapTotalBytes = kv["SwapTotal"]
	if st.SwapTotalBytes >= kv["SwapFree"] {
		st.SwapUsedBytes = st.SwapTotalBytes - kv["SwapFree"]
	}
}

func parseDisks(raw string) []models.DiskUsage {
	var disks []models.DiskUsage
	seenMount := make(map[string]bool)
	for _, line := range nonEmptyLines(raw) {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		mount := fields[5]
		if seenMount[mount] {
			continue
		}
		seenMount[mount] = true
		total, _ := strconv.ParseUint(fields[1], 10, 64)
		used, _ := strconv.ParseUint(fields[2], 10, 64)
		avail, _ := strconv.ParseUint(fields[3], 10, 64)
		pctStr := strings.TrimSuffix(fields[4], "%")
		pct, _ := strconv.ParseFloat(pctStr, 64)
		disks = append(disks, models.DiskUsage{
			Filesystem: fields[0],
			MountPoint: mount,
			TotalBytes: total,
			UsedBytes:  used,
			AvailBytes: avail,
			UsePercent: pct,
		})
	}
	return disks
}

func parseNetInterfaces(ipRaw, devRaw string) []models.NetInterface {
	ips := make(map[string]string)
	for _, line := range nonEmptyLines(ipRaw) {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			ips[fields[0]] = strings.Split(fields[1], "/")[0]
		}
	}
	var out []models.NetInterface
	for _, line := range nonEmptyLines(devRaw) {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		out = append(out, models.NetInterface{
			Name:    name,
			IPv4:    ips[name],
			RxBytes: rx,
			TxBytes: tx,
		})
	}
	return out
}

type containerStat struct {
	cpu, mem, memPerc, netIO string
}

func parseDockerStats(raw string) map[string]containerStat {
	res := make(map[string]containerStat)
	for _, line := range nonEmptyLines(raw) {
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		st := containerStat{
			cpu:     strings.TrimSpace(parts[1]),
			mem:     strings.TrimSpace(parts[2]),
			memPerc: strings.TrimSpace(parts[3]),
			netIO:   strings.TrimSpace(parts[4]),
		}
		res[id] = st
		if len(id) >= 12 {
			res[id[:12]] = st
		}
	}
	return res
}

func parseContainers(raw, runtime string, statsMap map[string]containerStat) []models.ContainerInfo {
	var out []models.ContainerInfo
	for _, line := range nonEmptyLines(raw) {
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		ports := ""
		if len(parts) >= 6 {
			ports = strings.TrimSpace(parts[5])
		}
		c := models.ContainerInfo{
			ID:      id,
			Name:    strings.TrimSpace(parts[1]),
			Image:   strings.TrimSpace(parts[2]),
			Runtime: runtime,
			State:   strings.TrimSpace(parts[3]),
			Status:  strings.TrimSpace(parts[4]),
			Ports:   ports,
		}
		if statsMap != nil {
			if st, ok := statsMap[id]; ok {
				c.CPUPerc, c.MemUsage, c.MemPerc, c.NetIO = st.cpu, st.mem, st.memPerc, st.netIO
			} else if len(id) >= 12 {
				if st, ok := statsMap[id[:12]]; ok {
					c.CPUPerc, c.MemUsage, c.MemPerc, c.NetIO = st.cpu, st.mem, st.memPerc, st.netIO
				}
			}
		}
		out = append(out, c)
	}
	return out
}

var procNameRe = regexp.MustCompile(`"([^"]+)"`)

func parseListeningPorts(raw string) []models.ListeningPort {
	var out []models.ListeningPort
	seen := make(map[string]bool)
	for _, line := range nonEmptyLines(raw) {
		if strings.HasPrefix(line, "State") || strings.HasPrefix(line, "Proto") || strings.HasPrefix(line, "Active") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		localAddr := fields[3]
		if strings.HasPrefix(fields[0], "tcp") {
			localAddr = fields[3]
		}
		idx := strings.LastIndex(localAddr, ":")
		if idx < 0 {
			continue
		}
		addr := localAddr[:idx]
		port := localAddr[idx+1:]
		proc := ""
		if m := procNameRe.FindStringSubmatch(line); len(m) > 1 {
			proc = m[1]
		} else if len(fields) >= 7 {
			proc = fields[len(fields)-1]
		}
		key := port + "|" + proc
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, models.ListeningPort{
			Protocol: "tcp",
			Address:  addr,
			Port:     port,
			Process:  proc,
		})
	}
	return out
}

func parseSystemdServices(raw string, ports []models.ListeningPort) []models.ServiceInfo {
	var out []models.ServiceInfo
	portByProc := make(map[string][]string)
	for _, p := range ports {
		if p.Process != "" {
			key := strings.ToLower(p.Process)
			portByProc[key] = append(portByProc[key], p.Port)
		}
	}

	for _, line := range nonEmptyLines(raw) {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := strings.TrimSuffix(fields[0], ".service")
		loadState := fields[1]
		activeState := fields[2]
		subState := fields[3]
		desc := ""
		if len(fields) > 4 {
			desc = strings.Join(fields[4:], " ")
		}
		if activeState != "active" && activeState != "failed" {
			continue
		}
		var matchedPorts []string
		lower := strings.ToLower(unit)
		for proc, pList := range portByProc {
			if strings.Contains(lower, proc) || strings.Contains(proc, lower) {
				matchedPorts = append(matchedPorts, pList...)
			}
		}
		out = append(out, models.ServiceInfo{
			Name:        unit,
			LoadState:   loadState,
			ActiveState: activeState,
			SubState:    subState,
			Description: desc,
			Manager:     "systemd",
			Ports:       dedupStrings(matchedPorts),
		})
	}
	return out
}

func parseNginxConfigs(raw string) []models.WebAppInfo {
	var apps []models.WebAppInfo
	files := splitFileBlocks(raw, "---FILE:", "---")
	listenRe := regexp.MustCompile(`(?m)^\s*listen\s+([^;]+);`)
	serverNameRe := regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)
	rootRe := regexp.MustCompile(`(?m)^\s*root\s+([^;]+);`)
	proxyRe := regexp.MustCompile(`(?m)^\s*proxy_pass\s+([^;]+);`)
	certRe := regexp.MustCompile(`(?m)^\s*ssl_certificate\s+([^;]+);`)

	for filePath, content := range files {
		blocks := extractBraceBlocks(content, "server")
		if len(blocks) == 0 && strings.Contains(content, "server_name") {
			blocks = []string{content}
		}
		for i, blk := range blocks {
			var domains, ports []string
			ssl := false
			for _, m := range listenRe.FindAllStringSubmatch(blk, -1) {
				val := strings.TrimSpace(m[1])
				if strings.Contains(val, "ssl") || strings.Contains(val, "443") {
					ssl = true
				}
				tok := strings.Fields(val)[0]
				if idx := strings.LastIndex(tok, ":"); idx >= 0 {
					tok = tok[idx+1:]
				}
				ports = append(ports, tok)
			}
			for _, m := range serverNameRe.FindAllStringSubmatch(blk, -1) {
				for _, d := range strings.Fields(m[1]) {
					if d != "_" && d != "" {
						domains = append(domains, d)
					}
				}
			}
			rootPath := ""
			if m := rootRe.FindStringSubmatch(blk); len(m) > 1 {
				rootPath = strings.TrimSpace(m[1])
			}
			proxyTarget := ""
			if m := proxyRe.FindStringSubmatch(blk); len(m) > 1 {
				proxyTarget = strings.TrimSpace(m[1])
			}
			certPath := ""
			if m := certRe.FindStringSubmatch(blk); len(m) > 1 {
				certPath = strings.Trim(strings.TrimSpace(m[1]), `"'`)
			}
			if len(domains) == 0 && len(ports) == 0 && rootPath == "" && proxyTarget == "" {
				continue
			}
			name := filepath.Base(filePath)
			if len(domains) > 0 {
				name = domains[0]
			} else if i > 0 {
				name = fmt.Sprintf("%s#%d", name, i+1)
			}
			if len(domains) == 0 {
				domains = []string{"default"}
			}
			apps = append(apps, models.WebAppInfo{
				Name:        name,
				ServerType:  "nginx",
				Domains:     dedupStrings(domains),
				Ports:       dedupStrings(ports),
				SSL:         ssl,
				CertPath:    certPath,
				RootOrPath:  rootPath,
				ProxyTarget: proxyTarget,
				ConfigPath:  filePath,
				Status:      "configured",
			})
		}
	}
	return apps
}

func parseApacheConfigs(raw string) []models.WebAppInfo {
	var apps []models.WebAppInfo
	files := splitFileBlocks(raw, "---FILE:", "---")
	vhRe := regexp.MustCompile(`(?is)<VirtualHost\s+([^>]+)>(.*?)</VirtualHost>`)
	snRe := regexp.MustCompile(`(?im)^\s*ServerName\s+(\S+)`)
	saRe := regexp.MustCompile(`(?im)^\s*ServerAlias\s+(.+)$`)
	docRe := regexp.MustCompile(`(?im)^\s*DocumentRoot\s+"?([^"\s]+)"?`)
	proxyRe := regexp.MustCompile(`(?im)^\s*ProxyPass\s+\S+\s+(\S+)`)
	sslRe := regexp.MustCompile(`(?im)^\s*SSLEngine\s+on`)
	certRe := regexp.MustCompile(`(?im)^\s*SSLCertificateFile\s+"?([^"\s]+)"?`)

	for filePath, content := range files {
		for _, m := range vhRe.FindAllStringSubmatch(content, -1) {
			addrSpec := strings.TrimSpace(m[1])
			body := m[2]
			var ports []string
			for _, part := range strings.Fields(addrSpec) {
				if idx := strings.LastIndex(part, ":"); idx >= 0 {
					ports = append(ports, part[idx+1:])
				}
			}
			var domains []string
			if sm := snRe.FindStringSubmatch(body); len(sm) > 1 {
				domains = append(domains, strings.TrimSpace(sm[1]))
			}
			for _, am := range saRe.FindAllStringSubmatch(body, -1) {
				domains = append(domains, strings.Fields(am[1])...)
			}
			docRoot := ""
			if dm := docRe.FindStringSubmatch(body); len(dm) > 1 {
				docRoot = strings.TrimSpace(dm[1])
			}
			proxyTarget := ""
			if pm := proxyRe.FindStringSubmatch(body); len(pm) > 1 {
				proxyTarget = strings.TrimSpace(pm[1])
			}
			ssl := sslRe.MatchString(body) || containsStrSlice(ports, "443")
			certPath := ""
			if cm := certRe.FindStringSubmatch(body); len(cm) > 1 {
				certPath = strings.TrimSpace(cm[1])
			}
			name := filepath.Base(filePath)
			if len(domains) > 0 {
				name = domains[0]
			} else {
				domains = []string{"default"}
			}
			apps = append(apps, models.WebAppInfo{
				Name:        name,
				ServerType:  "apache",
				Domains:     dedupStrings(domains),
				Ports:       dedupStrings(ports),
				SSL:         ssl,
				CertPath:    certPath,
				RootOrPath:  docRoot,
				ProxyTarget: proxyTarget,
				ConfigPath:  filePath,
				Status:      "configured",
			})
		}
	}
	return apps
}

func parseTomcat(raw string) []models.WebAppInfo {
	var apps []models.WebAppInfo
	blocks := splitFileBlocks(raw, "---TOMCAT_DIR:", "---")
	portRe := regexp.MustCompile(`port="(\d+)"`)

	for dir, content := range blocks {
		parts := strings.SplitN(content, "---SERVER_XML---", 2)
		webappsRaw := parts[0]
		ports := []string{"8080"}
		if len(parts) == 2 {
			var found []string
			for _, m := range portRe.FindAllStringSubmatch(parts[1], -1) {
				found = append(found, m[1])
			}
			if len(found) > 0 {
				ports = dedupStrings(found)
			}
		}
		seen := make(map[string]bool)
		for _, entry := range nonEmptyLines(webappsRaw) {
			appName := strings.TrimSuffix(entry, ".war")
			if appName == "" || seen[appName] {
				continue
			}
			seen[appName] = true
			ctxPath := "/" + appName
			if appName == "ROOT" {
				ctxPath = "/"
			}
			apps = append(apps, models.WebAppInfo{
				Name:       fmt.Sprintf("tomcat:%s", appName),
				ServerType: "tomcat",
				Domains:    []string{ctxPath},
				Ports:      ports,
				SSL:        containsStrSlice(ports, "8443") || containsStrSlice(ports, "443"),
				RootOrPath: filepath.Join(dir, "webapps", entry),
				ConfigPath: filepath.Join(dir, "conf", "server.xml"),
				Status:     "deployed",
			})
		}
	}
	return apps
}

func parseCaddy(raw string) []models.WebAppInfo {
	var apps []models.WebAppInfo
	files := splitFileBlocks(raw, "---FILE:", "---")
	siteRe := regexp.MustCompile(`(?m)^([a-zA-Z0-9._:/\-, *]+)\s*\{`)
	proxyRe := regexp.MustCompile(`(?m)^\s*reverse_proxy\s+([^\n]+)`)
	for filePath, content := range files {
		for _, m := range siteRe.FindAllStringSubmatch(content, -1) {
			site := strings.TrimSpace(m[1])
			if site == "" || strings.HasPrefix(site, "#") {
				continue
			}
			proxy := ""
			if pm := proxyRe.FindStringSubmatch(content); len(pm) > 1 {
				proxy = strings.TrimSpace(pm[1])
			}
			apps = append(apps, models.WebAppInfo{
				Name:        site,
				ServerType:  "caddy",
				Domains:     []string{site},
				Ports:       []string{"80", "443"},
				SSL:         !strings.HasPrefix(site, "http://"),
				ProxyTarget: proxy,
				ConfigPath:  filePath,
				Status:      "configured",
			})
		}
	}
	return apps
}

func parseHAProxy(raw string) []models.WebAppInfo {
	var apps []models.WebAppInfo
	files := splitFileBlocks(raw, "---FILE:", "---")
	feRe := regexp.MustCompile(`(?m)^\s*frontend\s+(\S+)`)
	bindRe := regexp.MustCompile(`(?m)^\s*bind\s+\S*:(\d+)`)
	for filePath, content := range files {
		frontends := feRe.FindAllStringSubmatch(content, -1)
		var ports []string
		for _, bm := range bindRe.FindAllStringSubmatch(content, -1) {
			ports = append(ports, bm[1])
		}
		for _, fm := range frontends {
			apps = append(apps, models.WebAppInfo{
				Name:       fm[1],
				ServerType: "haproxy",
				Domains:    []string{fm[1]},
				Ports:      dedupStrings(ports),
				SSL:        containsStrSlice(ports, "443"),
				ConfigPath: filePath,
				Status:     "configured",
			})
		}
	}
	return apps
}

func inferHTTPAppsFromPorts(ports []models.ListeningPort, existing []models.WebAppInfo) []models.WebAppInfo {
	knownPorts := make(map[string]bool)
	for _, a := range existing {
		for _, p := range a.Ports {
			knownPorts[p] = true
		}
	}
	webProcs := map[string]bool{
		"node": true, "python": true, "python3": true, "gunicorn": true,
		"uvicorn": true, "java": true, "ruby": true, "puma": true,
		"traefik": true, "envoy": true, "caddy": true, "nginx": true,
		"apache2": true, "httpd": true, "dotnet": true,
	}
	var extra []models.WebAppInfo
	for _, p := range ports {
		if knownPorts[p.Port] {
			continue
		}
		proc := strings.ToLower(p.Process)
		isWebPort := p.Port == "80" || p.Port == "443" || p.Port == "3000" || p.Port == "5000" || p.Port == "8000" || p.Port == "8080" || p.Port == "8443" || p.Port == "9000"
		if webProcs[proc] || isWebPort {
			if proc == "" {
				proc = "http-listener"
			}
			extra = append(extra, models.WebAppInfo{
				Name:       fmt.Sprintf("%s (:%s)", proc, p.Port),
				ServerType: "http",
				Domains:    []string{fmt.Sprintf("*:%s", p.Port)},
				Ports:      []string{p.Port},
				SSL:        p.Port == "443" || p.Port == "8443",
				Status:     "listening",
			})
		}
	}
	return extra
}

func splitFileBlocks(raw, prefix, suffix string) map[string]string {
	res := make(map[string]string)
	var curFile string
	var buf strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, prefix) && strings.HasSuffix(t, suffix) {
			if curFile != "" {
				res[curFile] = buf.String()
			}
			curFile = strings.TrimSuffix(strings.TrimPrefix(t, prefix), suffix)
			buf.Reset()
			continue
		}
		if curFile != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	if curFile != "" {
		res[curFile] = buf.String()
	}
	return res
}

func extractBraceBlocks(content, keyword string) []string {
	var blocks []string
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(keyword) + `\s*\{`)
	locs := re.FindAllStringIndex(content, -1)
	for _, loc := range locs {
		depth := 0
		start := loc[1] - 1
		for i := start; i < len(content); i++ {
			if content[i] == '{' {
				depth++
			} else if content[i] == '}' {
				depth--
				if depth == 0 {
					blocks = append(blocks, content[start+1:i])
					break
				}
			}
		}
	}
	return blocks
}

func dedupStrings(in []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func containsStrSlice(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func attachCertExpiry(apps []models.WebAppInfo, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	expiryByPath := make(map[string]int)
	now := time.Now()
	layouts := []string{
		"Jan _2 15:04:05 2006 MST",
		"Jan  2 15:04:05 2006 MST",
		time.RFC1123,
		time.RFC3339,
	}
	for _, line := range nonEmptyLines(raw) {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		certPath := strings.TrimSpace(parts[0])
		dateStr := strings.TrimSpace(parts[1])
		for _, layout := range layouts {
			if t, err := time.Parse(layout, dateStr); err == nil {
				days := int(t.Sub(now).Hours() / 24)
				expiryByPath[certPath] = days
				break
			}
		}
	}
	for i := range apps {
		if apps[i].CertPath == "" {
			continue
		}
		if days, ok := expiryByPath[apps[i].CertPath]; ok {
			d := days
			apps[i].CertExpiryDays = &d
		}
	}
}
