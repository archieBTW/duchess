package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/duchess-devops/duchess-cli/internal/config"
	"github.com/duchess-devops/duchess-cli/internal/runner"
	"github.com/spf13/cobra"
)

var (
	venvPinPlaybook string
	venvPinServer   string
	venvPinDefault  bool
	venvPinRemote   bool
)

var venvCmd = &cobra.Command{
	Use:     "venv",
	Aliases: []string{"venvs"},
	Short:   "Manage default, per-playbook, and per-server Python virtualenvs for Ansible",
}

var venvShowCmd = &cobra.Command{
	Use:     "show",
	Aliases: []string{"list", "ls"},
	Short:   "Show configured venv pins (duchess.yml) and auto-discovered local virtualenvs",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := resolveWorkspace()
		wsCfg := config.Load(ws)
		detected := runner.DiscoverVenvs(ws)
		for i := range detected {
			detected[i].Version = runner.ProbeAnsibleVersion(detected[i].BinPath, detected[i].Path)
		}

		if flagJSON {
			return printJSON(map[string]any{
				"configPath":        wsCfg.ConfigPath,
				"defaultVenv":       wsCfg.AnsibleVenv,
				"defaultRemoteVenv": wsCfg.RemoteVenv,
				"playbookVenvs":     wsCfg.PlaybookVenvs,
				"serverVenvs":       wsCfg.ServerVenvs,
				"serverRemoteVenvs": wsCfg.ServerRemoteVenvs,
				"detectedVenvs":     detected,
			})
		}

		cfgLabel := wsCfg.ConfigPath
		if cfgLabel == "" {
			cfgLabel = "(none — run `duchess venv pin ...` to create duchess.yml)"
		}
		fmt.Printf("Config: %s\n", cfgLabel)
		fmt.Printf("Default Controller Venv: %s\n", fallbackDash(wsCfg.AnsibleVenv))
		fmt.Printf("Default Remote Venv:     %s\n\n", fallbackDash(wsCfg.RemoteVenv))

		if len(wsCfg.PlaybookVenvs) > 0 {
			fmt.Println("Pinned Playbook Venvs (playbook_venvs):")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  PLAYBOOK / GLOB\tVENV")
			keys := sortedMapKeys(wsCfg.PlaybookVenvs)
			for _, k := range keys {
				fmt.Fprintf(w, "  %s\t%s\n", k, wsCfg.PlaybookVenvs[k])
			}
			_ = w.Flush()
			fmt.Println()
		}

		if len(wsCfg.ServerVenvs) > 0 || len(wsCfg.ServerRemoteVenvs) > 0 {
			fmt.Println("Pinned Server / Group Venvs:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  SERVER / GROUP / GLOB\tCONTROLLER VENV\tREMOTE INTERPRETER VENV")
			seen := make(map[string]bool)
			var allKeys []string
			for k := range wsCfg.ServerVenvs {
				seen[k] = true
				allKeys = append(allKeys, k)
			}
			for k := range wsCfg.ServerRemoteVenvs {
				if !seen[k] {
					allKeys = append(allKeys, k)
				}
			}
			sort.Strings(allKeys)
			for _, k := range allKeys {
				fmt.Fprintf(w, "  %s\t%s\t%s\n", k, fallbackDash(wsCfg.ServerVenvs[k]), fallbackDash(wsCfg.ServerRemoteVenvs[k]))
			}
			_ = w.Flush()
			fmt.Println()
		}

		fmt.Printf("Auto-Detected Local Virtualenvs (%d):\n", len(detected))
		if len(detected) == 0 {
			fmt.Println("  (none found)")
		} else {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  LABEL\tPATH\tANSIBLE VERSION")
			for _, v := range detected {
				fmt.Fprintf(w, "  %s\t%s\t%s\n", v.Label, v.Path, fallbackDash(v.Version))
			}
			_ = w.Flush()
		}
		return nil
	},
}

