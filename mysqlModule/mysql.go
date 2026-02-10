package mysqlModule

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"ccdc-cli/utils"

	"github.com/go-sql-driver/mysql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
)

var (
	port           int
	host           string
	username       string
	inventory      bool
	backup         bool
	restore        bool
	file           string
	dbName         string = ""
	cachedPassword string = ""
	askedPass      bool   = false
)

func GetmysqlCmd() *cobra.Command {
	mysqlCmd := &cobra.Command{
		Use:   "mysql",
		Short: "Module to Inventory Mysql.",
		Long: `This command contains all functionality related to Mysql databases.

This Module Contains the Following Functionality:
- Backup a Database
- Restore a Database
- Inventory a Database

This Command must be run with any of the following flags: -irb`,
		RunE:         runCmd,
		SilenceUsage: true,
	}
	mysqlCmd.Flags().IntVarP(&port, "port", "p", 3306, "Port to Connect to")
	mysqlCmd.Flags().StringVarP(&host, "host", "H", "127.0.0.1", "Host to Connect to")
	mysqlCmd.Flags().StringVarP(&username, "username", "u", "root", "User to Connect as")
	mysqlCmd.Flags().BoolVarP(&inventory, "inventory", "i", false, "Should run Inventory Check")
	mysqlCmd.Flags().BoolVarP(&backup, "backup", "b", false, "Should Backup")
	mysqlCmd.Flags().BoolVarP(&restore, "restore", "r", false, "Should Restore")
	mysqlCmd.Flags().StringVarP(&file, "file", "f", "", "File to Use for Backup/Restore")
	// mysqlCmd.Flags().StringVarP(&dbName, "dbName", "n", "", "Database name to Connect to")

	mysqlCmd.MarkFlagsMutuallyExclusive("backup", "restore")
	return mysqlCmd
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
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("failed to read password")
		return
	}

	if anonymousLoginCheck() != nil {
		return
	}

	db, err := connectToDatabase(username, password, host, port, dbName, false)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer db.Close()

	if db.Ping() != nil {
		fmt.Printf("Error: SQL Authentication failed for %s@%s.\n", username, host)
		return
	}
	userAccountsAndAuth(db)
	userRoleMappings(db)
	userPrivileges(db)
	databaseTableInventory(db)
	securityVars(db)
}

func anonymousLoginCheck() error {
	db, err := connectToDatabase("", "", host, port, dbName, true)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer db.Close()
	utils.PrintHeader("ANONYMOUS LOGIN TEST")
	err = db.Ping()

	if err != nil {
		// fmt.Println(err)

		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) {
			if mysqlErr.Number == 1045 {
				fmt.Println("Anonymous login disabled")
				return nil
			}
		}
		fmt.Printf("Server at %s allows ANONYMOUS login.\n", host)
	} else {
		fmt.Println("Anonymous login disabled")
	}
	return nil
}

func userAccountsAndAuth(db *sql.DB) {
	utils.PrintHeader("USER ACCOUNTS & AUTHENITCATION PLUGINS")
	fmt.Printf("  %-25s | %-15s | %-15s\n", "User@Host", "Plugin", "Password Set")
	query := `
		SELECT User, Host, plugin, 
		IF(authentication_string='' OR Password='', 'NO', 'YES') 
		FROM mysql.user;`

	rows, err := db.Query(query)
	if err != nil {
		fmt.Println("Query Failed.")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var user, host, plugin, passSet string
		if err := rows.Scan(&user, &host, &plugin, &passSet); err != nil {
			fmt.Println("Error Reading Rows")
			return
		}

		userHost := fmt.Sprintf("%s@%s", user, host)
		fmt.Printf("  %-25s | %-15s | %-15s\n", userHost, plugin, passSet)
	}

	if err = rows.Err(); err != nil {
		fmt.Println("Error During Row Interation.")
	}
}

func userRoleMappings(db *sql.DB) {
	utils.PrintHeader("ROLE MAPPINGS")

	query := `SELECT User, Host, Role FROM mysql.roles_mapping;`
	rows, err := db.Query(query)
	if err != nil {
		fmt.Println("No Specific Role Mappings")
		return
	}
	defer rows.Close()

	found := false

	for rows.Next() {
		found = true
		var user, host, role string
		if err := rows.Scan(&user, &host, &role); err != nil {
			fmt.Printf("  Error Scanning Row: %s\n", err)
		}

		fmt.Printf("  - User '%s'@'%s' has role: %s\n", user, host, role)
	}

	if !found {
		fmt.Println("No Specific Roles Mapped")
	}
}

func userPrivileges(db *sql.DB) {
	utils.PrintHeader("Detailed User Privileges (GRANTS)")

	query := "SELECT User, Host FROM mysql.user"
	userRows, err := db.Query(query)
	if err != nil {
		fmt.Println("Error reading users from db")
		return
	}
	defer userRows.Close()
	for userRows.Next() {
		var user, host string
		if err := userRows.Scan(&user, &host); err != nil {
			continue
		}
		fmt.Printf("  GRANT for '%s'@'%s':\n", user, host)
		query = fmt.Sprintf("SHOW GRANTS FOR '%s'@'%s'", user, host)
		grantRows, err := db.Query(query)
		if err != nil {
			fmt.Println("    |-- [!] Could not retrieve")
			fmt.Println()
			continue
		}
		defer grantRows.Close()
		for grantRows.Next() {
			var grant string
			if err := grantRows.Scan(&grant); err != nil {
				continue
			}
			fmt.Printf("    |-- %s\n", grant)
		}
		grantRows.Close()
		fmt.Println()
	}
}

