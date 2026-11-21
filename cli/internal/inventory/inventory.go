package inventory

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/duchess-devops/duchess-cli/internal/models"
	"gopkg.in/yaml.v3"
)

// DefaultSSHUser returns the current OS username (matching standard ssh/ansible behavior).
func DefaultSSHUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if cur, err := user.Current(); err == nil && cur.Username != "" {
		return cur.Username
	}
	return "root"
}

// New creates an empty Inventory.
func New(sourcePath string) *models.Inventory {
	return &models.Inventory{
		SourcePath: sourcePath,
		Hosts:      make(map[string]*models.Host),
		Groups:     make(map[string][]string),
		GroupVars:  make(map[string]map[string]string),
		AllVars:    make(map[string]string),
	}
}

// Load parses an Ansible inventory file in either INI or YAML format.
func Load(path string) (*models.Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read inventory %s: %w", path, err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	trimmed := strings.TrimSpace(string(data))
	if ext == ".yml" || ext == ".yaml" || strings.HasPrefix(trimmed, "all:") || strings.HasPrefix(trimmed, "---") {
		return ParseYAML(string(data), path)
	}
	return ParseINI(string(data), path)
}

// ParseINI parses an Ansible INI inventory string.
func ParseINI(content, sourcePath string) (*models.Inventory, error) {
	inv := New(sourcePath)
	scanner := bufio.NewScanner(strings.NewReader(content))

	currentSection := "ungrouped"
	sectionType := "hosts" // hosts | vars | children
	childrenMap := make(map[string][]string)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			header := strings.TrimSpace(line[1 : len(line)-1])
			if strings.Contains(header, ":") {
				parts := strings.SplitN(header, ":", 2)
				currentSection = strings.TrimSpace(parts[0])
				sectionType = strings.TrimSpace(parts[1])
			} else {
				currentSection = header
				sectionType = "hosts"
				if _, ok := inv.Groups[currentSection]; !ok {
					inv.Groups[currentSection] = []string{}
				}
			}
			continue
		}

		switch sectionType {
		case "vars":
			k, v, ok := splitKeyVal(line)
			if !ok {
				continue
			}
			if currentSection == "all" {
				inv.AllVars[k] = v
			} else {
				if inv.GroupVars[currentSection] == nil {
					inv.GroupVars[currentSection] = make(map[string]string)
				}
				inv.GroupVars[currentSection][k] = v
			}
		case "children":
			child := strings.Fields(line)[0]
			childrenMap[currentSection] = append(childrenMap[currentSection], child)
		default:
			tokens := tokenizeLine(line)
			if len(tokens) == 0 {
				continue
			}
			hostName := tokens[0]
			hostVars := make(map[string]string)
			for _, tok := range tokens[1:] {
				if k, v, ok := splitKeyVal(tok); ok {
					hostVars[k] = v
				}
			}
			upsertHost(inv, hostName, currentSection, hostVars)
		}
	}

	// Propagate [parent:children] memberships
	for parent, children := range childrenMap {
		for _, child := range children {
			for _, h := range inv.Groups[child] {
				addHostToGroup(inv, h, parent)
			}
		}
	}

	applyInheritedVars(inv)
	return inv, nil
}

