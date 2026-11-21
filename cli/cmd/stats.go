package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/duchess-devops/duchess-cli/internal/sshclient"
	"github.com/duchess-devops/duchess-cli/internal/telemetry"
	"github.com/spf13/cobra"
)

var watchInterval int

var statsCmd = &cobra.Command{
	Use:   "stats [target]",
	Short: "Fetch live server CPU, RAM, disk, and network stats via SSH",
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

		results := collectStatsForHosts(cmd.Context(), hosts)
		if flagJSON {
			return printJSON(results)
		}
		printStatsTable(results)
		return nil
	},
}

var watchCmd = &cobra.Command{
	Use:   "watch [target]",
	Short: "Continuously stream live server stats over SSH",
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
		for {
			fmt.Print("\033[H\033[2J")
			fmt.Printf("Duchess Live Watch (%s) — refresh every %ds (Ctrl+C to exit)\n\n", time.Now().Format("15:04:05"), watchInterval)
			results := collectStatsForHosts(cmd.Context(), hosts)
			printStatsTable(results)
			select {
			case <-cmd.Context().Done():
				return nil
			case <-time.After(time.Duration(watchInterval) * time.Second):
			}
		}
	},
}

func collectStatsForHosts(ctx context.Context, hosts []*models.Host) []*models.ServerStats {
	opts := sshOptions()
	results := make([]*models.ServerStats, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(idx int, host *models.Host) {
			defer wg.Done()
			cCtx, cancel := context.WithTimeout(ctx, opts.Timeout+5*time.Second)
			defer cancel()
			client, err := sshclient.Connect(cCtx, host, opts)
			if err != nil {
				results[idx] = &models.ServerStats{
					HostName:    host.Name,
					Address:     host.Address,
					CollectedAt: time.Now(),
					Error:       err.Error(),
				}
				return
			}
			defer client.Close()
			results[idx] = telemetry.CollectStats(cCtx, client, host)
		}(i, h)
	}
	wg.Wait()
	return results
}

func printStatsTable(results []*models.ServerStats) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tADDRESS\tSTATUS\tOS\tCPU%\tLOAD(1/5/15)\tMEMORY\tROOT DISK\tUPTIME")
	for _, st := range results {
		if st.Error != "" {
			fmt.Fprintf(w, "%s\t%s\tOFFLINE\t-\t-\t-\t-\t-\t%s\n", st.HostName, st.Address, st.Error)
			continue
		}
		rootDisk := "-"
		for _, d := range st.Disks {
			if d.MountPoint == "/" || rootDisk == "-" {
				rootDisk = fmt.Sprintf("%.0f%% (%s/%s)", d.UsePercent, formatBytes(d.UsedBytes), formatBytes(d.TotalBytes))
				if d.MountPoint == "/" {
					break
				}
			}
		}
		mem := fmt.Sprintf("%.1f%% (%s/%s)", st.MemUsagePercent, formatBytes(st.MemUsedBytes), formatBytes(st.MemTotalBytes))
		load := fmt.Sprintf("%.2f/%.2f/%.2f (%dc)", st.Load1, st.Load5, st.Load15, st.CPUCores)
		fmt.Fprintf(w, "%s\t%s\tONLINE\t%s\t%.1f%%\t%s\t%s\t%s\t%s\n",
			st.HostName, st.Address, st.OS, st.CPUUsagePercent, load, mem, rootDisk, st.UptimeHuman)
	}
	_ = w.Flush()
}

func init() {
	watchCmd.Flags().IntVar(&watchInterval, "interval", 5, "Refresh interval in seconds")
	rootCmd.AddCommand(statsCmd, watchCmd)
}
