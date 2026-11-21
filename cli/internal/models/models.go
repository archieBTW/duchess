package models

import "time"

// Host represents a server enrolled via an Ansible inventory file.
type Host struct {
	Name       string            `json:"name"`
	Address    string            `json:"address"`
	User       string            `json:"user"`
	Port       int               `json:"port"`
	PrivateKey string            `json:"privateKey,omitempty"`
	Password   string            `json:"password,omitempty"`
	JumpHost   string            `json:"jumpHost,omitempty"`
	Venv       string            `json:"venv,omitempty"`
	RemoteVenv string            `json:"remoteVenv,omitempty"`
	Groups     []string          `json:"groups"`
	Vars       map[string]string `json:"vars,omitempty"`
}

// Inventory holds parsed Ansible hosts, groups, and group/global variables.
type Inventory struct {
	SourcePath string                       `json:"sourcePath"`
	Hosts      map[string]*Host             `json:"hosts"`
	Groups     map[string][]string          `json:"groups"`
	GroupVars  map[string]map[string]string `json:"groupVars,omitempty"`
	AllVars    map[string]string            `json:"allVars,omitempty"`
}

// DiskUsage represents a mounted filesystem's utilization.
type DiskUsage struct {
	Filesystem string  `json:"filesystem"`
	MountPoint string  `json:"mountPoint"`
	TotalBytes uint64  `json:"totalBytes"`
	UsedBytes  uint64  `json:"usedBytes"`
	AvailBytes uint64  `json:"availBytes"`
	UsePercent float64 `json:"usePercent"`
}

// NetInterface represents a network interface with IPv4 and byte counters.
type NetInterface struct {
	Name    string `json:"name"`
	IPv4    string `json:"ipv4,omitempty"`
	RxBytes uint64 `json:"rxBytes"`
	TxBytes uint64 `json:"txBytes"`
}

// ServerStats contains live telemetry fetched over SSH.
type ServerStats struct {
	HostName        string         `json:"hostName"`
	Address         string         `json:"address"`
	HostnameFQDN    string         `json:"hostnameFqdn"`
	OS              string         `json:"os"`
	Kernel          string         `json:"kernel"`
	Arch            string         `json:"arch"`
	UptimeSeconds   float64        `json:"uptimeSeconds"`
	UptimeHuman     string         `json:"uptimeHuman"`
	CPUCores        int            `json:"cpuCores"`
	CPUUsagePercent float64        `json:"cpuUsagePercent"`
	Load1           float64        `json:"load1"`
	Load5           float64        `json:"load5"`
	Load15          float64        `json:"load15"`
	MemTotalBytes   uint64         `json:"memTotalBytes"`
	MemUsedBytes    uint64         `json:"memUsedBytes"`
	MemAvailBytes   uint64         `json:"memAvailBytes"`
	MemUsagePercent float64        `json:"memUsagePercent"`
	SwapTotalBytes  uint64         `json:"swapTotalBytes"`
	SwapUsedBytes   uint64         `json:"swapUsedBytes"`
	Disks           []DiskUsage    `json:"disks"`
	Interfaces      []NetInterface `json:"interfaces"`
	CollectedAt     time.Time      `json:"collectedAt"`
	Error           string         `json:"error,omitempty"`
}

// ContainerInfo describes an auto-detected Docker or Podman container.
type ContainerInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	Runtime  string `json:"runtime"` // docker | podman
	State    string `json:"state"`
	Status   string `json:"status"`
	Ports    string `json:"ports"`
	CPUPerc  string `json:"cpuPerc,omitempty"`
	MemUsage string `json:"memUsage,omitempty"`
	MemPerc  string `json:"memPerc,omitempty"`
	NetIO    string `json:"netIO,omitempty"`
}

// ServiceInfo describes an auto-detected OS service (systemd / sysv).
type ServiceInfo struct {
	Name        string   `json:"name"`
	LoadState   string   `json:"loadState"`
	ActiveState string   `json:"activeState"`
	SubState    string   `json:"subState"`
	Description string   `json:"description"`
	Manager     string   `json:"manager"`
	Ports       []string `json:"ports,omitempty"`
}

// WebAppInfo describes an auto-detected website or web application hosted on
// Nginx, Apache, Tomcat, Caddy, HAProxy, or a direct HTTP runtime.
type WebAppInfo struct {
	Name           string   `json:"name"`
	ServerType     string   `json:"serverType"` // nginx | apache | tomcat | caddy | haproxy | http
	Domains        []string `json:"domains"`
	Ports          []string `json:"ports"`
	SSL            bool     `json:"ssl"`
	CertPath       string   `json:"certPath,omitempty"`
	CertExpiryDays *int     `json:"certExpiryDays,omitempty"`
	RootOrPath     string   `json:"rootOrPath,omitempty"`
	ProxyTarget    string   `json:"proxyTarget,omitempty"`
	ConfigPath     string   `json:"configPath,omitempty"`
	Status         string   `json:"status"`
}

// ListeningPort describes a TCP port open on the server.
type ListeningPort struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     string `json:"port"`
	Process  string `json:"process"`
}

// Alert represents a proactive health warning or critical finding on a host.
type Alert struct {
	Severity string `json:"severity"` // warning | critical
	Category string `json:"category"` // disk | memory | cpu | load | service | container | tls
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

// DiscoveryReport combines live stats with auto-detected containers, services,
// listening ports, hosted web applications, and evaluated health alerts.
type DiscoveryReport struct {
	HostName       string          `json:"hostName"`
	Address        string          `json:"address"`
	Stats          *ServerStats    `json:"stats,omitempty"`
	Containers     []ContainerInfo `json:"containers"`
	Services       []ServiceInfo   `json:"services"`
	WebApps        []WebAppInfo    `json:"webApps"`
	ListeningPorts []ListeningPort `json:"listeningPorts"`
	Alerts         []Alert         `json:"alerts,omitempty"`
	CollectedAt    time.Time       `json:"collectedAt"`
	Error          string          `json:"error,omitempty"`
}

// AutomationItem represents a playbook or shell script stored in the workspace.
type AutomationItem struct {
	FileName    string            `json:"fileName"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Kind        string            `json:"kind"` // playbook | script
	Path        string            `json:"path"`
	Venv        string            `json:"venv,omitempty"`
	RemoteVenv  string            `json:"remoteVenv,omitempty"`
	DefaultVars map[string]string `json:"defaultVars,omitempty"`
	Content     string            `json:"content,omitempty"`
}

// ExecutionResult captures output from running a command, script, or playbook.
type ExecutionResult struct {
	HostName   string `json:"hostName"`
	Address    string `json:"address,omitempty"`
	ExitCode   int    `json:"exitCode"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"durationMs"`
	Engine     string `json:"engine,omitempty"`
	Venv       string `json:"venv,omitempty"`
	Error      string `json:"error,omitempty"`
}
