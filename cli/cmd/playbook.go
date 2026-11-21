package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/duchess-devops/duchess-cli/internal/history"
	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/runner"
	"github.com/spf13/cobra"
)

var (
	pbLimit         string
	pbExtraVars     []string
	pbCheckMode     bool
	pbDiffMode      bool
	pbBecome        bool
	pbTags          string
	pbSkipTags      string
	pbVaultPassFile string
	pbVerbosity     int
	pbVenv          string
	pbRemoteVenv    string
	pbExecTimeout   int

	genPbTemplate   string
	genPbName       string
	genPbFile       string
	genPbHosts      string
	genPbService    string
	genPbDomain     string
	genPbUpstream   string
	genPbPackages   string
	genPbCmd        string
	genPbBecome     bool
	genPbVenv       string
	genPbRemoteVenv string
)

var playbookCmd = &cobra.Command{
	Use:     "playbook",
	Aliases: []string{"pb"},
	Short:   "List, add, generate, inspect venvs, and run Ansible playbooks",
}

var pbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List playbooks in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := resolveWorkspace()
		wsCfg := loadWorkspaceConfig()
		dir := filepath.Join(ws, "playbooks")
		items, err := runner.ListItems(dir, "playbook")
		if err != nil {
			return err
		}
		for i := range items {
			if items[i].Venv == "" {
				items[i].Venv = wsCfg.MatchPlaybookVenv(items[i].Path)
			}
		}
		if flagJSON {
			return printJSON(items)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "FILE\tNAME\tCATEGORY\tVENV\tDESCRIPTION")
		for _, it := range items {
			venv := it.Venv
			if venv == "" {
				venv = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", it.FileName, it.Name, it.Category, venv, it.Description)
		}
		return w.Flush()
	},
}

var pbVenvsCmd = &cobra.Command{
	Use:   "venvs",
	Short: "List auto-discovered Python virtual environments containing ansible-playbook",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := resolveWorkspace()
		venvs := runner.DiscoverVenvs(ws)
		for i := range venvs {
			venvs[i].Version = runner.ProbeAnsibleVersion(venvs[i].BinPath, venvs[i].Path)
		}
		if flagJSON {
			return printJSON(venvs)
		}
		if len(venvs) == 0 {
			fmt.Println("No Python virtualenvs with ansible-playbook detected.")
			fmt.Println("Tip: pass --venv /path/to/venv, set ansible_venv in duchess.yml, or add `# duchess:venv: /path/to/venv` to a playbook.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "LABEL\tPATH\tANSIBLE VERSION")
		for _, v := range venvs {
			ver := v.Version
			if ver == "" {
				ver = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", v.Label, v.Path, ver)
		}
		return w.Flush()
	},
}

var pbAddCmd = &cobra.Command{
	Use:   "add <file.yml>",
	Short: "Import an existing Ansible playbook into the workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := filepath.Join(resolveWorkspace(), "playbooks")
		dest, err := runner.AddFile(args[0], dir)
		if err != nil {
			return err
		}
		fmt.Printf("Added playbook: %s\n", dest)
		return nil
	},
}

var pbGenerateCmd = &cobra.Command{
	Use:     "generate",
	Aliases: []string{"gen"},
	Short:   "Generate a new Ansible playbook from a template",
	RunE: func(cmd *cobra.Command, args []string) error {
		var pkgs []string
		if genPbPackages != "" {
			pkgs = strings.Split(genPbPackages, ",")
		}
		yamlContent := runner.GeneratePlaybookYAML(runner.GeneratePlaybookOptions{
			Template:    genPbTemplate,
			Name:        genPbName,
			Hosts:       genPbHosts,
			Become:      genPbBecome,
			ServiceName: genPbService,
			Domain:      genPbDomain,
			Upstream:    genPbUpstream,
			Packages:    pkgs,
			CustomCmd:   genPbCmd,
			Venv:        genPbVenv,
			RemoteVenv:  genPbRemoteVenv,
		})

		fileName := genPbFile
		if fileName == "" {
			base := strings.ToLower(strings.ReplaceAll(genPbName, " ", "-"))
			if base == "" {
				base = genPbTemplate
			}
			fileName = base + ".yml"
		}
		if !strings.HasSuffix(fileName, ".yml") && !strings.HasSuffix(fileName, ".yaml") {
			fileName += ".yml"
		}
		dir := filepath.Join(resolveWorkspace(), "playbooks")
		dest, err := runner.SaveGenerated(dir, fileName, yamlContent)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(map[string]string{"path": dest, "content": yamlContent})
		}
		fmt.Printf("Generated playbook saved to %s\n\n%s", dest, yamlContent)
		return nil
	},
}

