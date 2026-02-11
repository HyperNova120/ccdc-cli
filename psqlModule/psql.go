package psqlModule

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"ccdc-cli/utils"

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

func runInventory() {
	panic("unimplemented")
}

func runRestore() {
	if !utils.CheckCliCmdExist("psql") {
		fmt.Println("This command requires 'psql' to be in path")
		return
	} else if len(file) == 0 {
		fmt.Println("This command requires the -f flag to be set")
		return
	}

	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("Failed to read password!")
		return
	}

	ifile, err := os.Open(file)
	if err != nil {
		fmt.Printf("Failed to open backup file: %v", err)
		return
	}
	defer ifile.Close()

	cmd := exec.Command("psql",
		"-h", host,
		"-p", strconv.Itoa(port),
		"-U", username,
		"-d", "postgres")

	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", password))

	cmd.Stdin = ifile
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	fmt.Printf("Starting full restoration frum %s\n", file)
	if err := cmd.Run(); err != nil {
		fmt.Printf("Restore failed: %v\n", err)
		return
	}
	fmt.Println("Restoration completed successfully!")
}

func runBackup() {
	if !utils.CheckCliCmdExist("pg_dumpall") {
		fmt.Println("This command requires 'pg_dumpall' to be in path")
		return
	} else if len(file) == 0 {
		fmt.Println("This command requires the -f flag to be set")
		return
	}

	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("Failed to read password!")
		return
	}

	cmd := exec.Command("pg_dumpall",
		"-h", host,
		"-p", strconv.Itoa(port),
		"-U", username)

	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", password))

	ofile, err := os.Create(file)
	if err != nil {
		fmt.Printf("Failed to create backup file: %v\n", err)
		return
	}
	defer ofile.Close()

	cmd.Stdout = ofile
	cmd.Stderr = os.Stderr

	fmt.Printf("Backing up instance from %s:%d", host, port)
	if err := cmd.Run(); err != nil {
		fmt.Printf("Backup Failed: %v\n", err)
		os.Remove(file)
		return
	}

	fmt.Printf("Created Backup: %s\n", file)
}
