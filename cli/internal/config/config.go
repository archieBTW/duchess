package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/duchess-devops/duchess-cli/internal/models"
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
	Inventory           string            `yaml:"inventory,omitempty" json:"inventory,omitempty"`
	AnsibleVenv         string            `yaml:"ansible_venv,omitempty" json:"ansibleVenv,omitempty"`
	RemoteVenv          string            `yaml:"remote_venv,omitempty" json:"remoteVenv,omitempty"`
	PlaybookVenvs       map[string]string `yaml:"playbook_venvs,omitempty" json:"playbookVenvs,omitempty"`
	ServerVenvs         map[string]string `yaml:"server_venvs,omitempty" json:"serverVenvs,omitempty"`
	ServerRemoteVenvs   map[string]string `yaml:"server_remote_venvs,omitempty" json:"serverRemoteVenvs,omitempty"`
	PollIntervalSeconds int               `yaml:"poll_interval_seconds,omitempty" json:"pollIntervalSeconds"`
	ExecTimeoutSeconds  int               `yaml:"exec_timeout_seconds,omitempty" json:"execTimeoutSeconds"`
	HistoryDir          string            `yaml:"history_dir,omitempty" json:"historyDir,omitempty"`
	SSH                 SSHConfig         `yaml:"ssh,omitempty" json:"ssh"`
	Alerts              AlertThresholds   `yaml:"alerts,omitempty" json:"alerts"`
	ConfigPath          string            `yaml:"-" json:"configPath,omitempty"`
}

