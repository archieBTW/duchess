package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/history"
	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
	"github.com/spf13/cobra"
)

var cpCmd = &cobra.Command{
	Use:   "cp <src> <dst>",
	Short: "Copy files between local machine and enrolled servers over SSH (supports group fan-out)",
	Long: `Copy files to or from enrolled hosts over SSH (without requiring sftp-server):
  Upload to a host or group:  duchess cp ./nginx.conf webservers:/etc/nginx/conf.d/app.conf
  Download from a host:       duchess cp prod-app-01:/var/log/nginx/error.log ./error.log`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		src, dst := args[0], args[1]
		srcHost, srcPath, srcRemote := splitRemoteSpec(src)
		dstHost, dstPath, dstRemote := splitRemoteSpec(dst)

		if srcRemote == dstRemote {
			return fmt.Errorf("specify exactly one remote path (host:/path) and one local path")
		}

		inv, _, err := loadInventory()
		if err != nil {
			return err
		}
		opts := sshOptions()
		start := time.Now()

		if dstRemote {
			hosts := inventory.ResolveTargets(inv, dstHost)
			if len(hosts) == 0 {
				return fmt.Errorf("no hosts matched target %q", dstHost)
			}
			type cpResult struct {
				Host       string `json:"host"`
				Address    string `json:"address"`
				Bytes      int64  `json:"bytes"`
				RemotePath string `json:"remotePath"`
				Error      string `json:"error,omitempty"`
			}
			results := make([]cpResult, len(hosts))
			var wg sync.WaitGroup
			for i, h := range hosts {
				wg.Add(1)
				go func(idx int, host *models.Host) {
					defer wg.Done()
					cCtx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout+60*time.Second)
					defer cancel()
					client, err := sshclient.Connect(cCtx, host, opts)
					if err != nil {
						results[idx] = cpResult{Host: host.Name, Address: host.Address, RemotePath: dstPath, Error: err.Error()}
						return
					}
					defer client.Close()
					n, uErr := client.UploadFile(cCtx, src, dstPath)
					errMsg := ""
					if uErr != nil {
						errMsg = uErr.Error()
					}
					results[idx] = cpResult{Host: host.Name, Address: host.Address, Bytes: n, RemotePath: dstPath, Error: errMsg}
				}(i, h)
			}
			wg.Wait()

			var outBuf strings.Builder
			exitCode := 0
			for _, r := range results {
				if r.Error != "" {
					exitCode = 1
					outBuf.WriteString(fmt.Sprintf("FAIL %s (%s): %s\n", r.Host, r.Address, r.Error))
				} else {
					outBuf.WriteString(fmt.Sprintf("OK   %s (%s): uploaded %s -> %s (%s)\n", r.Host, r.Address, src, r.RemotePath, formatBytes(uint64(r.Bytes))))
				}
			}
			recordHistory(history.Entry{
				Timestamp:  start,
				Title:      fmt.Sprintf("cp %s -> %s", src, dst),
				Kind:       "cp",
				Target:     dstHost,
				Engine:     "ssh-cp",
				ExitCode:   exitCode,
				DurationMs: time.Since(start).Milliseconds(),
				Stdout:     outBuf.String(),
			})
			if flagJSON {
				return printJSON(results)
			}
			fmt.Print(outBuf.String())
			if exitCode != 0 {
				return fmt.Errorf("one or more uploads failed")
			}
			return nil
		}

		// Download from single host
		hosts := inventory.ResolveTargets(inv, srcHost)
		if len(hosts) == 0 {
			return fmt.Errorf("no hosts matched target %q", srcHost)
		}
		if len(hosts) > 1 {
			return fmt.Errorf("download target %q matched %d hosts; specify a single host", srcHost, len(hosts))
		}
		host := hosts[0]
		cCtx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout+60*time.Second)
		defer cancel()
		client, err := sshclient.Connect(cCtx, host, opts)
		if err != nil {
			return err
		}
		defer client.Close()
		n, err := client.DownloadFile(cCtx, srcPath, dst)
		if err != nil {
			recordHistory(history.Entry{
				Timestamp:  start,
				Title:      fmt.Sprintf("cp %s -> %s", src, dst),
				Kind:       "cp",
				Target:     host.Name,
				Engine:     "ssh-cp",
				ExitCode:   1,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      err.Error(),
			})
			return err
		}
		msg := fmt.Sprintf("Downloaded %s:%s -> %s (%s)\n", host.Name, srcPath, dst, formatBytes(uint64(n)))
		recordHistory(history.Entry{
			Timestamp:  start,
			Title:      fmt.Sprintf("cp %s -> %s", src, dst),
			Kind:       "cp",
			Target:     host.Name,
			Engine:     "ssh-cp",
			ExitCode:   0,
			DurationMs: time.Since(start).Milliseconds(),
			Stdout:     msg,
		})
		if flagJSON {
			return printJSON(map[string]any{"host": host.Name, "remotePath": srcPath, "localPath": dst, "bytes": n})
		}
		fmt.Print(msg)
		return nil
	},
}

func splitRemoteSpec(spec string) (host, remotePath string, isRemote bool) {
	if _, err := os.Stat(spec); err == nil {
		return "", spec, false
	}
	idx := strings.IndexByte(spec, ':')
	if idx <= 0 {
		return "", spec, false
	}
	// Ignore relative/absolute paths containing ':'
	if strings.Contains(spec[:idx], "/") {
		return "", spec, false
	}
	return spec[:idx], spec[idx+1:], true
}

func init() {
	rootCmd.AddCommand(cpCmd)
}
