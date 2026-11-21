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

var execCmd = &cobra.Command{
	Use:   "exec <target> <command>",
	Short: "Run an ad-hoc shell command via SSH across target servers (DevOps-only)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, remoteCmd := args[0], args[1]
		inv, _, err := loadInventory()
		if err != nil {
			return err
		}
		hosts := inventory.ResolveTargets(inv, target)
		if len(hosts) == 0 {
			return fmt.Errorf("no hosts matched target %q", target)
		}
		startAll := time.Now()
		opts := sshOptions()
		results := make([]models.ExecutionResult, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func(idx int, host *models.Host) {
				defer wg.Done()
				start := time.Now()
				cCtx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout+30*time.Second)
				defer cancel()
				client, err := sshclient.Connect(cCtx, host, opts)
				if err != nil {
					results[idx] = models.ExecutionResult{
						HostName:   host.Name,
						Address:    host.Address,
						ExitCode:   -1,
						DurationMs: time.Since(start).Milliseconds(),
						Engine:     "ssh-exec",
						Error:      err.Error(),
					}
					return
				}
				defer client.Close()
				stdout, stderr, code, runErr := client.Run(cCtx, remoteCmd)
				errMsg := ""
				if runErr != nil {
					errMsg = runErr.Error()
				}
				results[idx] = models.ExecutionResult{
					HostName:   host.Name,
					Address:    host.Address,
					ExitCode:   code,
					Stdout:     stdout,
					Stderr:     stderr,
					DurationMs: time.Since(start).Milliseconds(),
					Engine:     "ssh-exec",
					Error:      errMsg,
				}
			}(i, h)
		}
		wg.Wait()

		var outBuf, errBuf strings.Builder
		exitCode := 0
		for _, r := range results {
			if r.ExitCode != 0 {
				exitCode = r.ExitCode
			}
			outBuf.WriteString(fmt.Sprintf("=== %s (%s) exit=%d ===\n%s\n", r.HostName, r.Address, r.ExitCode, r.Stdout))
			if r.Stderr != "" {
				errBuf.WriteString(fmt.Sprintf("[%s] %s\n", r.HostName, r.Stderr))
			}
		}
		recordHistory(history.Entry{
			Timestamp:  startAll,
			Title:      remoteCmd,
			Kind:       "ssh",
			Target:     target,
			Engine:     "ssh-exec",
			ExitCode:   exitCode,
			DurationMs: time.Since(startAll).Milliseconds(),
			Stdout:     outBuf.String(),
			Stderr:     errBuf.String(),
		})

		if flagJSON {
			return printJSON(results)
		}
		for _, r := range results {
			fmt.Printf("=== %s (%s) exit=%d (%dms) ===\n", r.HostName, r.Address, r.ExitCode, r.DurationMs)
			if r.Stdout != "" {
				fmt.Println(r.Stdout)
			}
			if r.Stderr != "" {
				fmt.Fprintf(os.Stderr, "%s\n", r.Stderr)
			}
			if r.Error != "" {
				fmt.Fprintf(os.Stderr, "ERROR: %s\n", r.Error)
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(execCmd)
}
