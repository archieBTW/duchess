package sshclient

import (
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// StartEphemeralAgent decrypts any passphrase-protected local SSH private keys
// using opts.KeyPassphrase, DUCHESS_SSH_KEY_PASSPHRASE, or opts.Password and serves
// them over a temporary Unix domain socket (mode 0700 dir / 0600 socket) compatible
// with OpenSSH / ansible-playbook / rsync via SSH_AUTH_SOCK.
//
// Returns sockPath="" (and a no-op cleanup) if no encrypted local keys were unlocked.
// usedSessionPass is true when opts.Password successfully decrypted a private key
// (so callers know not to pass it as ansible_password / sshpass).
func StartEphemeralAgent(opts Options) (sockPath string, usedSessionPass bool, cleanup func(), err error) {
	noop := func() {}

	var passphrases []string
	if opts.KeyPassphrase != "" {
		passphrases = append(passphrases, opts.KeyPassphrase)
	}
	if envPass := os.Getenv("DUCHESS_SSH_KEY_PASSPHRASE"); envPass != "" {
		passphrases = append(passphrases, envPass)
	}
	if opts.Password != "" {
		passphrases = append(passphrases, opts.Password)
	}
	if len(passphrases) == 0 {
		return "", false, noop, nil
	}

	var keyPaths []string
	seen := make(map[string]bool)
	addPath := func(p string) {
		if p == "" {
			return
		}
		exp := inventory.ExpandHome(p)
		if !seen[exp] {
			seen[exp] = true
			keyPaths = append(keyPaths, exp)
		}
	}
	addPath(opts.PrivateKey)
	if home, hErr := os.UserHomeDir(); hErr == nil {
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			addPath(filepath.Join(home, ".ssh", name))
		}
	}

	keyring := agent.NewKeyring()
	decryptedCount := 0
	var unencryptedKeys []struct {
		raw     any
		comment string
	}

	for _, kp := range keyPaths {
		data, rErr := os.ReadFile(kp)
		if rErr != nil {
			continue
		}
		raw, pErr := ssh.ParseRawPrivateKey(data)
		if pErr == nil {
			unencryptedKeys = append(unencryptedKeys, struct {
				raw     any
				comment string
			}{raw: raw, comment: kp})
			continue
		}
		var passMissing *ssh.PassphraseMissingError
		if !errors.As(pErr, &passMissing) {
			continue
		}
		for _, p := range passphrases {
			if dec, dErr := ssh.ParseRawPrivateKeyWithPassphrase(data, []byte(p)); dErr == nil {
				if addErr := keyring.Add(agent.AddedKey{PrivateKey: dec, Comment: kp}); addErr == nil {
					decryptedCount++
					if opts.Password != "" && p == opts.Password {
						usedSessionPass = true
					}
				}
				break
			}
		}
	}

	if decryptedCount == 0 {
		return "", false, noop, nil
	}

	// Also include any unencrypted local keys so OpenSSH can try all local identities
	for _, uk := range unencryptedKeys {
		_ = keyring.Add(agent.AddedKey{PrivateKey: uk.raw, Comment: uk.comment})
	}

	tmpDir, err := os.MkdirTemp("", "duchess-agent-*")
	if err != nil {
		return "", false, noop, err
	}
	_ = os.Chmod(tmpDir, 0o700)

	sock := filepath.Join(tmpDir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", false, noop, err
	}
	_ = os.Chmod(sock, 0o600)

	go func() {
		for {
			conn, aErr := ln.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = agent.ServeAgent(keyring, c)
			}(conn)
		}
	}()

	cleanup = func() {
		_ = ln.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return sock, usedSessionPass, cleanup, nil
}
