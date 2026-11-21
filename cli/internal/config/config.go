package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AlertThresholds defines warning thresholds for proactive fleet health alerts.
type AlertThresholds struct {
	DiskWarnPct  float64 `yaml:"disk_warn_pct" json:"diskWarnPct"`
	DiskCritPct  float64 `yaml:"disk_crit_pct" json:"diskCritPct"`
	MemWarnPct   float64 `yaml:"mem_warn_pct" json:"memWarnPct"`
	CPUWarnPct   float64 `yaml:"cpu_warn_pct" json:"cpuWarnPct"`
	LoadFactor   float64 `yaml:"load_factor" json:"loadFactor"`
	CertWarnDays int     `yaml:"cert_warn_days" json:"certWarnDays"`
}

// SSHConfig holds workspace-level SSH defaults.
type SSHConfig struct {
	User           string `yaml:"user" json:"user,omitempty"`
	Port           int    `yaml:"port" json:"port,omitempty"`
	PrivateKey     string `yaml:"private_key" json:"privateKey,omitempty"`
	JumpHost       string `yaml:"jump_host" json:"jumpHost,omitempty"`
	UseSSHConfig   *bool  `yaml:"use_ssh_config" json:"useSshConfig,omitempty"`
	StrictHostKey  bool   `yaml:"strict_host_key" json:"strictHostKey"`
	KnownHostsPath string `yaml:"known_hosts_path" json:"knownHostsPath,omitempty"`
}

// WorkspaceConfig represents optional project configuration loaded from duchess.yml.
type WorkspaceConfig struct {
	Inventory           string          `yaml:"inventory" json:"inventory,omitempty"`
	AnsibleVenv         string          `yaml:"ansible_venv" json:"ansibleVenv,omitempty"`
	RemoteVenv          string          `yaml:"remote_venv" json:"remoteVenv,omitempty"`
	PollIntervalSeconds int             `yaml:"poll_interval_seconds" json:"pollIntervalSeconds"`
	ExecTimeoutSeconds  int             `yaml:"exec_timeout_seconds" json:"execTimeoutSeconds"`
	HistoryDir          string          `yaml:"history_dir" json:"historyDir,omitempty"`
	SSH                 SSHConfig       `yaml:"ssh" json:"ssh"`
	Alerts              AlertThresholds `yaml:"alerts" json:"alerts"`
	ConfigPath          string          `yaml:"-" json:"configPath,omitempty"`
}

// Default returns sensible default workspace configuration.
func Default() WorkspaceConfig {
	useSSHConfig := true
	return WorkspaceConfig{
		PollIntervalSeconds: 10,
		ExecTimeoutSeconds:  0, // 0 = no artificial timeout for interactive/long runs
		SSH: SSHConfig{
			UseSSHConfig: &useSSHConfig,
		},
		Alerts: AlertThresholds{
			DiskWarnPct:  85,
			DiskCritPct:  95,
			MemWarnPct:   90,
			CPUWarnPct:   90,
			LoadFactor:   1.5,
			CertWarnDays: 14,
		},
	}
}

// UseSSHConfigEnabled returns true unless explicitly disabled in config.
func (c WorkspaceConfig) UseSSHConfigEnabled() bool {
	if c.SSH.UseSSHConfig == nil {
		return true
	}
	return *c.SSH.UseSSHConfig
}

// EffectiveHistoryDir returns the resolved path for persistent execution logs.
func (c WorkspaceConfig) EffectiveHistoryDir(workspaceDir string) string {
	if strings.TrimSpace(c.HistoryDir) != "" {
		return expandPath(c.HistoryDir, workspaceDir)
	}
	return filepath.Join(workspaceDir, ".duchess", "history")
}

