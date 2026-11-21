package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/history"
	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/runner"
	"github.com/spf13/cobra"
)

var (
	scTarget string
	scEnv    []string

	genScTemplate string
	genScName     string
	genScFile     string
	genScService  string
	genScPath     string
	genScURL      string
	genScCmd      string
)

var scriptCmd = &cobra.Command{
	Use:     "script",
	Aliases: []string{"sc"},
	Short:   "List, add, generate, and run shell scripts over SSH",
}

var scListCmd = &cobra.Command{
	Use:   "list",
	Short: "List shell scripts in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := filepath.Join(resolveWorkspace(), "scripts")
		items, err := runner.ListItems(dir, "script")
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(items)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "FILE\tNAME\tCATEGORY\tDESCRIPTION")
		for _, it := range items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", it.FileName, it.Name, it.Category, it.Description)
		}
		return w.Flush()
	},
}

var scAddCmd = &cobra.Command{
	Use:   "add <file.sh>",
	Short: "Import an existing shell script into the workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := filepath.Join(resolveWorkspace(), "scripts")
		dest, err := runner.AddFile(args[0], dir)
		if err != nil {
			return err
		}
		fmt.Printf("Added script: %s\n", dest)
		return nil
	},
}

var scGenerateCmd = &cobra.Command{
	Use:     "generate",
	Aliases: []string{"gen"},
	Short:   "Generate a new shell script from a template",
	RunE: func(cmd *cobra.Command, args []string) error {
		content := runner.GenerateScriptContent(runner.GenerateScriptOptions{
			Template:    genScTemplate,
			Name:        genScName,
			ServiceName: genScService,
			TargetPath:  genScPath,
			URL:         genScURL,
			CustomCmd:   genScCmd,
		})
		fileName := genScFile
		if fileName == "" {
			base := strings.ToLower(strings.ReplaceAll(genScName, " ", "-"))
			if base == "" {
				base = genScTemplate
			}
			fileName = base + ".sh"
		}
		if !strings.HasSuffix(fileName, ".sh") {
			fileName += ".sh"
		}
		dir := filepath.Join(resolveWorkspace(), "scripts")
		dest, err := runner.SaveGenerated(dir, fileName, content)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(map[string]string{"path": dest, "content": content})
		}
		fmt.Printf("Generated script saved to %s\n\n%s", dest, content)
		return nil
	},
}

var scRunCmd = &cobra.Command{
	Use:   "run <script-file>",
	Short: "Execute a shell script on target servers over SSH",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scPath := args[0]
		if _, err := os.Stat(scPath); os.IsNotExist(err) {
			candidate := filepath.Join(resolveWorkspace(), "scripts", scPath)
			if _, err2 := os.Stat(candidate); err2 == nil {
				scPath = candidate
			}
		}
		inv, _, err := loadInventory()
		if err != nil {
			return err
		}
		hosts := inventory.ResolveTargets(inv, scTarget)
		if len(hosts) == 0 {
			return fmt.Errorf("no hosts matched target %q", scTarget)
		}
		start := time.Now()
		var outMu sync.Mutex
		var onOut func(host, stream, line string)
		if !flagJSON {
			onOut = func(host, stream, line string) {
				outMu.Lock()
				if stream == "stderr" {
					fmt.Fprintf(os.Stderr, "[%s] %s\n", host, line)
				} else {
					fmt.Printf("[%s] %s\n", host, line)
				}
				outMu.Unlock()
			}
		}
		results, err := runner.RunScriptStreaming(cmd.Context(), scPath, hosts, parseKVFlags(scEnv), sshOptions(), onOut)
		if err != nil {
			return err
		}
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
			Timestamp:  start,
			Title:      filepath.Base(scPath),
			Kind:       "script",
			Target:     scTarget,
			Engine:     "ssh",
			ExitCode:   exitCode,
			DurationMs: time.Since(start).Milliseconds(),
			Stdout:     outBuf.String(),
			Stderr:     errBuf.String(),
		})
		if flagJSON {
			return printJSON(results)
		}
		for _, r := range results {
			fmt.Printf("=== %s (%s) exit=%d (%dms) ===\n", r.HostName, r.Address, r.ExitCode, r.DurationMs)
			if r.Error != "" {
				fmt.Fprintf(os.Stderr, "ERROR: %s\n", r.Error)
			}
		}
		return nil
	},
}

func init() {
	scGenerateCmd.Flags().StringVarP(&genScTemplate, "template", "t", "diagnostics", "Template: "+strings.Join(runner.ScriptTemplates(), ", "))
	scGenerateCmd.Flags().StringVarP(&genScName, "name", "n", "Generated Script", "Human-readable script name")
	scGenerateCmd.Flags().StringVarP(&genScFile, "file", "f", "", "Output filename (in scripts/)")
	scGenerateCmd.Flags().StringVar(&genScService, "service", "nginx", "Service or container name")
	scGenerateCmd.Flags().StringVar(&genScPath, "path", "/var/cache/nginx", "Target directory path")
	scGenerateCmd.Flags().StringVar(&genScURL, "url", "http://127.0.0.1:80/", "Target URL (for http-probe)")
	scGenerateCmd.Flags().StringVar(&genScCmd, "cmd", "", "Shell command (for custom template)")

	scRunCmd.Flags().StringVarP(&scTarget, "target", "t", "all", "Target host, IP, or inventory group")
	scRunCmd.Flags().StringArrayVarP(&scEnv, "env", "e", nil, "Environment variable (KEY=VAL), repeatable")

	scriptCmd.AddCommand(scListCmd, scAddCmd, scGenerateCmd, scRunCmd)
	rootCmd.AddCommand(scriptCmd)
}
