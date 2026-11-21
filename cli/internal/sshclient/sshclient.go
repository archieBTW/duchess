package sshclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Options overrides connection parameters for SSH sessions.
type Options struct {
	User           string
	Port           int
	PrivateKey     string
	Password       string
	KeyPassphrase  string
	JumpHost       string
	UseSSHConfig   bool
	SSHConfigPath  string
	StrictHostKey  bool
	KnownHostsPath string
	Timeout        time.Duration
}

// Client wraps an active SSH connection to a remote host (plus any ProxyJump hops).
type Client struct {
	host  *models.Host
	conn  *ssh.Client
	jumps []*ssh.Client
}

// Connect establishes an SSH connection to host using ~/.ssh/config, ProxyJump bastions,
// SSH agent, private keys (including passphrase-protected keys), or password authentication.
func Connect(ctx context.Context, host *models.Host, opts Options) (*Client, error) {
	return connectWithDepth(ctx, host, opts, 0)
}

func connectWithDepth(ctx context.Context, host *models.Host, opts Options, depth int) (*Client, error) {
	if depth > 5 {
		return nil, fmt.Errorf("ProxyJump chain too deep (>5 hops)")
	}

	// Resolve ~/.ssh/config unless explicitly disabled (default enabled when SSHConfigPath != "none")
	var cfgMatch SSHConfigMatch
	if opts.UseSSHConfig || (opts.SSHConfigPath != "none" && opts.SSHConfigPath != "") {
		cfgMatch = ResolveSSHConfig(opts.SSHConfigPath, host.Name, host.Address)
	} else if opts.SSHConfigPath == "" {
		// Always allow ~/.ssh/config lookup by default for convenient bastion & IdentityFile resolution
		cfgMatch = ResolveSSHConfig("", host.Name, host.Address)
	}

	address := host.Address
	if address == "" {
		address = host.Name
	}
	if cfgMatch.HostName != "" && (address == host.Name || address == "") {
		address = cfgMatch.HostName
	}

	user := host.User
	if cfgMatch.User != "" && (user == "" || user == inventory.DefaultSSHUser()) {
		user = cfgMatch.User
	}
	if opts.User != "" {
		user = opts.User
	}
	if user == "" {
		user = inventory.DefaultSSHUser()
	}

	port := host.Port
	if cfgMatch.Port > 0 && (port == 0 || port == 22) {
		port = cfgMatch.Port
	}
	if opts.Port > 0 {
		port = opts.Port
	}
	if port == 0 {
		port = 22
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	pass := host.Password
	if opts.Password != "" {
		pass = opts.Password
	}

	// Candidate passphrases for encrypted private keys: explicit KeyPassphrase > env > session password
	var passphrases []string
	for _, p := range []string{opts.KeyPassphrase, os.Getenv("DUCHESS_SSH_KEY_PASSPHRASE"), pass} {
		if p != "" {
			passphrases = append(passphrases, p)
		}
	}

	// Collect ALL public-key signers into ONE ssh.PublicKeys AuthMethod.
	// golang.org/x/crypto/ssh only invokes a single AuthMethod per method name
	// ("publickey"), so appending multiple ssh.PublicKeys / PublicKeysCallback
	// entries would cause all entries after the first to be silently skipped.
	var allSigners []ssh.Signer
	seenPub := make(map[string]bool)
	seenPaths := make(map[string]bool)
	var keyNotes []string

	addSigner := func(s ssh.Signer) {
		if s == nil {
			return
		}
		pubKey := string(s.PublicKey().Marshal())
		if seenPub[pubKey] {
			return
		}
		seenPub[pubKey] = true
		allSigners = append(allSigners, s)
	}

	tryKey := func(path string) {
		if path == "" {
			return
		}
		expanded := inventory.ExpandHome(path)
		if seenPaths[expanded] {
			return
		}
		seenPaths[expanded] = true
		signer, err := loadPrivateKey(expanded, passphrases...)
		if err == nil {
			addSigner(signer)
			return
		}
		var passMissing *ssh.PassphraseMissingError
		if errors.As(err, &passMissing) {
			keyNotes = append(keyNotes, fmt.Sprintf("%s requires a passphrase (load into ssh-agent, enter in SSH modal, or set DUCHESS_SSH_KEY_PASSPHRASE)", filepath.Base(expanded)))
		}
	}

	// 1. Explicit private key (CLI/TUI override > inventory > ~/.ssh/config IdentityFile)
	if opts.PrivateKey != "" {
		tryKey(opts.PrivateKey)
	}
	if host.PrivateKey != "" {
		tryKey(host.PrivateKey)
	}
	for _, idFile := range cfgMatch.IdentityFiles {
		tryKey(idFile)
	}

	// 2. SSH Agent (keep agentConn open until handshake finishes)
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if agentConn, err := net.Dial("unix", sock); err == nil {
			defer agentConn.Close()
			ag := agent.NewClient(agentConn)
			if agentSigners, err := ag.Signers(); err == nil {
				for _, s := range agentSigners {
					addSigner(s)
				}
			}
		}
	}

	// 3. Default SSH keys in ~/.ssh
	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			tryKey(filepath.Join(home, ".ssh", name))
		}
	}

	var authMethods []ssh.AuthMethod
	if len(allSigners) > 0 {
		authMethods = append(authMethods, ssh.PublicKeys(allSigners...))
	}

	// 4. Password & keyboard-interactive fallback
	if pass != "" {
		authMethods = append(authMethods, ssh.Password(pass))
		authMethods = append(authMethods, ssh.KeyboardInteractive(func(u, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = pass
			}
			return answers, nil
		}))
	}

	hostKeyCB, err := buildHostKeyCallback(opts)
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCB,
		Timeout:         timeout,
	}

	// Resolve ProxyJump (CLI/TUI override > inventory host var > ~/.ssh/config)
	jumpSpec := opts.JumpHost
	if jumpSpec == "" {
		jumpSpec = host.JumpHost
	}
	if jumpSpec == "" {
		jumpSpec = cfgMatch.ProxyJump
	}

	addr := fmt.Sprintf("%s:%d", address, port)
	var rawConn net.Conn
	var jumpClients []*ssh.Client

	if jumpSpec != "" && !strings.EqualFold(jumpSpec, "none") {
		hops := strings.Split(jumpSpec, ",")
		firstHop := strings.TrimSpace(hops[0])
		remaining := ""
		if len(hops) > 1 {
			remaining = strings.Join(hops[1:], ",")
		}
		jUser, jHost, jPort := ParseJumpSpec(firstHop)
		jumpHostModel := &models.Host{
			Name:    jHost,
			Address: jHost,
			User:    jUser,
			Port:    jPort,
		}
		jumpOpts := opts
		jumpOpts.JumpHost = remaining
		// Don't force target-only password onto jump host unless user explicitly set it
		if jUser != "" {
			jumpOpts.User = jUser
		}
		if jPort > 0 {
			jumpOpts.Port = jPort
		} else {
			jumpOpts.Port = 0
		}

		jumpClient, err := connectWithDepth(ctx, jumpHostModel, jumpOpts, depth+1)
		if err != nil {
			return nil, fmt.Errorf("ssh ProxyJump %s for %s: %w", firstHop, host.Name, err)
		}
		jumpClients = append(jumpClients, jumpClient.jumps...)
		jumpClients = append(jumpClients, jumpClient.conn)

		tunConn, err := jumpClient.conn.Dial("tcp", addr)
		if err != nil {
			for i := len(jumpClients) - 1; i >= 0; i-- {
				_ = jumpClients[i].Close()
			}
			return nil, fmt.Errorf("ssh tunnel via %s to %s (%s): %w", firstHop, host.Name, addr, err)
		}
		rawConn = tunConn
	} else {
		dialer := net.Dialer{Timeout: timeout}
		rawConn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("ssh dial %s (%s): %w", host.Name, addr, err)
		}
	}

	c, chans, reqs, err := ssh.NewClientConn(rawConn, addr, cfg)
	if err != nil {
		rawConn.Close()
		for i := len(jumpClients) - 1; i >= 0; i-- {
			_ = jumpClients[i].Close()
		}
		hint := ""
		if len(keyNotes) > 0 {
			hint = " [" + strings.Join(keyNotes, "; ") + "]"
		}
		return nil, fmt.Errorf("ssh handshake %s (%s@%s): %w%s", host.Name, user, addr, err, hint)
	}

	return &Client{
		host:  host,
		conn:  ssh.NewClient(c, chans, reqs),
		jumps: jumpClients,
	}, nil
}