var venvPinCmd = &cobra.Command{
	Use:   "pin <venv-path>",
	Short: "Pin a default Python venv globally, for a playbook (or glob), or for a server/group",
	Long: `Persist default virtualenv pins in duchess.yml:
  Pin a global default venv:     duchess venv pin ~/venvs/ansible --default
  Pin a specific playbook:       duchess venv pin ~/venvs/we-iac --playbook deploy-bybl.yml
  Pin a playbook glob pattern:   duchess venv pin ~/venvs/we-iac --playbook 'deploy-*.yml'
  Pin a server or group:         duchess venv pin ~/venvs/prod --server production
  Pin a remote host interpreter: duchess venv pin /opt/app/.venv --server prod-app-01 --remote`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		venvPath := args[0]
		if !venvPinDefault && venvPinPlaybook == "" && venvPinServer == "" {
			return fmt.Errorf("specify at least one target: --default, --playbook <name-or-glob>, or --server <host-or-group>")
		}

		ws := resolveWorkspace()
		wsCfg := config.Load(ws)
		if wsCfg.PlaybookVenvs == nil {
			wsCfg.PlaybookVenvs = make(map[string]string)
		}
		if wsCfg.ServerVenvs == nil {
			wsCfg.ServerVenvs = make(map[string]string)
		}
		if wsCfg.ServerRemoteVenvs == nil {
			wsCfg.ServerRemoteVenvs = make(map[string]string)
		}

		if venvPinDefault {
			if venvPinRemote {
				wsCfg.RemoteVenv = venvPath
			} else {
				wsCfg.AnsibleVenv = venvPath
			}
		}
		if venvPinPlaybook != "" {
			wsCfg.PlaybookVenvs[venvPinPlaybook] = venvPath
		}
		if venvPinServer != "" {
			if venvPinRemote {
				wsCfg.ServerRemoteVenvs[venvPinServer] = venvPath
			} else {
				wsCfg.ServerVenvs[venvPinServer] = venvPath
			}
		}

		savedPath, err := config.Save(ws, "", wsCfg)
		if err != nil {
			return err
		}
		fmt.Printf("Saved venv pin (%s) to %s\n", venvPath, savedPath)
		return nil
	},
}

var venvUnpinCmd = &cobra.Command{
	Use:   "unpin",
	Short: "Remove a pinned virtualenv from duchess.yml",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !venvPinDefault && venvPinPlaybook == "" && venvPinServer == "" {
			return fmt.Errorf("specify --default, --playbook <key>, or --server <key> to unpin")
		}
		ws := resolveWorkspace()
		wsCfg := config.Load(ws)
		if venvPinDefault {
			if venvPinRemote {
				wsCfg.RemoteVenv = ""
			} else {
				wsCfg.AnsibleVenv = ""
			}
		}
		if venvPinPlaybook != "" {
			delete(wsCfg.PlaybookVenvs, venvPinPlaybook)
		}
		if venvPinServer != "" {
			if venvPinRemote {
				delete(wsCfg.ServerRemoteVenvs, venvPinServer)
			} else {
				delete(wsCfg.ServerVenvs, venvPinServer)
			}
		}
		savedPath, err := config.Save(ws, "", wsCfg)
		if err != nil {
			return err
		}
		fmt.Printf("Updated venv pins in %s\n", savedPath)
		return nil
	},
}

func fallbackDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func init() {
	for _, c := range []*cobra.Command{venvPinCmd, venvUnpinCmd} {
		c.Flags().StringVar(&venvPinPlaybook, "playbook", "", "Playbook filename, stem, or glob (e.g. deploy-bybl.yml or 'deploy-*.yml')")
		c.Flags().StringVar(&venvPinServer, "server", "", "Server alias, IP, inventory group, or glob (e.g. prod-app-01 or production)")
		c.Flags().BoolVar(&venvPinDefault, "default", false, "Apply to workspace global default venv")
		c.Flags().BoolVar(&venvPinRemote, "remote", false, "Pin remote host Python interpreter venv (ansible_python_interpreter)")
	}
	venvCmd.AddCommand(venvShowCmd, venvPinCmd, venvUnpinCmd)
	rootCmd.AddCommand(venvCmd)
}
