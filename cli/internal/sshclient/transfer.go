package sshclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// UploadFile copies a local file to remotePath over SSH without requiring sftp-server.
// Parent directories are created automatically and file permissions are preserved.
func (c *Client) UploadFile(ctx context.Context, localPath, remotePath string) (int64, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("stat local file %s: %w", localPath, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("cannot upload directory %s (pass a file path)", localPath)
	}
	f, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("open local file %s: %w", localPath, err)
	}
	defer f.Close()

	// If remotePath ends with '/', append the local filename
	if strings.HasSuffix(remotePath, "/") {
		remotePath = remotePath + filepath.Base(localPath)
	}

	session, err := c.conn.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()

	var errBuf bytes.Buffer
	session.Stderr = &errBuf
	session.Stdin = f

	mode := fmt.Sprintf("%04o", info.Mode().Perm())
	dir := path.Dir(remotePath)
	remoteCmd := fmt.Sprintf(
		"mkdir -p %s && cat > %s && chmod %s %s",
		shellQuote(dir), shellQuote(remotePath), mode, shellQuote(remotePath),
	)

	done := make(chan error, 1)
	go func() {
		done <- session.Run(remoteCmd)
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case runErr := <-done:
		if runErr != nil {
			msg := strings.TrimSpace(errBuf.String())
			if msg != "" {
				return 0, fmt.Errorf("upload %s -> %s: %v (%s)", localPath, remotePath, runErr, msg)
			}
			return 0, fmt.Errorf("upload %s -> %s: %w", localPath, remotePath, runErr)
		}
		return info.Size(), nil
	}
}

// DownloadFile copies remotePath from the remote server to localPath over SSH.
func (c *Client) DownloadFile(ctx context.Context, remotePath, localPath string) (int64, error) {
	if st, err := os.Stat(localPath); err == nil && st.IsDir() {
		localPath = filepath.Join(localPath, path.Base(remotePath))
	} else if strings.HasSuffix(localPath, string(os.PathSeparator)) {
		localPath = filepath.Join(localPath, path.Base(remotePath))
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return 0, err
	}

	session, err := c.conn.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var errBuf bytes.Buffer
	session.Stderr = &errBuf

	if err := session.Start("cat -- " + shellQuote(remotePath)); err != nil {
		return 0, err
	}

	tmpPath := localPath + ".duchess.tmp"
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return 0, err
	}

	copyDone := make(chan struct {
		n   int64
		err error
	}, 1)
	go func() {
		n, cErr := io.Copy(outFile, stdout)
		copyDone <- struct {
			n   int64
			err error
		}{n, cErr}
	}()

	var written int64
	select {
	case <-ctx.Done():
		outFile.Close()
		_ = os.Remove(tmpPath)
		return 0, ctx.Err()
	case res := <-copyDone:
		written = res.n
		if res.err != nil {
			outFile.Close()
			_ = os.Remove(tmpPath)
			return 0, res.err
		}
	}

	if err := outFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}

	if err := session.Wait(); err != nil {
		_ = os.Remove(tmpPath)
		msg := strings.TrimSpace(errBuf.String())
		if msg != "" {
			return 0, fmt.Errorf("download %s: %v (%s)", remotePath, err, msg)
		}
		return 0, fmt.Errorf("download %s: %w", remotePath, err)
	}

	if err := os.Rename(tmpPath, localPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	return written, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