func buildHostKeyCallback(opts Options) (ssh.HostKeyCallback, error) {
	if !opts.StrictHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	khPath := opts.KnownHostsPath
	if khPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve known_hosts path: %w", err)
		}
		khPath = filepath.Join(home, ".ssh", "known_hosts")
	} else {
		khPath = inventory.ExpandHome(khPath)
	}
	cb, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts (%s): %w", khPath, err)
	}
	return cb, nil
}

// Close closes the underlying SSH connection and any ProxyJump bastion connections.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	if c.conn != nil {
		firstErr = c.conn.Close()
	}
	for i := len(c.jumps) - 1; i >= 0; i-- {
		if c.jumps[i] != nil {
			if err := c.jumps[i].Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Run executes a command on the remote host and returns stdout, stderr, and exit code.
func (c *Client) Run(ctx context.Context, cmd string) (stdout string, stderr string, exitCode int, err error) {
	return c.RunStreaming(ctx, cmd, nil)
}

// RunStreaming executes a command on the remote host, invoking onLine for each stdout/stderr
// line as it arrives while also returning the full accumulated stdout, stderr, and exit code.
func (c *Client) RunStreaming(ctx context.Context, cmd string, onLine func(stream, line string)) (stdout string, stderr string, exitCode int, err error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer session.Close()

	if onLine == nil {
		var outBuf, errBuf bytes.Buffer
		session.Stdout = &outBuf
		session.Stderr = &errBuf

		done := make(chan error, 1)
		go func() {
			done <- session.Run(cmd)
		}()

		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGKILL)
			return outBuf.String(), errBuf.String(), -1, ctx.Err()
		case runErr := <-done:
			if runErr != nil {
				if exitErr, ok := runErr.(*ssh.ExitError); ok {
					return outBuf.String(), errBuf.String(), exitErr.ExitStatus(), nil
				}
				return outBuf.String(), errBuf.String(), -1, runErr
			}
			return outBuf.String(), errBuf.String(), 0, nil
		}
	}

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return "", "", -1, err
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return "", "", -1, err
	}
	if err := session.Start(cmd); err != nil {
		return "", "", -1, err
	}

	var outBuf, errBuf bytes.Buffer
	var readWG sync.WaitGroup
	readWG.Add(2)
	go func() {
		defer readWG.Done()
		streamLines(stdoutPipe, &outBuf, "stdout", onLine)
	}()
	go func() {
		defer readWG.Done()
		streamLines(stderrPipe, &errBuf, "stderr", onLine)
	}()

	done := make(chan error, 1)
	go func() {
		readWG.Wait()
		done <- session.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		readWG.Wait()
		return outBuf.String(), errBuf.String(), -1, ctx.Err()
	case runErr := <-done:
		if runErr != nil {
			if exitErr, ok := runErr.(*ssh.ExitError); ok {
				return outBuf.String(), errBuf.String(), exitErr.ExitStatus(), nil
			}
			return outBuf.String(), errBuf.String(), -1, runErr
		}
		return outBuf.String(), errBuf.String(), 0, nil
	}
}

