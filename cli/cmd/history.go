package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/duchess-devops/duchess-cli/internal/config"
	"github.com/duchess-devops/duchess-cli/internal/history"
	"github.com/spf13/cobra"
)

var (
	histLimit  int
	histFailed bool
	histKind   string
)

var historyCmd = &cobra.Command{
	Use:     "history",
	Aliases: []string{"logs", "audit"},
	Short:   "View persisted execution audit history for playbooks, scripts, SSH, and file transfers",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := resolveWorkspace()
		wsCfg := config.Load(ws)
		entries, err := history.LoadRecent(wsCfg.EffectiveHistoryDir(ws), histLimit*3)
		if err != nil {
			return err
		}

		var filtered []history.Entry
		for _, e := range entries {
			if histFailed && e.Succeeded() {
				continue
			}
			if histKind != "" && !strings.EqualFold(e.Kind, histKind) {
				continue
			}
			filtered = append(filtered, e)
			if histLimit > 0 && len(filtered) >= histLimit {
				break
			}
		}

		if flagJSON {
			return printJSON(filtered)
		}
		if len(filtered) == 0 {
			fmt.Println("No execution history recorded yet.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tKIND\tSTATUS\tTITLE\tTARGET\tENGINE\tDURATION")
		for _, e := range filtered {
			st := "OK"
			if !e.Succeeded() {
				st = fmt.Sprintf("EXIT %d", e.ExitCode)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%dms\n",
				e.Timestamp.Format("2006-01-02 15:04:05"),
				strings.ToUpper(e.Kind),
				st,
				e.Title,
				e.Target,
				e.Engine,
				e.DurationMs,
			)
		}
		return w.Flush()
	},
}

func init() {
	historyCmd.Flags().IntVarP(&histLimit, "limit", "n", 25, "Maximum number of history entries to display")
	historyCmd.Flags().BoolVar(&histFailed, "failed", false, "Show only failed executions")
	historyCmd.Flags().StringVar(&histKind, "kind", "", "Filter by kind (playbook, script, ssh, ops, cp)")
	rootCmd.AddCommand(historyCmd)
}
