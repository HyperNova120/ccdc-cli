package psqlModule

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	port      int
	host      string
	username  string
	inventory bool
	backup    bool
	restore   bool
	file      string
)

func GetpsqlCmd() *cobra.Command {
	psqlCmd := &cobra.Command{
		Use:   "psql",
		Short: "Module to Inventory PostreSQL.",
		Long: `This command contains all functionality related to PostreSQL databases.

This Module Contains the Following Functionality:
- Backup a Database
- Restore a Database
- Inventory a Database

This Command must be run with any of the following flags: -irb`,
		RunE:         runCmd,
		SilenceUsage: true,
	}
	psqlCmd.Flags().IntVarP(&port, "port", "p", 5432, "Port to Connect to")
	psqlCmd.Flags().StringVarP(&host, "host", "H", "127.0.0.1", "Host to Connect to")
	psqlCmd.Flags().StringVarP(&username, "username", "u", "postgres", "User to Connect as")
	psqlCmd.Flags().BoolVarP(&inventory, "inventory", "i", false, "Should run Inventory Check")
	psqlCmd.Flags().BoolVarP(&backup, "backup", "b", false, "Should Backup")
	psqlCmd.Flags().BoolVarP(&restore, "restore", "r", false, "Should Restore")
	psqlCmd.Flags().StringVarP(&file, "file", "f", "", "File to Use for Backup/Restore")

	psqlCmd.MarkFlagsMutuallyExclusive("backup", "restore")
	return psqlCmd
}

func runCmd(cmd *cobra.Command, args []string) error {
	didGetFlag := false
	if cmd.Flags().Changed("inventory") {
		runInventory()
		didGetFlag = true
	}

	if cmd.Flags().Changed("backup") {
		runBackup()
		didGetFlag = true
	} else if cmd.Flags().Changed("restore") {
		runRestore()
		didGetFlag = true
	}

	if !didGetFlag {
		fmt.Println("This command must be run with -i, -b, or -r")
	}
	return nil
}

func runRestore() {
	panic("unimplemented")
}

func runBackup() {
	panic("unimplemented")
}

func runInventory() {
	panic("unimplemented")
}