// RunScript streams a bash script via stdin to `bash -s` with optional env vars.
func (c *Client) RunScript(ctx context.Context, scriptContent string, envVars map[string]string) (string, string, int, error) {
	return c.RunScriptStreaming(ctx, scriptContent, envVars, nil)
}

// RunScriptStreaming streams a bash script via stdin to `bash -s` and invokes onLine live.
func (c *Client) RunScriptStreaming(ctx context.Context, scriptContent string, envVars map[string]string, onLine func(stream, line string)) (string, string, int, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer session.Close()

	var envPrefix strings.Builder
	for k, v := range envVars {
		safeVal := strings.ReplaceAll(v, "'", "'\"'\"'")
		envPrefix.WriteString(fmt.Sprintf("export %s='%s'\n", k, safeVal))
	}

	fullScript := envPrefix.String() + scriptContent
	session.Stdin = strings.NewReader(fullScript)

	if onLine == nil {
		var outBuf, errBuf bytes.Buffer
		session.Stdout = &outBuf
		session.Stderr = &errBuf

		done := make(chan error, 1)
		go func() {
			done <- session.Run("bash -s")
		}()

		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGKILL)
			return outBuf.String(), errBuf.String(), -1, ctx.Err()
		case runErr := <-done:
			if runErr != nil {
				if exitErr, ok := runErr.(*ssh.ExitError); ok {
					return outBuf.String(), errBuf.String(), exitErr.ExitStatus(), nil
				}
				return outBuf.String(), errBuf.String(), -1, runErr
			}
			return outBuf.String(), errBuf.String(), 0, nil
		}
	}

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return "", "", -1, err
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return "", "", -1, err
	}
	if err := session.Start("bash -s"); err != nil {
		return "", "", -1, err
	}

	var outBuf, errBuf bytes.Buffer
	var readWG sync.WaitGroup
	readWG.Add(2)
	go func() {
		defer readWG.Done()
		streamLines(stdoutPipe, &outBuf, "stdout", onLine)
	}()
	go func() {
		defer readWG.Done()
		streamLines(stderrPipe, &errBuf, "stderr", onLine)
	}()

	done := make(chan error, 1)
	go func() {
		readWG.Wait()
		done <- session.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		readWG.Wait()
		return outBuf.String(), errBuf.String(), -1, ctx.Err()
	case runErr := <-done:
		if runErr != nil {
			if exitErr, ok := runErr.(*ssh.ExitError); ok {
				return outBuf.String(), errBuf.String(), exitErr.ExitStatus(), nil
			}
			return outBuf.String(), errBuf.String(), -1, runErr
		}
		return outBuf.String(), errBuf.String(), 0, nil
	}
}

