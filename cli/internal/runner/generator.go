package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GeneratePlaybookOptions configures the Ansible playbook generator.
type GeneratePlaybookOptions struct {
	Template    string // nginx-proxy | docker-deploy | systemd-service | firewall-ufw | package-install | health-check | custom
	Name        string
	Description string
	Hosts       string
	Become      bool
	ServiceName string
	Domain      string
	Upstream    string
	Packages    []string
	CustomCmd   string
	Venv        string
	RemoteVenv  string
}

// PlaybookTemplates returns available playbook generator template IDs.
func PlaybookTemplates() []string {
	return []string{
		"nginx-proxy",
		"docker-deploy",
		"systemd-service",
		"firewall-ufw",
		"package-install",
		"health-check",
		"custom",
	}
}

// GeneratePlaybookYAML creates a ready-to-run Ansible playbook from options.
func GeneratePlaybookYAML(opts GeneratePlaybookOptions) string {
	if opts.Name == "" {
		opts.Name = "Generated Playbook"
	}
	if opts.Hosts == "" {
		opts.Hosts = "all"
	}
	if opts.Description == "" {
		opts.Description = fmt.Sprintf("Generated %s playbook via Duchess", opts.Template)
	}
	becomeStr := "false"
	if opts.Become {
		becomeStr = "true"
	}

	header := fmt.Sprintf(`---
# duchess:name: %s
# duchess:description: %s
# duchess:category: %s
`, opts.Name, opts.Description, opts.Template)
	if strings.TrimSpace(opts.Venv) != "" {
		header += fmt.Sprintf("# duchess:venv: %s\n", strings.TrimSpace(opts.Venv))
	}
	if strings.TrimSpace(opts.RemoteVenv) != "" {
		header += fmt.Sprintf("# duchess:remote_venv: %s\n", strings.TrimSpace(opts.RemoteVenv))
	}

	switch opts.Template {
	case "nginx-proxy":
		domain := fallback(opts.Domain, "app.example.com")
		upstream := fallback(opts.Upstream, "127.0.0.1:8080")
		return header + fmt.Sprintf(`- name: %s
  hosts: %s
  become: true
  vars:
    site_domain: %s
    upstream_addr: %s
  tasks:
    - name: Ensure Nginx is installed
      ansible.builtin.package:
        name: nginx
        state: present

    - name: Configure reverse proxy for {{ site_domain }}
      ansible.builtin.copy:
        dest: "/etc/nginx/sites-available/{{ site_domain }}.conf"
        mode: "0644"
        content: |
          server {
              listen 80;
              server_name {{ site_domain }};
              location / {
                  proxy_pass http://{{ upstream_addr }};
                  proxy_set_header Host $host;
                  proxy_set_header X-Real-IP $remote_addr;
                  proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
              }
          }

    - name: Enable site configuration
      ansible.builtin.file:
        src: "/etc/nginx/sites-available/{{ site_domain }}.conf"
        dest: "/etc/nginx/sites-enabled/{{ site_domain }}.conf"
        state: link

    - name: Test Nginx configuration
      ansible.builtin.command: nginx -t
      changed_when: false

    - name: Reload Nginx
      ansible.builtin.systemd:
        name: nginx
        state: reloaded
`, opts.Name, opts.Hosts, domain, upstream)

	case "docker-deploy":
		svc := fallback(opts.ServiceName, "app-stack")
		return header + fmt.Sprintf(`- name: %s
  hosts: %s
  become: true
  vars:
    stack_dir: /opt/%s
  tasks:
    - name: Pull latest container images
      ansible.builtin.command: docker compose pull
      args:
        chdir: "{{ stack_dir }}"

    - name: Start/update containers in detached mode
      ansible.builtin.command: docker compose up -d --remove-orphans
      args:
        chdir: "{{ stack_dir }}"

    - name: Verify running containers
      ansible.builtin.command: docker compose ps
      args:
        chdir: "{{ stack_dir }}"
      register: compose_ps
      changed_when: false

    - name: Show deployment status
      ansible.builtin.debug:
        var: compose_ps.stdout_lines
`, opts.Name, opts.Hosts, svc)

	case "systemd-service":
		svc := fallback(opts.ServiceName, "nginx")
		return header + fmt.Sprintf(`- name: %s
  hosts: %s
  become: true
  vars:
    service_name: %s
  tasks:
    - name: Ensure {{ service_name }} is enabled and restarted
      ansible.builtin.systemd:
        name: "{{ service_name }}"
        enabled: true
        state: restarted
        daemon_reload: true

    - name: Confirm {{ service_name }} is active
      ansible.builtin.command: "systemctl is-active {{ service_name }}"
      register: svc_state
      changed_when: false

    - name: Display service state
      ansible.builtin.debug:
        msg: "{{ service_name }} => {{ svc_state.stdout }}"
`, opts.Name, opts.Hosts, svc)

	case "firewall-ufw":
		return header + fmt.Sprintf(`- name: %s
  hosts: %s
  become: true
  tasks:
    - name: Allow SSH (22/tcp)
      ansible.builtin.command: ufw allow 22/tcp

    - name: Allow HTTP (80/tcp) and HTTPS (443/tcp)
      ansible.builtin.shell: ufw allow 80/tcp && ufw allow 443/tcp

    - name: Enable UFW non-interactively
      ansible.builtin.command: ufw --force enable

    - name: Show UFW status
      ansible.builtin.command: ufw status verbose
      register: ufw_out
      changed_when: false

    - name: Print firewall status
      ansible.builtin.debug:
        var: ufw_out.stdout_lines
`, opts.Name, opts.Hosts)

	case "package-install":
		pkgs := opts.Packages
		if len(pkgs) == 0 {
			pkgs = []string{"htop", "curl", "jq"}
		}
		var pkgList strings.Builder
		for _, p := range pkgs {
			pkgList.WriteString(fmt.Sprintf("          - %s\n", strings.TrimSpace(p)))
		}
		return header + fmt.Sprintf(`- name: %s
  hosts: %s
  become: true
  tasks:
    - name: Install requested packages
      ansible.builtin.package:
        name:
%s        state: present
`, opts.Name, opts.Hosts, pkgList.String())

	case "health-check":
		return header + fmt.Sprintf(`- name: %s
  hosts: %s
  gather_facts: true
  tasks:
    - name: Check uptime and memory
      ansible.builtin.shell: uptime && free -h && df -h /
      register: health_out
      changed_when: false

    - name: Report host health
      ansible.builtin.debug:
        var: health_out.stdout_lines
`, opts.Name, opts.Hosts)

	default:
		cmd := fallback(opts.CustomCmd, "uname -a && uptime")
		return header + fmt.Sprintf(`- name: %s
  hosts: %s
  become: %s
  tasks:
    - name: Run custom operational task
      ansible.builtin.shell: %s
      register: task_out

    - name: Print task output
      ansible.builtin.debug:
        var: task_out.stdout_lines
`, opts.Name, opts.Hosts, becomeStr, cmd)
	}
}