// ParseYAML parses an Ansible YAML inventory string.
func ParseYAML(content, sourcePath string) (*models.Inventory, error) {
	inv := New(sourcePath)
	var root map[string]any
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return nil, fmt.Errorf("parse yaml inventory: %w", err)
	}

	var walkGroup func(groupName string, node map[string]any)
	walkGroup = func(groupName string, node map[string]any) {
		if varsRaw, ok := node["vars"].(map[string]any); ok {
			for k, v := range varsRaw {
				val := fmt.Sprintf("%v", v)
				if groupName == "all" {
					inv.AllVars[k] = val
				} else {
					if inv.GroupVars[groupName] == nil {
						inv.GroupVars[groupName] = make(map[string]string)
					}
					inv.GroupVars[groupName][k] = val
				}
			}
		}
		if hostsRaw, ok := node["hosts"].(map[string]any); ok {
			for hostName, hVal := range hostsRaw {
				vars := make(map[string]string)
				if hMap, ok := hVal.(map[string]any); ok {
					for k, v := range hMap {
						vars[k] = fmt.Sprintf("%v", v)
					}
				}
				grp := groupName
				if grp == "all" {
					grp = "ungrouped"
				}
				upsertHost(inv, hostName, grp, vars)
			}
		}
		if childrenRaw, ok := node["children"].(map[string]any); ok {
			for childName, cVal := range childrenRaw {
				if cMap, ok := cVal.(map[string]any); ok {
					walkGroup(childName, cMap)
				}
			}
		}
	}

	for topKey, topVal := range root {
		if m, ok := topVal.(map[string]any); ok {
			walkGroup(topKey, m)
		}
	}

	applyInheritedVars(inv)
	return inv, nil
}

func upsertHost(inv *models.Inventory, name, group string, vars map[string]string) {
	h, exists := inv.Hosts[name]
	if !exists {
		h = &models.Host{
			Name:    name,
			Address: name,
			User:    "",
			Port:    22,
			Vars:    make(map[string]string),
		}
		inv.Hosts[name] = h
	}
	for k, v := range vars {
		h.Vars[k] = v
	}
	addHostToGroup(inv, name, group)
}

func addHostToGroup(inv *models.Inventory, hostName, group string) {
	if group == "" {
		group = "ungrouped"
	}
	h := inv.Hosts[hostName]
	if h == nil {
		return
	}
	if !containsStr(h.Groups, group) {
		h.Groups = append(h.Groups, group)
	}
	if !containsStr(inv.Groups[group], hostName) {
		inv.Groups[group] = append(inv.Groups[group], hostName)
	}
}

func applyInheritedVars(inv *models.Inventory) {
	for _, h := range inv.Hosts {
		merged := make(map[string]string)
		for k, v := range inv.AllVars {
			merged[k] = v
		}
		for _, g := range h.Groups {
			for k, v := range inv.GroupVars[g] {
				merged[k] = v
			}
		}
		for k, v := range h.Vars {
			merged[k] = v
		}
		if v := merged["ansible_host"]; v != "" {
			h.Address = v
		}
		if v := merged["ansible_user"]; v != "" {
			h.User = v
		}
		if v := merged["ansible_port"]; v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				h.Port = p
			}
		}
		if v := merged["ansible_ssh_private_key_file"]; v != "" {
			h.PrivateKey = ExpandHome(v)
		}
		if v := merged["ansible_password"]; v != "" {
			h.Password = v
		} else if v := merged["ansible_ssh_pass"]; v != "" {
			h.Password = v
		}
		if v := merged["duchess_jump_host"]; v != "" {
			h.JumpHost = v
		} else if v := merged["proxy_jump"]; v != "" {
			h.JumpHost = v
		} else if v := merged["ansible_ssh_common_args"]; v != "" {
			if j := extractJumpFromArgs(v); j != "" {
				h.JumpHost = j
			}
		}
		if h.Port == 0 {
			h.Port = 22
		}
		if h.User == "" {
			h.User = DefaultSSHUser()
		}
		sort.Strings(h.Groups)
	}
}