func streamLines(r io.Reader, buf *bytes.Buffer, stream string, onLine func(stream, line string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 32*1024), 1024*1024)
	for scanner.Scan() {
		ln := scanner.Text()
		buf.WriteString(ln)
		buf.WriteByte('\n')
		if onLine != nil {
			onLine(stream, ln)
		}
	}
}

func loadPrivateKey(path string, passphrases ...string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return signer, nil
	}
	var passMissing *ssh.PassphraseMissingError
	if errors.As(err, &passMissing) {
		for _, p := range passphrases {
			if p == "" {
				continue
			}
			if s, pErr := ssh.ParsePrivateKeyWithPassphrase(data, []byte(p)); pErr == nil {
				return s, nil
			}
		}
	}
	return nil, err
}

// Pool reuses authenticated SSH connections across periodic polling ticks so
// target servers don't perform repeated SSH handshakes or spam auth logs.
type Pool struct {
	mu      sync.Mutex
	clients map[string]*Client
}

// NewPool creates a reusable SSH connection pool.
func NewPool() *Pool {
	return &Pool{clients: make(map[string]*Client)}
}

func poolKey(host *models.Host, opts Options) string {
	u := host.User
	if opts.User != "" {
		u = opts.User
	}
	p := host.Port
	if opts.Port > 0 {
		p = opts.Port
	}
	j := host.JumpHost
	if opts.JumpHost != "" {
		j = opts.JumpHost
	}
	return fmt.Sprintf("%s@%s:%d|%s|%s|%v", u, host.Address, p, opts.PrivateKey, j, opts.StrictHostKey)
}

// Get returns a cached SSH client for host or opens a new connection.
func (p *Pool) Get(ctx context.Context, host *models.Host, opts Options) (*Client, error) {
	key := poolKey(host, opts)
	p.mu.Lock()
	existing := p.clients[key]
	p.mu.Unlock()

	if existing != nil {
		// Fast keepalive check to verify the transport is still alive
		if _, _, err := existing.conn.SendRequest("keepalive@duchess", true, nil); err == nil {
			return existing, nil
		}
		p.Invalidate(host, opts)
	}

	client, err := Connect(ctx, host, opts)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if old, ok := p.clients[key]; ok {
		_ = old.Close()
	}
	p.clients[key] = client
	p.mu.Unlock()
	return client, nil
}

// Invalidate closes and drops a cached connection for a specific host.
func (p *Pool) Invalidate(host *models.Host, opts Options) {
	key := poolKey(host, opts)
	p.mu.Lock()
	if c, ok := p.clients[key]; ok {
		_ = c.Close()
		delete(p.clients, key)
	}
	p.mu.Unlock()
}

// CloseAll closes all pooled SSH connections.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range p.clients {
		_ = c.Close()
		delete(p.clients, k)
	}
}