func databaseTableInventory(db *sql.DB) {
	utils.PrintHeader("Database and Table Inventory")

	query := `
									SELECT schema_name
									FROM information_schema.schemata
									WHERE schema_name NOT IN ('information_schema', 'performance_schema', 'sys', 'mysql')`

	dbRows, err := db.Query(query)
	if err != nil {
		fmt.Printf("Error fetching databases: %v\n", err)
	}
	defer dbRows.Close()

	for dbRows.Next() {
		var dbName string
		if err := dbRows.Scan(&dbName); err != nil {
			continue
		}

		var dbSize sql.NullFloat64
		sizeQuery := fmt.Sprintf(`
			SELECT ROUND (SUM(data_length + index_length) / 1024 / 1024, 2)
			FROM information_schema.tables
			WHERE table_schema='%s'`, dbName)
		err = db.QueryRow(sizeQuery).Scan(&dbSize)
		if err != nil {
			fmt.Printf("  DATABASE: %s (Size: 0 MB)\n", dbName)
		} else {
			fmt.Printf("  DATABASE: %s (Size: %.2f MB)\n", dbName, dbSize.Float64)
		}

		tableQuery := fmt.Sprintf(`
			SELECT table_name,
							COALESCE(engine, 'N/A'),
							COALESCE(table_rows, 0),
							COALESCE(create_time, 'N/A')
			FROM information_schema.tables
			WHERE table_schema='%s'`, dbName)

		tRows, err := db.Query(tableQuery)
		if err != nil {
			fmt.Printf("    |-- [1] Could not retrieve tables for %s\n", dbName)
			continue
		}
		defer tRows.Close()

		for tRows.Next() {
			var tName, tEng, tRowsCount, tDate string
			if err := tRows.Scan(&tName, &tEng, &tRowsCount, &tDate); err != nil {
				continue
			}

			fmt.Printf("    |-- %-25s | %-10s | Rows: %-8s | Created: %s\n", tName, tEng, tRowsCount, tDate)

		}
		tRows.Close()
		fmt.Println()
	}
}

func securityVars(db *sql.DB) {
	utils.PrintHeader("CRITICAL SECURITY VARIABLES")

	query := `SHOW VARIABLES WHERE Variable_name IN ('local_infile', 'skip_networking', 'have_ssl', 'version')`
	rows, err := db.Query(query)
	if err != nil {
		fmt.Printf("Error retrieving security variables", err)
		return
	}
	defer rows.Close()

	fmt.Printf("  %-25s | %-10s\n", "Variable Name", "Values")

	for rows.Next() {
		var varName, varValue string
		if err := rows.Scan(&varName, &varValue); err != nil {
			continue
		}
		fmt.Printf("  %-25s | %-10s\n", varName, varValue)
	}
}

// ===========================================================
//
//	BACKUP COMMAND
//
// ===========================================================
func runBackup() {
	if len(file) == 0 {
		fmt.Println("This command requires -f to be specified")
		return
	} else if !utils.CheckCliCmdExist("mysqldump") {
		fmt.Println("This command requires mysqldump to be in path")
		return
	}
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Printf("failed to read password")
		return
	}

	ofile, err := os.Create(file)
	if err != nil {
		fmt.Printf("%s", err)
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
		fmt.Printf("Backup Failed: %s\n", err)
		return
	}

	fmt.Println("Backup completed successfully")
}

// ===========================================================
//
//											RESTORE COMMAND
//
// ===========================================================

func runRestore() {
	if len(file) == 0 {
		fmt.Println("This command requires -f to be specified")
		return
	} else if !utils.CheckCliCmdExist("mysql") {
		fmt.Println("This command requires mysql to be in path")
		return
	}
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Printf("failed to read password")
		return
	}
	ifile, err := os.Open(file)
	if err != nil {
		fmt.Println("Could not open specified file")
		return
	}
	defer ifile.Close()

	cmd := exec.Command("mysql",
		"-u", username,
		"-p"+password,
		"-h", host,
		"-P", strconv.Itoa(port),
	)

	cmd.Stdin = ifile
	cmd.Stderr = os.Stderr

	fmt.Printf("Restoring backup from %s...\n", file)
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Restore Failed: %s", err)
		os.Remove(file)
		return
	}

	fmt.Println("Restoration completed successfully")
}

func runDefault() error {
	p, err := utils.GetPassword()
	if err != nil {
		return fmt.Errorf("failed to read password")
	}

	db, err := connectToDatabase(username, p, host, port, dbName, true)
	if err != nil {
		return fmt.Errorf("")
	}
	defer db.Close()
	err = db.Ping()
	if err != nil {
		return fmt.Errorf("MySQL connection failed: %v", err)
	}

	fmt.Println("MySQL connection successful!")

	return nil
}

func connectToDatabase(user string, password string, host string, port int, dbName string, shouldPrintConnecting bool) (*sql.DB, error) {
	dns := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true", username, password, host, port, dbName)
	db, err := sql.Open("mysql", dns)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database")
	}

	err = db.Ping()
	if err != nil {
		var netErr *net.OpError
		if errors.As(err, &netErr) {
			return nil, fmt.Errorf("DB not Listening")
		}
	}

	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(6)
	if shouldPrintConnecting {
		fmt.Printf("Connecting to MySQL at %s:%d...\n", host, port)
	}
	return db, nil
}