// Default returns sensible default workspace configuration.
func Default() WorkspaceConfig {
	useSSHConfig := true
	return WorkspaceConfig{
		PollIntervalSeconds: 10,
		ExecTimeoutSeconds:  0, // 0 = no artificial timeout for interactive/long runs
		PlaybookVenvs:       make(map[string]string),
		ServerVenvs:         make(map[string]string),
		ServerRemoteVenvs:   make(map[string]string),
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
	// Avoid double-nesting ~/.duchess/.duchess/history when workspace is ~/.duchess
	if filepath.Base(filepath.Clean(workspaceDir)) == ".duchess" {
		return filepath.Join(workspaceDir, "history")
	}
	return filepath.Join(workspaceDir, ".duchess", "history")
}

// MatchPlaybookVenv returns a pinned controller venv for playbookPath from playbook_venvs.
// Checks exact filename (deploy-bybl.yml), stem (deploy-bybl), and glob patterns (deploy-*.yml).
func (c WorkspaceConfig) MatchPlaybookVenv(playbookPath string) string {
	if len(c.PlaybookVenvs) == 0 || strings.TrimSpace(playbookPath) == "" {
		return ""
	}
	base := filepath.Base(playbookPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return matchPatternMap(c.PlaybookVenvs, []string{playbookPath, base, stem})
}

// MatchServerVenv returns a pinned controller venv for target hosts/groups.
// Priority: inventory host var (duchess_venv) > exact host alias/IP in server_venvs >
// inventory group in server_venvs > target expression / glob pattern in server_venvs.
func (c WorkspaceConfig) MatchServerVenv(hosts []*models.Host, targetExpr string) string {
	for _, h := range hosts {
		if h != nil && strings.TrimSpace(h.Venv) != "" {
			return strings.TrimSpace(h.Venv)
		}
	}
	if len(c.ServerVenvs) == 0 {
		return ""
	}
	var hostCandidates []string
	var groupCandidates []string
	for _, h := range hosts {
		if h == nil {
			continue
		}
		if h.Name != "" {
			hostCandidates = append(hostCandidates, h.Name)
		}
		if h.Address != "" {
			hostCandidates = append(hostCandidates, h.Address)
		}
		groupCandidates = append(groupCandidates, h.Groups...)
	}
	if v := matchPatternMap(c.ServerVenvs, hostCandidates); v != "" {
		return v
	}
	if t := strings.TrimSpace(targetExpr); t != "" && t != "all" {
		if v := matchPatternMap(c.ServerVenvs, []string{t}); v != "" {
			return v
		}
	}
	if v := matchPatternMap(c.ServerVenvs, groupCandidates); v != "" {
		return v
	}
	return ""
}

// MatchServerRemoteVenv returns a pinned remote Python interpreter venv for target hosts/groups.
func (c WorkspaceConfig) MatchServerRemoteVenv(hosts []*models.Host, targetExpr string) string {
	for _, h := range hosts {
		if h != nil && strings.TrimSpace(h.RemoteVenv) != "" {
			return strings.TrimSpace(h.RemoteVenv)
		}
	}
	if len(c.ServerRemoteVenvs) == 0 {
		return ""
	}
	var hostCandidates, groupCandidates []string
	for _, h := range hosts {
		if h == nil {
			continue
		}
		if h.Name != "" {
			hostCandidates = append(hostCandidates, h.Name)
		}
		if h.Address != "" {
			hostCandidates = append(hostCandidates, h.Address)
		}
		groupCandidates = append(groupCandidates, h.Groups...)
	}
	if v := matchPatternMap(c.ServerRemoteVenvs, hostCandidates); v != "" {
		return v
	}
	if t := strings.TrimSpace(targetExpr); t != "" && t != "all" {
		if v := matchPatternMap(c.ServerRemoteVenvs, []string{t}); v != "" {
			return v
		}
	}
	return matchPatternMap(c.ServerRemoteVenvs, groupCandidates)
}

func matchPatternMap(m map[string]string, candidates []string) string {
	if len(m) == 0 || len(candidates) == 0 {
		return ""
	}
	// 1. Exact matches first (case-sensitive, then case-insensitive)
	for _, cand := range candidates {
		if v := strings.TrimSpace(m[cand]); v != "" {
			return v
		}
	}
	for _, cand := range candidates {
		low := strings.ToLower(cand)
		for k, v := range m {
			if strings.ToLower(k) == low && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	// 2. Glob patterns (longest pattern wins for deterministic specificity)
	patterns := make([]string, 0, len(m))
	for k := range m {
		if strings.ContainsAny(k, "*?[") {
			patterns = append(patterns, k)
		}
	}
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) != len(patterns[j]) {
			return len(patterns[i]) > len(patterns[j])
		}
		return patterns[i] < patterns[j]
	})
	for _, pat := range patterns {
		for _, cand := range candidates {
			if ok, err := filepath.Match(pat, cand); err == nil && ok {
				if v := strings.TrimSpace(m[pat]); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// Save writes cfg to path (or <workspaceDir>/duchess.yml if path is empty).
func Save(workspaceDir, path string, cfg WorkspaceConfig) (string, error) {
	if path == "" {
		if cfg.ConfigPath != "" {
			path = cfg.ConfigPath
		} else {
			path = filepath.Join(workspaceDir, "duchess.yml")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return "", err
	}
	header := "# Duchess Workspace Configuration\n"
	if err := os.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
		return "", err
	}
	return path, nil
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
			filepath.Join(home, ".duchess", "duchess.yml"),
			filepath.Join(home, ".duchess", "duchess.yaml"),
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
	for k, v := range src.PlaybookVenvs {
		if dst.PlaybookVenvs == nil {
			dst.PlaybookVenvs = make(map[string]string)
		}
		dst.PlaybookVenvs[k] = v
	}
	for k, v := range src.ServerVenvs {
		if dst.ServerVenvs == nil {
			dst.ServerVenvs = make(map[string]string)
		}
		dst.ServerVenvs[k] = v
	}
	for k, v := range src.ServerRemoteVenvs {
		if dst.ServerRemoteVenvs == nil {
			dst.ServerRemoteVenvs = make(map[string]string)
		}
		dst.ServerRemoteVenvs[k] = v
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