// GenerateScriptOptions configures the shell script generator.
type GenerateScriptOptions struct {
	Template    string // diagnostics | log-tail | cache-clear | backup-dir | docker-restart | http-probe | custom
	Name        string
	Description string
	ServiceName string
	TargetPath  string
	URL         string
	CustomCmd   string
}

// ScriptTemplates returns available script generator template IDs.
func ScriptTemplates() []string {
	return []string{
		"diagnostics",
		"log-tail",
		"cache-clear",
		"backup-dir",
		"docker-restart",
		"http-probe",
		"custom",
	}
}

// GenerateScriptContent creates a bash script from template options.
func GenerateScriptContent(opts GenerateScriptOptions) string {
	if opts.Name == "" {
		opts.Name = "Generated Script"
	}
	if opts.Description == "" {
		opts.Description = fmt.Sprintf("Generated %s script via Duchess", opts.Template)
	}

	header := fmt.Sprintf(`#!/usr/bin/env bash
# duchess:name: %s
# duchess:description: %s
# duchess:category: %s
set -euo pipefail

`, opts.Name, opts.Description, opts.Template)

	switch opts.Template {
	case "log-tail":
		svc := fallback(opts.ServiceName, "nginx")
		return header + fmt.Sprintf(`SERVICE_NAME="${SERVICE_NAME:-%s}"
LINES="${LINES:-80}"
echo "=== Logs for ${SERVICE_NAME} (last ${LINES} lines) ==="
journalctl -u "${SERVICE_NAME}" -n "${LINES}" --no-pager
`, svc)

	case "cache-clear":
		dir := fallback(opts.TargetPath, "/var/cache/nginx")
		return header + fmt.Sprintf(`CACHE_DIR="${CACHE_DIR:-%s}"
if [ -d "${CACHE_DIR}" ]; then
  find "${CACHE_DIR}" -mindepth 1 -maxdepth 2 -type f -delete
  echo "Cleared cache files under ${CACHE_DIR}"
else
  echo "Directory ${CACHE_DIR} not found"
fi
`, dir)

	case "backup-dir":
		dir := fallback(opts.TargetPath, "/etc/nginx")
		return header + fmt.Sprintf(`SRC_DIR="${SRC_DIR:-%s}"
STAMP="$(date +%%Y%%m%%d-%%H%%M%%S)"
ARCHIVE="/tmp/backup-$(basename "${SRC_DIR}")-${STAMP}.tar.gz"
tar -czf "${ARCHIVE}" "${SRC_DIR}"
ls -lh "${ARCHIVE}"
`, dir)

	case "docker-restart":
		svc := fallback(opts.ServiceName, "web")
		return header + fmt.Sprintf(`CONTAINER="${CONTAINER:-%s}"
echo "Restarting container ${CONTAINER}..."
docker restart "${CONTAINER}"
docker ps --filter "name=${CONTAINER}" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
`, svc)

	case "http-probe":
		url := fallback(opts.URL, "http://127.0.0.1:80/")
		return header + fmt.Sprintf(`TARGET_URL="${TARGET_URL:-%s}"
curl -fsS -o /dev/null -w "URL: %%{url_effective}\nHTTP: %%{http_code}\nTotal: %%{time_total}s\n" "${TARGET_URL}"
`, url)

	case "diagnostics":
		return header + `echo "Host: $(hostname -f 2>/dev/null || hostname)"
uptime
free -h
df -h /
ss -tlnp 2>/dev/null | head -n 25 || true
`
	default:
		cmd := fallback(opts.CustomCmd, "echo 'Hello from Duchess on '$(hostname)")
		return header + cmd + "\n"
	}
}

// SaveGenerated writes generated content to a sanitized file path inside targetDir.
func SaveGenerated(targetDir, fileName, content string) (string, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	clean := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fileName), " ", "-"))
	dest := filepath.Join(targetDir, filepath.Base(clean))
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