// Load reads workspace configuration from <workspace>/duchess.yml (or duchess.yaml),
// falling back to ~/.config/duchess/config.yml, and applies environment overrides.
func Load(workspaceDir string) WorkspaceConfig {
	cfg := Default()

	candidates := []string{
		filepath.Join(workspaceDir, "duchess.yml"),
		filepath.Join(workspaceDir, "duchess.yaml"),
		filepath.Join(workspaceDir, ".duchess.yml"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "duchess", "config.yml"),
			filepath.Join(home, ".config", "duchess", "config.yaml"),
		)
	}

	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var parsed WorkspaceConfig
		if err := yaml.Unmarshal(data, &parsed); err != nil {
			continue
		}
		mergeConfig(&cfg, parsed)
		cfg.ConfigPath = p
		break
	}

	// Environment variable overrides
	if v := strings.TrimSpace(os.Getenv("DUCHESS_ANSIBLE_VENV")); v != "" {
		cfg.AnsibleVenv = v
	}
	if v := strings.TrimSpace(os.Getenv("DUCHESS_REMOTE_VENV")); v != "" {
		cfg.RemoteVenv = v
	}
	if v := strings.TrimSpace(os.Getenv("DUCHESS_INVENTORY")); v != "" {
		cfg.Inventory = v
	}
	if v := strings.TrimSpace(os.Getenv("DUCHESS_SSH_JUMP")); v != "" {
		cfg.SSH.JumpHost = v
	}

	if cfg.PollIntervalSeconds <= 0 {
		cfg.PollIntervalSeconds = 10
	}
	if cfg.Alerts.DiskWarnPct <= 0 {
		cfg.Alerts.DiskWarnPct = 85
	}
	if cfg.Alerts.DiskCritPct <= 0 {
		cfg.Alerts.DiskCritPct = 95
	}
	if cfg.Alerts.MemWarnPct <= 0 {
		cfg.Alerts.MemWarnPct = 90
	}
	if cfg.Alerts.CPUWarnPct <= 0 {
		cfg.Alerts.CPUWarnPct = 90
	}
	if cfg.Alerts.LoadFactor <= 0 {
		cfg.Alerts.LoadFactor = 1.5
	}
	if cfg.Alerts.CertWarnDays <= 0 {
		cfg.Alerts.CertWarnDays = 14
	}
	return cfg
}

func mergeConfig(dst *WorkspaceConfig, src WorkspaceConfig) {
	if src.Inventory != "" {
		dst.Inventory = src.Inventory
	}
	if src.AnsibleVenv != "" {
		dst.AnsibleVenv = src.AnsibleVenv
	}
	if src.RemoteVenv != "" {
		dst.RemoteVenv = src.RemoteVenv
	}
	if src.PollIntervalSeconds > 0 {
		dst.PollIntervalSeconds = src.PollIntervalSeconds
	}
	if src.ExecTimeoutSeconds > 0 {
		dst.ExecTimeoutSeconds = src.ExecTimeoutSeconds
	}
	if src.HistoryDir != "" {
		dst.HistoryDir = src.HistoryDir
	}
	if src.SSH.User != "" {
		dst.SSH.User = src.SSH.User
	}
	if src.SSH.Port > 0 {
		dst.SSH.Port = src.SSH.Port
	}
	if src.SSH.PrivateKey != "" {
		dst.SSH.PrivateKey = src.SSH.PrivateKey
	}
	if src.SSH.JumpHost != "" {
		dst.SSH.JumpHost = src.SSH.JumpHost
	}
	if src.SSH.UseSSHConfig != nil {
		dst.SSH.UseSSHConfig = src.SSH.UseSSHConfig
	}
	if src.SSH.StrictHostKey {
		dst.SSH.StrictHostKey = true
	}
	if src.SSH.KnownHostsPath != "" {
		dst.SSH.KnownHostsPath = src.SSH.KnownHostsPath
	}
	if src.Alerts.DiskWarnPct > 0 {
		dst.Alerts.DiskWarnPct = src.Alerts.DiskWarnPct
	}
	if src.Alerts.DiskCritPct > 0 {
		dst.Alerts.DiskCritPct = src.Alerts.DiskCritPct
	}
	if src.Alerts.MemWarnPct > 0 {
		dst.Alerts.MemWarnPct = src.Alerts.MemWarnPct
	}
	if src.Alerts.CPUWarnPct > 0 {
		dst.Alerts.CPUWarnPct = src.Alerts.CPUWarnPct
	}
	if src.Alerts.LoadFactor > 0 {
		dst.Alerts.LoadFactor = src.Alerts.LoadFactor
	}
	if src.Alerts.CertWarnDays > 0 {
		dst.Alerts.CertWarnDays = src.Alerts.CertWarnDays
	}
}

func expandPath(p, baseDir string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}
