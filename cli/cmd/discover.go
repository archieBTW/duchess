package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
	"github.com/duchess-devops/duchess-cli/internal/telemetry"
	"github.com/spf13/cobra"
)

var discoverCmd = &cobra.Command{
	Use:   "discover [target]",
	Short: "Auto-detect containers (Docker/Podman), services, and hosted websites/apps (Nginx, Apache, Tomcat, Caddy, HAProxy)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := "all"
		if len(args) > 0 {
			target = args[0]
		}
		inv, _, err := loadInventory()
		if err != nil {
			return err
		}
		hosts := inventory.ResolveTargets(inv, target)
		if len(hosts) == 0 {
			return fmt.Errorf("no hosts matched target %q", target)
		}

		opts := sshOptions()
		reports := make([]*models.DiscoveryReport, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func(idx int, host *models.Host) {
				defer wg.Done()
				cCtx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout+15*time.Second)
				defer cancel()
				client, err := sshclient.Connect(cCtx, host, opts)
				if err != nil {
					reports[idx] = &models.DiscoveryReport{
						HostName:    host.Name,
						Address:     host.Address,
						CollectedAt: time.Now(),
						Error:       err.Error(),
					}
					return
				}
				defer client.Close()
				reports[idx] = telemetry.Discover(cCtx, client, host)
			}(i, h)
		}
		wg.Wait()

		if flagJSON {
			return printJSON(reports)
		}

		for _, r := range reports {
			fmt.Printf("====================================================================\n")
			fmt.Printf("HOST: %s (%s)\n", r.HostName, r.Address)
			fmt.Printf("====================================================================\n")
			if r.Error != "" {
				fmt.Printf("  ERROR: %s\n\n", r.Error)
				continue
			}
			if r.Stats != nil {
				fmt.Printf("  OS: %s (%s %s) | Uptime: %s | CPU: %.1f%% (%d cores) | RAM: %.1f%% (%s/%s)\n\n",
					r.Stats.OS, r.Stats.Kernel, r.Stats.Arch, r.Stats.UptimeHuman,
					r.Stats.CPUUsagePercent, r.Stats.CPUCores, r.Stats.MemUsagePercent,
					formatBytes(r.Stats.MemUsedBytes), formatBytes(r.Stats.MemTotalBytes))
			}

			fmt.Printf("  [CONTAINERS: %d]\n", len(r.Containers))
			if len(r.Containers) > 0 {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "  RUNTIME\tNAME\tIMAGE\tSTATE\tCPU\tMEM\tPORTS")
				for _, c := range r.Containers {
					fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						c.Runtime, c.Name, c.Image, c.State, c.CPUPerc, c.MemUsage, c.Ports)
				}
				_ = w.Flush()
			} else {
				fmt.Println("  (none detected)")
			}

			fmt.Printf("\n  [HOSTED WEBSITES / APPS: %d]\n", len(r.WebApps))
			if len(r.WebApps) > 0 {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "  SERVER\tAPP/SITE\tDOMAINS\tPORTS\tSSL\tTARGET/ROOT")
				for _, a := range r.WebApps {
					targetStr := a.ProxyTarget
					if targetStr == "" {
						targetStr = a.RootOrPath
					}
					sslStr := "no"
					if a.SSL {
						sslStr = "yes"
					}
					fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
						a.ServerType, a.Name, strings.Join(a.Domains, ","), strings.Join(a.Ports, ","), sslStr, targetStr)
				}
				_ = w.Flush()
			} else {
				fmt.Println("  (none detected)")
			}

			fmt.Printf("\n  [ACTIVE/FAILED SERVICES: %d]\n", len(r.Services))
			if len(r.Services) > 0 {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "  SERVICE\tSTATE\tPORTS\tDESCRIPTION")
				limit := len(r.Services)
				if limit > 25 {
					limit = 25
				}
				for _, s := range r.Services[:limit] {
					fmt.Fprintf(w, "  %s\t%s/%s\t%s\t%s\n",
						s.Name, s.ActiveState, s.SubState, strings.Join(s.Ports, ","), s.Description)
				}
				_ = w.Flush()
				if len(r.Services) > limit {
					fmt.Printf("  ... and %d more (use --json for full list)\n", len(r.Services)-limit)
				}
			} else {
				fmt.Println("  (none detected)")
			}
			fmt.Println()
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(discoverCmd)
}
