package sshclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/models"
	"golang.org/x/crypto/ssh"
)

func TestParseSSHConfig(t *testing.T) {
	cfg := `
# Sample SSH config
Host prod-*
    User ops
    Port 2222
    IdentityFile ~/.ssh/prod_ed25519
    ProxyJump bastion.corp.internal

Host prod-app-01
    HostName 10.20.30.40
    IdentityFile ~/.ssh/app01_key

Host *
    User fallback
    Port 22
`
	m := ParseSSHConfig(cfg, "prod-app-01", "10.20.30.40")
	if m.HostName != "10.20.30.40" {
		t.Fatalf("expected HostName 10.20.30.40, got %q", m.HostName)
	}
	if m.User != "ops" {
		t.Fatalf("expected first-match User 'ops', got %q", m.User)
	}
	if m.Port != 2222 {
		t.Fatalf("expected Port 2222, got %d", m.Port)
	}
	if m.ProxyJump != "bastion.corp.internal" {
		t.Fatalf("expected ProxyJump bastion.corp.internal, got %q", m.ProxyJump)
	}
	if len(m.IdentityFiles) != 2 {
		t.Fatalf("expected 2 identity files, got %v", m.IdentityFiles)
	}
}

func TestParseJumpSpec(t *testing.T) {
	u, h, p := ParseJumpSpec("admin@bastion.example.com:2200")
	if u != "admin" || h != "bastion.example.com" || p != 2200 {
		t.Fatalf("unexpected jump parse: u=%q h=%q p=%d", u, h, p)
	}
	u2, h2, p2 := ParseJumpSpec("jumpbox")
	if u2 != "" || h2 != "jumpbox" || p2 != 0 {
		t.Fatalf("unexpected simple jump parse: u=%q h=%q p=%d", u2, h2, p2)
	}
}

func generateTestKeyPair(t *testing.T, passphrase string) (privPEM []byte, pub ssh.PublicKey) {
	t.Helper()
	pubEd, privEd, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pubEd)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(privEd, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(privEd, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), sshPub
}

func startTestSSHServer(t *testing.T, authorizedPub ssh.PublicKey) (string, int, func()) {
	t.Helper()
	hostKeyPEM, _ := generateTestKeyPair(t, "")
	hostSigner, err := ssh.ParsePrivateKey(hostKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	srvCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorizedPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, os.ErrPermission
		},
	}
	srvCfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sConn, chans, reqs, err := ssh.NewServerConn(c, srvCfg)
				if err != nil {
					return
				}
				defer sConn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.UnknownChannelType, "test")
				}
			}(conn)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, func() { _ = ln.Close() }
}

func TestConnectTriesAllPublicKeysAndPassphraseFallback(t *testing.T) {
	dir := t.TempDir()
	wrongPEM, _ := generateTestKeyPair(t, "")
	goodPEM, goodPub := generateTestKeyPair(t, "secret-pass")

	wrongPath := filepath.Join(dir, "wrong_key")
	goodPath := filepath.Join(dir, "good_key")
	if err := os.WriteFile(wrongPath, wrongPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goodPath, goodPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// Put good_key as second key via ~/.ssh/config while wrong_key is explicit PrivateKey:
	// both must be combined into ONE ssh.PublicKeys AuthMethod so good_key is actually tried!
	sshCfgPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(sshCfgPath, []byte("Host test-node\n  IdentityFile "+goodPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ip, port, stop := startTestSSHServer(t, goodPub)
	defer stop()

	host := &models.Host{
		Name:       "test-node",
		Address:    ip,
		User:       "tester",
		Port:       port,
		PrivateKey: wrongPath,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Session password also serves as fallback key passphrase for encrypted good_key
	client, err := Connect(ctx, host, Options{
		Password:      "secret-pass",
		UseSSHConfig:  true,
		SSHConfigPath: sshCfgPath,
		Timeout:       3 * time.Second,
	})
	if err != nil {
		t.Fatalf("expected Connect to try second key with passphrase fallback and succeed, got: %v", err)
	}
	_ = client.Close()
}