// Merge enrolls all hosts, groups, and vars from incoming into base.
func Merge(base, incoming *models.Inventory) int {
	added := 0
	for k, v := range incoming.AllVars {
		if _, ok := base.AllVars[k]; !ok {
			base.AllVars[k] = v
		}
	}
	for g, vars := range incoming.GroupVars {
		if base.GroupVars[g] == nil {
			base.GroupVars[g] = make(map[string]string)
		}
		for k, v := range vars {
			base.GroupVars[g][k] = v
		}
	}
	for name, h := range incoming.Hosts {
		if _, exists := base.Hosts[name]; !exists {
			added++
		}
		groups := h.Groups
		if len(groups) == 0 {
			groups = []string{"ungrouped"}
		}
		for _, g := range groups {
			upsertHost(base, name, g, h.Vars)
		}
		if base.Hosts[name].Vars["ansible_host"] == "" && h.Address != "" && h.Address != name {
			base.Hosts[name].Vars["ansible_host"] = h.Address
		}
		if base.Hosts[name].Vars["ansible_user"] == "" && h.User != "" {
			base.Hosts[name].Vars["ansible_user"] = h.User
		}
		if base.Hosts[name].Vars["ansible_port"] == "" && h.Port > 0 && h.Port != 22 {
			base.Hosts[name].Vars["ansible_port"] = strconv.Itoa(h.Port)
		}
		if base.Hosts[name].Vars["ansible_ssh_private_key_file"] == "" && h.PrivateKey != "" {
			base.Hosts[name].Vars["ansible_ssh_private_key_file"] = h.PrivateKey
		}
	}
	applyInheritedVars(base)
	return added
}

// AddHost adds or updates a host in the inventory.
func AddHost(inv *models.Inventory, h *models.Host) {
	vars := make(map[string]string)
	for k, v := range h.Vars {
		vars[k] = v
	}
	if h.Address != "" {
		vars["ansible_host"] = h.Address
	}
	if h.User != "" {
		vars["ansible_user"] = h.User
	}
	if h.Port > 0 {
		vars["ansible_port"] = strconv.Itoa(h.Port)
	}
	if h.PrivateKey != "" {
		vars["ansible_ssh_private_key_file"] = h.PrivateKey
	}
	groups := h.Groups
	if len(groups) == 0 {
		groups = []string{"production"}
	}
	for _, g := range groups {
		upsertHost(inv, h.Name, g, vars)
	}
	applyInheritedVars(inv)
}

// RemoveHost removes a host from the inventory and all groups.
func RemoveHost(inv *models.Inventory, name string) bool {
	if _, ok := inv.Hosts[name]; !ok {
		return false
	}
	delete(inv.Hosts, name)
	for g, members := range inv.Groups {
		var next []string
		for _, m := range members {
			if m != name {
				next = append(next, m)
			}
		}
		if len(next) == 0 {
			delete(inv.Groups, g)
		} else {
			inv.Groups[g] = next
		}
	}
	return true
}

