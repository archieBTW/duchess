package cmd

import (
	"github.com/duchess-devops/duchess-cli/internal/tui"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:     "tui",
	Aliases: []string{"dashboard", "ui"},
	Short:   "Launch the interactive ASCII DevOps TUI dashboard (default when running `duchess` with no args)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return launchTUI()
	},
}

func launchTUI() error {
	ws := resolveWorkspace()
	wsCfg := loadWorkspaceConfig()
	if flagDefaultVenv != "" {
		wsCfg.AnsibleVenv = flagDefaultVenv
	}
	opts := sshOptions()
	return tui.Run(tui.Config{
		WorkspaceDir:      ws,
		InventoryPath:     resolveInventoryPath(),
		SSHOptions:        opts,
		WorkspaceConfig:   wsCfg,
		PromptSSHOnLaunch: flagSSHUser == "" && wsCfg.SSH.User == "" && !flagNoPrompt,
	})
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
