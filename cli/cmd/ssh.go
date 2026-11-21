package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/spf13/cobra"
)

var sshCmd = &cobra.Command{
	Use:   "ssh <host>",
	Short: "Open an interactive SSH shell to an enrolled server using inventory credentials and jump host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		inv, _, err := loadInventory()
		if err != nil {
			return err
		}
		hosts := inventory.ResolveTargets(inv, args[0])
		if len(hosts) == 0 {
			return fmt.Errorf("no host matched %q", args[0])
		}
		host := hosts[0]
		opts := sshOptions()

		user := host.User
		if opts.User != "" {
			user = opts.User
		}
		port := host.Port
		if opts.Port > 0 {
			port = opts.Port
		}
		key := host.PrivateKey
		if opts.PrivateKey != "" {
			key = opts.PrivateKey
		}
		jump := host.JumpHost
		if opts.JumpHost != "" {
			jump = opts.JumpHost
		}

		sshArgs := []string{}
		if port > 0 && port != 22 {
			sshArgs = append(sshArgs, "-p", strconv.Itoa(port))
		}
		if key != "" {
			sshArgs = append(sshArgs, "-i", inventory.ExpandHome(key))
		}
		if jump != "" {
			sshArgs = append(sshArgs, "-J", jump)
		}
		dest := host.Address
		if user != "" {
			dest = user + "@" + dest
		}
		sshArgs = append(sshArgs, dest)

		c := exec.Command("ssh", sshArgs...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(sshCmd)
}