// SaveINI writes the inventory back in clean Ansible INI format.
func SaveINI(inv *models.Inventory, path string) error {
	var b strings.Builder
	b.WriteString("# Duchess DevOps Ansible Inventory\n\n")

	groupNames := make([]string, 0, len(inv.Groups))
	for g := range inv.Groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	writtenDetails := make(map[string]bool)
	for _, g := range groupNames {
		members := append([]string(nil), inv.Groups[g]...)
		if len(members) == 0 {
			continue
		}
		sort.Strings(members)
		b.WriteString(fmt.Sprintf("[%s]\n", g))
		for _, name := range members {
			h := inv.Hosts[name]
			if h == nil {
				continue
			}
			line := name
			if h.Address != "" && h.Address != name {
				line += fmt.Sprintf(" ansible_host=%s", h.Address)
			}
			if !writtenDetails[name] {
				if h.User != "" {
					line += fmt.Sprintf(" ansible_user=%s", h.User)
				}
				if h.Port > 0 && h.Port != 22 {
					line += fmt.Sprintf(" ansible_port=%d", h.Port)
				} else if h.Port == 22 {
					line += " ansible_port=22"
				}
				if h.PrivateKey != "" {
					line += fmt.Sprintf(" ansible_ssh_private_key_file=%s", h.PrivateKey)
				}
				// Include extra custom host vars
				var extraKeys []string
				for k := range h.Vars {
					switch k {
					case "ansible_host", "ansible_user", "ansible_port", "ansible_ssh_private_key_file", "ansible_password", "ansible_ssh_pass":
						continue
					default:
						extraKeys = append(extraKeys, k)
					}
				}
				sort.Strings(extraKeys)
				for _, k := range extraKeys {
					line += fmt.Sprintf(" %s=%s", k, quoteIfNeeded(h.Vars[k]))
				}
				writtenDetails[name] = true
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	if len(inv.GroupVars) > 0 {
		gvNames := make([]string, 0, len(inv.GroupVars))
		for g := range inv.GroupVars {
			gvNames = append(gvNames, g)
		}
		sort.Strings(gvNames)
		for _, g := range gvNames {
			if len(inv.GroupVars[g]) == 0 {
				continue
			}
			b.WriteString(fmt.Sprintf("[%s:vars]\n", g))
			keys := sortedKeys(inv.GroupVars[g])
			for _, k := range keys {
				b.WriteString(fmt.Sprintf("%s=%s\n", k, quoteIfNeeded(inv.GroupVars[g][k])))
			}
			b.WriteString("\n")
		}
	}

	if len(inv.AllVars) > 0 {
		b.WriteString("[all:vars]\n")
		for _, k := range sortedKeys(inv.AllVars) {
			b.WriteString(fmt.Sprintf("%s=%s\n", k, quoteIfNeeded(inv.AllVars[k])))
		}
		b.WriteString("\n")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ResolveTargets resolves a target expression ("", "all", group name, host name,
// IP address, or comma-separated list) into a deterministic slice of hosts.
func ResolveTargets(inv *models.Inventory, pattern string) []*models.Host {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "all" || pattern == "*" {
		return SortedHosts(inv)
	}
	seen := make(map[string]bool)
	var out []*models.Host
	for _, raw := range strings.Split(pattern, ",") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if p == "all" || p == "*" {
			return SortedHosts(inv)
		}
		if h, ok := inv.Hosts[p]; ok {
			if !seen[h.Name] {
				seen[h.Name] = true
				out = append(out, h)
			}
			continue
		}
		if members, ok := inv.Groups[p]; ok {
			for _, m := range members {
				if h, ok := inv.Hosts[m]; ok && !seen[h.Name] {
					seen[h.Name] = true
					out = append(out, h)
				}
			}
			continue
		}
		// Also match by IP / Address
		for _, h := range inv.Hosts {
			if h.Address == p && !seen[h.Name] {
				seen[h.Name] = true
				out = append(out, h)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SortedHosts returns all hosts sorted by name.
func SortedHosts(inv *models.Inventory) []*models.Host {
	out := make([]*models.Host, 0, len(inv.Hosts))
	for _, h := range inv.Hosts {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ExpandHome replaces a leading ~/ with the current user's home directory.
func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func splitKeyVal(s string) (string, string, bool) {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 {
		return "", "", false
	}
	k := strings.TrimSpace(s[:idx])
	v := strings.TrimSpace(s[idx+1:])
	if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
		v = v[1 : len(v)-1]
	}
	return k, v, true
}

func tokenizeLine(line string) []string {
	var tokens []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			cur.WriteByte(ch)
			continue
		}
		if ch == ' ' || ch == '\t' {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			continue
		}
		if ch == '#' && cur.Len() == 0 {
			break
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func quoteIfNeeded(v string) string {
	if strings.ContainsAny(v, " \t") {
		return "'" + v + "'"
	}
	return v
}

func containsStr(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func extractJumpFromArgs(args string) string {
	fields := strings.Fields(args)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "-J" && i+1 < len(fields) {
			return fields[i+1]
		}
		if strings.HasPrefix(f, "-J") && len(f) > 2 {
			return f[2:]
		}
		if strings.HasPrefix(strings.ToLower(f), "proxyjump=") {
			return f[len("proxyjump="):]
		}
		if f == "-o" && i+1 < len(fields) && strings.HasPrefix(strings.ToLower(fields[i+1]), "proxyjump=") {
			return fields[i+1][len("proxyjump="):]
		}
	}
	return ""
}