var pbRunCmd = &cobra.Command{
	Use:   "run <playbook-file>",
	Short: "Run an Ansible playbook against enrolled servers (with optional Python venv)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := resolveWorkspace()
		wsCfg := loadWorkspaceConfig()
		pbPath := args[0]
		if _, err := os.Stat(pbPath); os.IsNotExist(err) {
			candidate := filepath.Join(ws, "playbooks", pbPath)
			if _, err2 := os.Stat(candidate); err2 == nil {
				pbPath = candidate
			}
		}
		inv, invPath, err := loadInventory()
		if err != nil {
			return err
		}
		hosts := inventory.ResolveTargets(inv, pbLimit)
		if len(hosts) == 0 {
			return fmt.Errorf("no hosts matched limit %q", pbLimit)
		}

		explicitVenv := pbVenv
		if explicitVenv == "" {
			explicitVenv = flagDefaultVenv
		}
		remoteVenv := pbRemoteVenv
		if remoteVenv == "" {
			remoteVenv = wsCfg.MatchServerRemoteVenv(hosts, pbLimit)
		}
		if remoteVenv == "" {
			remoteVenv = wsCfg.RemoteVenv
		}

		ctx := cmd.Context()
		var cancel context.CancelFunc
		timeoutSec := pbExecTimeout
		if timeoutSec == 0 {
			timeoutSec = wsCfg.ExecTimeoutSeconds
		}
		if timeoutSec > 0 {
			ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
			defer cancel()
		}

		pbOpts := runner.PlaybookOptions{
			Venv:               explicitVenv,
			PlaybookConfigVenv: wsCfg.MatchPlaybookVenv(pbPath),
			ServerConfigVenv:   wsCfg.MatchServerVenv(hosts, pbLimit),
			ConfigVenv:         wsCfg.AnsibleVenv,
			WorkspaceDir:       ws,
			RemoteVenv:         remoteVenv,
			CheckMode:          pbCheckMode,
			DiffMode:           pbDiffMode,
			Become:             pbBecome,
			Tags:               pbTags,
			SkipTags:           pbSkipTags,
			VaultPassFile:      pbVaultPassFile,
			Verbosity:          pbVerbosity,
		}
		if !flagJSON {
			pbOpts.OnOutput = func(line string) {
				fmt.Println(line)
			}
		}

		start := time.Now()
		results, err := runner.RunPlaybookWithOptions(
			ctx,
			invPath,
			pbPath,
			pbLimit,
			hosts,
			parseKVFlags(pbExtraVars),
			pbOpts,
			sshOptions(),
		)
		if err != nil {
			recordHistory(history.Entry{
				Timestamp:  start,
				Title:      filepath.Base(pbPath),
				Kind:       "playbook",
				Target:     pbLimit,
				Engine:     "ansible-playbook",
				Venv:       explicitVenv,
				ExitCode:   -1,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      err.Error(),
			})
			return err
		}

		var outBuf, errBuf strings.Builder
		exitCode := 0
		engine := "ansible-playbook"
		usedVenv := ""
		firstErr := ""
		for _, r := range results {
			if r.Engine != "" {
				engine = r.Engine
			}
			if r.Venv != "" {
				usedVenv = r.Venv
			}
			if r.ExitCode != 0 {
				exitCode = r.ExitCode
			}
			if r.Error != "" && firstErr == "" {
				firstErr = r.Error
			}
			outBuf.WriteString(r.Stdout)
			errBuf.WriteString(r.Stderr)
		}
		recordHistory(history.Entry{
			Timestamp:  start,
			Title:      filepath.Base(pbPath),
			Kind:       "playbook",
			Target:     pbLimit,
			Engine:     engine,
			Venv:       usedVenv,
			ExitCode:   exitCode,
			DurationMs: time.Since(start).Milliseconds(),
			Stdout:     outBuf.String(),
			Stderr:     errBuf.String(),
			Error:      firstErr,
		})

		if flagJSON {
			return printJSON(results)
		}
		for _, r := range results {
			fmt.Printf("=== %s [%s] exit=%d (%dms) ===\n", r.HostName, r.Engine, r.ExitCode, r.DurationMs)
			if r.Error != "" {
				fmt.Fprintf(os.Stderr, "ERROR: %s\n", r.Error)
			}
		}
		if exitCode != 0 {
			return fmt.Errorf("playbook exited with code %d", exitCode)
		}
		return nil
	},
}

