package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/duchess-devops/duchess-cli/internal/inventory"
	"github.com/duchess-devops/duchess-cli/internal/models"
	"github.com/spf13/cobra"
)

var (
	addHostAddr  string
	addHostUser  string
	addHostPort  int
	addHostKey   string
	addHostGroup string
)

var inventoryCmd = &cobra.Command{
	Use:     "inventory",
	Aliases: []string{"inv", "servers"},
	Short:   "Manage enrolled servers via Ansible inventory files",
}

var invListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all enrolled servers and groups",
	RunE: func(cmd *cobra.Command, args []string) error {
		inv, path, err := loadInventory()
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(inv)
		}
		fmt.Printf("Inventory: %s (%d hosts, %d groups)\n\n", path, len(inv.Hosts), len(inv.Groups))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "HOST\tADDRESS\tUSER\tPORT\tGROUPS\tKEY")
		for _, h := range inventory.SortedHosts(inv) {
			key := h.PrivateKey
			if key == "" {
				key = "(agent/default)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
				h.Name, h.Address, h.User, h.Port, strings.Join(h.Groups, ","), key)
		}
		return w.Flush()
	},
}

var invImportCmd = &cobra.Command{
	Use:   "import <inventory-file>",
	Short: "Enroll servers from an Ansible INI or YAML inventory file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		incoming, err := inventory.Load(args[0])
		if err != nil {
			return err
		}
		inv, invPath, err := loadInventory()
		if err != nil {
			inv = inventory.New(invPath)
		}
		added := inventory.Merge(inv, incoming)
		if err := inventory.SaveINI(inv, invPath); err != nil {
			return err
		}
		if flagJSON {
			return printJSON(map[string]any{
				"importedFrom": args[0],
				"savedTo":      invPath,
				"newHosts":     added,
				"totalHosts":   len(inv.Hosts),
			})
		}
		fmt.Printf("Enrolled %d new host(s) from %s into %s (total: %d hosts)\n", added, args[0], invPath, len(inv.Hosts))
		return nil
	},
}

var invAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Enroll a single server into the Ansible inventory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		addr := addHostAddr
		if addr == "" {
			addr = name
		}
		inv, invPath, err := loadInventory()
		if err != nil {
			inv = inventory.New(invPath)
		}
		groups := strings.Split(addHostGroup, ",")
		for i := range groups {
			groups[i] = strings.TrimSpace(groups[i])
		}
		inventory.AddHost(inv, &models.Host{
			Name:       name,
			Address:    addr,
			User:       addHostUser,
			Port:       addHostPort,
			PrivateKey: addHostKey,
			Groups:     groups,
		})
		if err := inventory.SaveINI(inv, invPath); err != nil {
			return err
		}
		fmt.Printf("Enrolled %s (%s) into %s\n", name, addr, invPath)
		return nil
	},
}

var invRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a server from the Ansible inventory",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		inv, invPath, err := loadInventory()
		if err != nil {
			return err
		}
		if !inventory.RemoveHost(inv, args[0]) {
			return fmt.Errorf("host %q not found in %s", args[0], invPath)
		}
		if err := inventory.SaveINI(inv, invPath); err != nil {
			return err
		}
		fmt.Printf("Removed %s from %s\n", args[0], invPath)
		return nil
	},
}

func init() {
	invAddCmd.Flags().StringVar(&addHostAddr, "host", "", "IP address or hostname (defaults to <name>)")
	invAddCmd.Flags().StringVar(&addHostUser, "ssh-user", inventory.DefaultSSHUser(), "Ansible SSH username")
	invAddCmd.Flags().IntVar(&addHostPort, "ssh-port", 22, "Ansible SSH port")
	invAddCmd.Flags().StringVar(&addHostKey, "ssh-key", "", "Path to SSH private key")
	invAddCmd.Flags().StringVarP(&addHostGroup, "group", "g", "production", "Comma-separated inventory groups")

	inventoryCmd.AddCommand(invListCmd, invImportCmd, invAddCmd, invRemoveCmd)
	rootCmd.AddCommand(inventoryCmd)
}
