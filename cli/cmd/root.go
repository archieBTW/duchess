package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/config"
	"github.com/duchess-devops/duchess-cli/internal/history"
	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
	"github.com/spf13/cobra"
)

var (
	flagWorkspace     string
	flagInventory     string
	flagSSHUser       string
	flagSSHKey        string
	flagSSHPort       int
	flagSSHPass       string
	flagSSHJump       string
	flagStrictHostKey bool
	flagKnownHosts    string
	flagSSHTimeout    int
	flagDefaultVenv   string
	flagJSON          bool
	flagNoPrompt      bool
)

var rootCmd = &cobra.Command{
	Use:   "duchess",
	Short: "Duchess DevOps TUI & CLI — interactive ASCII dashboard, SSH telemetry, discovery, playbooks, and scripts",
	Long: `Duchess is a DevOps TUI & CLI control plane for managing your server fleet.
Running 'duchess' with no arguments launches the full interactive ASCII TUI Dashboard:
  • ASCII fleet dashboard with live CPU/RAM/disk bar gauges, sparklines, proactive alerts, and auto-refresh
  • Actionable Server Inspector for Docker/Podman containers, systemd services, Nginx/Apache/Tomcat/Caddy/HAProxy apps, interactive SSH, and multi-host fan-out console
  • Ansible INI/YAML inventory enrollment & $EDITOR integration
  • Playbook & Script library with Python virtualenv (--venv) support, live streaming output, cancellation, and generator wizard
  • Persistent execution audit log & SSH ProxyJump / ~/.ssh/config / known_hosts support

Subcommands remain available for scripting and automation.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return launchTUI()
	},
}

// Execute runs the root CLI command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagWorkspace, "workspace", "w", "", "Duchess workspace directory (auto-detected if omitted)")
	rootCmd.PersistentFlags().StringVarP(&flagInventory, "inventory", "i", "", "Path to Ansible inventory file (defaults to <workspace>/inventory/hosts.ini)")
	rootCmd.PersistentFlags().StringVarP(&flagSSHUser, "user", "u", "", "Override SSH username")
	rootCmd.PersistentFlags().StringVarP(&flagSSHKey, "key", "k", "", "Override SSH private key path")
	rootCmd.PersistentFlags().IntVarP(&flagSSHPort, "port", "p", 0, "Override SSH port")
	rootCmd.PersistentFlags().StringVar(&flagSSHPass, "password", "", "SSH password (prefer SSH keys or agent)")
	rootCmd.PersistentFlags().StringVarP(&flagSSHJump, "jump", "J", "", "SSH ProxyJump bastion host (e.g. user@bastion:22)")
	rootCmd.PersistentFlags().BoolVar(&flagStrictHostKey, "strict-host-key", false, "Verify remote host keys against ~/.ssh/known_hosts")
	rootCmd.PersistentFlags().StringVar(&flagKnownHosts, "known-hosts", "", "Custom known_hosts file path (with --strict-host-key)")
	rootCmd.PersistentFlags().IntVar(&flagSSHTimeout, "timeout", 12, "SSH dial/handshake timeout in seconds")
	rootCmd.PersistentFlags().StringVar(&flagDefaultVenv, "venv", "", "Python virtualenv path for running ansible-playbook")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "Output machine-readable JSON")
	rootCmd.PersistentFlags().BoolVar(&flagNoPrompt, "no-prompt", false, "Skip the SSH user setup prompt when launching the TUI")
}

func resolveWorkspace() string {
	if flagWorkspace != "" {
		return flagWorkspace
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := cwd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "inventory", "hosts.ini")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "inventory", "hosts.yml")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "duchess.yml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return cwd
}

func loadWorkspaceConfig() config.WorkspaceConfig {
	return config.Load(resolveWorkspace())
}

func resolveInventoryPath() string {
	if flagInventory != "" {
		return flagInventory
	}
	ws := resolveWorkspace()
	wsCfg := config.Load(ws)
	if wsCfg.Inventory != "" {
		p := inventory.ExpandHome(wsCfg.Inventory)
		if !filepath.IsAbs(p) {
			p = filepath.Join(ws, p)
		}
		return p
	}
	iniPath := filepath.Join(ws, "inventory", "hosts.ini")
	if _, err := os.Stat(iniPath); err == nil {
		return iniPath
	}
	ymlPath := filepath.Join(ws, "inventory", "hosts.yml")
	if _, err := os.Stat(ymlPath); err == nil {
		return ymlPath
	}
	return iniPath
}

func loadInventory() (*models.Inventory, string, error) {
	invPath := resolveInventoryPath()
	inv, err := inventory.Load(invPath)
	if err != nil {
		return nil, invPath, err
	}
	return inv, invPath, nil
}

func sshOptions() sshclient.Options {
	wsCfg := loadWorkspaceConfig()
	user := wsCfg.SSH.User
	if flagSSHUser != "" {
		user = flagSSHUser
	}
	port := wsCfg.SSH.Port
	if flagSSHPort > 0 {
		port = flagSSHPort
	}
	key := wsCfg.SSH.PrivateKey
	if flagSSHKey != "" {
		key = flagSSHKey
	}
	jump := wsCfg.SSH.JumpHost
	if flagSSHJump != "" {
		jump = flagSSHJump
	}
	strict := wsCfg.SSH.StrictHostKey || flagStrictHostKey
	knownHosts := wsCfg.SSH.KnownHostsPath
	if flagKnownHosts != "" {
		knownHosts = flagKnownHosts
	}
	sshCfgPath := ""
	if !wsCfg.UseSSHConfigEnabled() {
		sshCfgPath = "none"
	}
	return sshclient.Options{
		User:           user,
		Port:           port,
		PrivateKey:     key,
		Password:       flagSSHPass,
		JumpHost:       jump,
		UseSSHConfig:   wsCfg.UseSSHConfigEnabled(),
		SSHConfigPath:  sshCfgPath,
		StrictHostKey:  strict,
		KnownHostsPath: knownHosts,
		Timeout:        time.Duration(flagSSHTimeout) * time.Second,
	}
}

func recordHistory(entry history.Entry) {
	ws := resolveWorkspace()
	wsCfg := config.Load(ws)
	_ = history.Append(wsCfg.EffectiveHistoryDir(ws), entry)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func parseKVFlags(pairs []string) map[string]string {
	out := make(map[string]string)
	for _, p := range pairs {
		if idx := strings.IndexByte(p, '='); idx > 0 {
			out[strings.TrimSpace(p[:idx])] = strings.TrimSpace(p[idx+1:])
		}
	}
	return out
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