func init() {
	pbGenerateCmd.Flags().StringVarP(&genPbTemplate, "template", "t", "health-check", "Template: "+strings.Join(runner.PlaybookTemplates(), ", "))
	pbGenerateCmd.Flags().StringVarP(&genPbName, "name", "n", "Generated Playbook", "Human-readable playbook name")
	pbGenerateCmd.Flags().StringVarP(&genPbFile, "file", "f", "", "Output filename (in playbooks/)")
	pbGenerateCmd.Flags().StringVar(&genPbHosts, "hosts", "all", "Target Ansible hosts/group pattern")
	pbGenerateCmd.Flags().StringVar(&genPbService, "service", "nginx", "Target service/stack name (for systemd-service/docker-deploy)")
	pbGenerateCmd.Flags().StringVar(&genPbDomain, "domain", "app.example.com", "Domain name (for nginx-proxy)")
	pbGenerateCmd.Flags().StringVar(&genPbUpstream, "upstream", "127.0.0.1:8080", "Upstream address (for nginx-proxy)")
	pbGenerateCmd.Flags().StringVar(&genPbPackages, "packages", "htop,curl,jq", "Comma-separated packages (for package-install)")
	pbGenerateCmd.Flags().StringVar(&genPbCmd, "cmd", "", "Shell command (for custom template)")
	pbGenerateCmd.Flags().BoolVar(&genPbBecome, "become", true, "Run tasks with privilege escalation (sudo)")
	pbGenerateCmd.Flags().StringVar(&genPbVenv, "venv", "", "Pin a controller Python venv (# duchess:venv:)")
	pbGenerateCmd.Flags().StringVar(&genPbRemoteVenv, "remote-venv", "", "Pin a remote host Python venv (# duchess:remote_venv:)")

	pbRunCmd.Flags().StringVarP(&pbLimit, "limit", "l", "all", "Limit execution to specific host or group")
	pbRunCmd.Flags().StringArrayVarP(&pbExtraVars, "extra-vars", "e", nil, "Extra variables (key=value), repeatable")
	pbRunCmd.Flags().BoolVar(&pbCheckMode, "check", false, "Dry-run check mode (--check)")
	pbRunCmd.Flags().BoolVar(&pbDiffMode, "diff", false, "Show file/template diffs (--diff)")
	pbRunCmd.Flags().BoolVarP(&pbBecome, "become", "b", false, "Run operations with privilege escalation (--become)")
	pbRunCmd.Flags().StringVar(&pbTags, "tags", "", "Only run plays and tasks tagged with these values")
	pbRunCmd.Flags().StringVar(&pbSkipTags, "skip-tags", "", "Skip plays and tasks matching these tags")
	pbRunCmd.Flags().StringVar(&pbVaultPassFile, "vault-password-file", "", "Path to Ansible Vault password file")
	pbRunCmd.Flags().CountVarP(&pbVerbosity, "verbose", "v", "Increase Ansible verbosity (-v, -vv, -vvv)")
	pbRunCmd.Flags().StringVar(&pbVenv, "venv", "", "Python virtualenv path containing ansible-playbook")
	pbRunCmd.Flags().StringVar(&pbRemoteVenv, "remote-venv", "", "Remote host virtualenv path (sets ansible_python_interpreter)")
	pbRunCmd.Flags().IntVar(&pbExecTimeout, "exec-timeout", 0, "Execution timeout in seconds (0 = unlimited)")

	playbookCmd.AddCommand(pbListCmd, pbVenvsCmd, pbAddCmd, pbGenerateCmd, pbRunCmd)
	rootCmd.AddCommand(playbookCmd)
}
