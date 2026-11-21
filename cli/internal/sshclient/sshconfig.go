package sshclient

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SSHConfigMatch holds resolved directives from ~/.ssh/config for a host.
type SSHConfigMatch struct {
	HostName      string
	User          string
	Port          int
	IdentityFiles []string
	ProxyJump     string
}

type sshConfigBlock struct {
	patterns      []string
	hostName      string
	user          string
	port          int
	identityFiles []string
	proxyJump     string
}

// ResolveSSHConfig parses ~/.ssh/config (or a custom path) and resolves matching
// directives for hostAlias and hostAddr following OpenSSH first-match-wins semantics.
func ResolveSSHConfig(configPath string, names ...string) SSHConfigMatch {
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return SSHConfigMatch{}
		}
		configPath = filepath.Join(home, ".ssh", "config")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return SSHConfigMatch{}
	}
	return ParseSSHConfig(string(data), names...)
}

// ParseSSHConfig parses OpenSSH client config text and resolves directives for names.
func ParseSSHConfig(content string, names ...string) SSHConfigMatch {
	var blocks []sshConfigBlock
	var current *sshConfigBlock

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := splitSSHConfigDirective(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "host":
			blocks = append(blocks, sshConfigBlock{patterns: strings.Fields(val)})
			current = &blocks[len(blocks)-1]
		case "match":
			// Stop applying directives inside Match blocks (not Host-only)
			current = nil
		default:
			if current == nil {
				continue
			}
			switch strings.ToLower(key) {
			case "hostname":
				if current.hostName == "" {
					current.hostName = val
				}
			case "user":
				if current.user == "" {
					current.user = val
				}
			case "port":
				if current.port == 0 {
					if p, err := strconv.Atoi(val); err == nil && p > 0 {
						current.port = p
					}
				}
			case "identityfile":
				current.identityFiles = append(current.identityFiles, strings.Trim(val, `"`))
			case "proxyjump":
				if current.proxyJump == "" && !strings.EqualFold(val, "none") {
					current.proxyJump = val
				}
			}
		}
	}

	var out SSHConfigMatch
	for _, blk := range blocks {
		if !blockMatchesAny(blk.patterns, names) {
			continue
		}
		if out.HostName == "" && blk.hostName != "" {
			out.HostName = blk.hostName
		}
		if out.User == "" && blk.user != "" {
			out.User = blk.user
		}
		if out.Port == 0 && blk.port > 0 {
			out.Port = blk.port
		}
		if out.ProxyJump == "" && blk.proxyJump != "" {
			out.ProxyJump = blk.proxyJump
		}
		out.IdentityFiles = append(out.IdentityFiles, blk.identityFiles...)
	}
	return out
}

func splitSSHConfigDirective(line string) (string, string, bool) {
	// OpenSSH accepts either whitespace or '=' as separator
	sepIdx := strings.IndexAny(line, " \t=")
	if sepIdx <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:sepIdx])
	val := strings.TrimSpace(line[sepIdx+1:])
	val = strings.TrimLeft(val, "=")
	val = strings.TrimSpace(val)
	if key == "" || val == "" {
		return "", "", false
	}
	return key, val, true
}

func blockMatchesAny(patterns, names []string) bool {
	for _, name := range names {
		if name == "" {
			continue
		}
		matched := false
		negated := false
		for _, pat := range patterns {
			if strings.HasPrefix(pat, "!") {
				if wildcardMatch(pat[1:], name) {
					negated = true
				}
				continue
			}
			if wildcardMatch(pat, name) {
				matched = true
			}
		}
		if matched && !negated {
			return true
		}
	}
	return false
}

// wildcardMatch supports OpenSSH '*' and '?' wildcards.
func wildcardMatch(pattern, s string) bool {
	pIdx, sIdx := 0, 0
	starIdx, matchIdx := -1, 0
	for sIdx < len(s) {
		if pIdx < len(pattern) && (pattern[pIdx] == '?' || pattern[pIdx] == s[sIdx]) {
			pIdx++
			sIdx++
		} else if pIdx < len(pattern) && pattern[pIdx] == '*' {
			starIdx = pIdx
			matchIdx = sIdx
			pIdx++
		} else if starIdx != -1 {
			pIdx = starIdx + 1
			matchIdx++
			sIdx = matchIdx
		} else {
			return false
		}
	}
	for pIdx < len(pattern) && pattern[pIdx] == '*' {
		pIdx++
	}
	return pIdx == len(pattern)
}

// ParseJumpSpec parses a single ProxyJump hop like "user@host:2222" or "bastion".
func ParseJumpSpec(spec string) (user, host string, port int) {
	s := strings.TrimSpace(spec)
	if at := strings.LastIndex(s, "@"); at > 0 {
		user = s[:at]
		s = s[at+1:]
	}
	// Handle IPv6 [addr]:port or host:port
	if strings.HasPrefix(s, "[") {
		if closeIdx := strings.Index(s, "]"); closeIdx > 0 {
			host = s[1:closeIdx]
			rest := s[closeIdx+1:]
			if strings.HasPrefix(rest, ":") {
				if p, err := strconv.Atoi(rest[1:]); err == nil && p > 0 {
					port = p
				}
			}
			return user, host, port
		}
	}
	if colon := strings.LastIndex(s, ":"); colon > 0 {
		if p, err := strconv.Atoi(s[colon+1:]); err == nil && p > 0 {
			port = p
			host = s[:colon]
			return user, host, port
		}
	}
	return user, s, port
}
