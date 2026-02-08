package cmd

import (
	"strconv"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"time"
  "syscall"
	"golang.org/x/term"
	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
)
var port int 
var host string
var username string
var inventory bool
var backup bool
var restore bool
var file string
var dbName string
var password string = ""
var askedPass bool = false

var mysqlCmd = &cobra.Command{
	Use: "mysql",
	Short: "Module to Inventory Mysql.",
	Long: `This command contains all functionality related to Mysql databases.

This Module Contains the Following Functionality:
- Backup a Database
- Restore a Database
- Inventory a Database`,	
	RunE: runCmd,
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(mysqlCmd)
	mysqlCmd.Flags().IntVarP(&port, "port", "p", 3306, "Port to Connect to")
	mysqlCmd.Flags().StringVarP(&host, "host", "H", "127.0.0.1", "Host to Connect to")
	mysqlCmd.Flags().StringVarP(&username, "username", "u", "root", "User to Connect as")
	mysqlCmd.Flags().BoolVarP(&inventory, "inventory", "i", false, "Should run Inventory Check")
	mysqlCmd.Flags().BoolVarP(&backup, "backup", "b", false, "Should Backup")
	mysqlCmd.Flags().BoolVarP(&restore, "restore", "r", false, "Should Restore")
	mysqlCmd.Flags().StringVarP(&file, "file", "f", "", "File to Use for Backup/Restore")
	mysqlCmd.Flags().StringVarP(&dbName, "dbName", "n", "", "Database name to Connect to")

	mysqlCmd.MarkFlagsMutuallyExclusive("backup", "restore")	
}

func runCmd(cmd *cobra.Command, args []string) error {
 
	var didGetFlag = false
	if cmd.Flags().Changed("inventory") {
		runInventory()
		didGetFlag = true 
	}	

	if  cmd.Flags().Changed("backup"){
		runBackup()
		didGetFlag = true
	} else if  cmd.Flags().Changed("restore"){
		runRestore()
		didGetFlag = true
	}

	if !didGetFlag {
		fmt.Println("This command must be run with -i, -b, or -r")
	}
	return nil
}

func runInventory(){}

func runBackup(){
	if len(file) == 0 {
		fmt.Println("This command requires -f to be specified")
		return
	} else if !CheckCliCmdExist("mysqldump") {
		fmt.Println("This command requires mysqldump to be in path")
		return
	}
	password, err := getPassword()
	if err != nil {
		fmt.Errorf("failed to read password")
		return 	
	}

	ofile, err := os.Create(file)
	if err != nil {
		fmt.Errorf("%w", err)
		return
	}
	defer ofile.Close()

	cmd := exec.Command("mysqldump",
			"-u", username,
			"-p"+password,
			"-h", host,
			"-P", strconv.Itoa(port),
			"--all-databases",
			"--events",
			"--routines",
			"--single-transaction",
	)

	cmd.Stdout = ofile
	cmd.Stderr = os.Stderr

	fmt.Printf("Starting Full Mysql backup from %s:%d...\n", host, port)

	err = cmd.Run()
	if err != nil {
		fmt.Printf("Backup Failed: %w\n", err)
		return
	}

	fmt.Println("Backup completed successfully")
}

func CheckCliCmdExist(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func runRestore(){
		if len(file) == 0 {
		fmt.Println("This command requires -f to be specified")
		return
	} else if !CheckCliCmdExist("mysql") {
		fmt.Println("This command requires mysql to be in path")
		return
	}
	password, err := getPassword()
	if err != nil {
		fmt.Errorf("failed to read password")
		return 	

		ifile, err := os.Open(file)
		if err != nil {
			fmt.Println("Could not open specified file")
			return
		}
		defer ifile.Close()

		cmd := exec.command("mysql",
				"-u", username,
				"-p"+password,
				"-h", host,
				"-P", strconv.Itoa(port),
		)
		

		cmd.Stdin = ifile
		cmd.Stderr = os.Stderr

		fmd.Printf("Restoring backup from %s...\n", file)
		err = cmd.Run()
		if err != nil {
			fmt.Printf("Restore Failed: %w", err)
			return
		}

		fmt.Println("Restoration completed successfully")
	}
}


func runDefault() error {
	p, err := getPassword()
	if err != nil {
		return fmt.Errorf("failed to read password")
	}

	db, err := connectToDatabase(username, p, host, port, dbName)

	if err != nil {
		return fmt.Errorf("")
	}

  err = db.Ping()	
	if err != nil {
		
		return fmt.Errorf("MySQL connection failed: %v", err)
  }

  fmt.Println("MySQL connection successful!")
	defer db.Close()
	return nil
}

func connectToDatabase(user string, password string, host string, port int, dbName string) (*sql.DB, error){
	dns := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",username, password, host, port, dbName)
	db, err := sql.Open("mysql", dns)	

	if err != nil {
		return  nil, fmt.Errorf("failed to connect to database")
	}

	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(1)

	fmt.Printf("Connecting to MySQL at %s:%d...\n", host, port)
	return db, nil   
}


func getPassword() (string, error) {
 		
		if askedPass {
			return password, nil
		}

		fmt.Print("Enter Database Password: ")
    
    // syscall.Stdin is the file descriptor for standard input
    // ReadPassword disables terminal echo automatically
    bytePassword, err := term.ReadPassword(int(syscall.Stdin))
    if err != nil {
        return "", err
    }
    
    fmt.Println() // Print a newline because ReadPassword doesn't
    password = string(bytePassword)
		askedPass = true
		return string(bytePassword), nil
}
