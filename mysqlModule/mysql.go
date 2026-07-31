package mysqlModule

import (
	"database/sql"
	"errors"
	"fmt"
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
	socket         string
	username       string
	inventory      bool
	backup         bool
	restore        bool
	file           string
	dbName         string = ""
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
		SilenceErrors: true,
	}
	mysqlCmd.Flags().IntVarP(&port, "port", "p", 3306, "Port to Connect to")
	mysqlCmd.Flags().StringVarP(&host, "host", "H", "127.0.0.1", "Host to Connect to (TCP)")
	mysqlCmd.Flags().StringVarP(&socket, "socket", "S", "", "Path to a Unix socket to connect through instead of TCP (e.g. /var/run/mysqld/mysqld.sock)")
	mysqlCmd.Flags().StringVarP(&username, "username", "u", "root", "User to Connect as")
	mysqlCmd.Flags().BoolVarP(&inventory, "inventory", "i", false, "Should run Inventory Check")
	mysqlCmd.Flags().BoolVarP(&backup, "backup", "b", false, "Should Backup")
	mysqlCmd.Flags().BoolVarP(&restore, "restore", "r", false, "Should Restore")
	mysqlCmd.Flags().StringVarP(&file, "file", "f", "", "File to Use for Backup/Restore")
	// mysqlCmd.Flags().StringVarP(&dbName, "dbName", "n", "", "Database name to Connect to")

	mysqlCmd.MarkFlagsMutuallyExclusive("backup", "restore")
	mysqlCmd.MarkFlagsMutuallyExclusive("host", "socket")
	return mysqlCmd
}

func runCmd(cmd *cobra.Command, args []string) error {
	didGetFlag := false
	var firstErr error

	if cmd.Flags().Changed("inventory") {
		if err := runInventory(); err != nil {
			firstErr = err
		}
		didGetFlag = true
	}

	if cmd.Flags().Changed("backup") {
		if err := runBackup(); err != nil && firstErr == nil {
			firstErr = err
		}
		didGetFlag = true
	} else if cmd.Flags().Changed("restore") {
		if err := runRestore(); err != nil && firstErr == nil {
			firstErr = err
		}
		didGetFlag = true
	}

	if !didGetFlag {
		return fmt.Errorf("this command must be run with -i, -b, or -r")
	}
	return firstErr
}

func runInventory() error {
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("failed to read password")
		return fmt.Errorf("failed to read password: %w", err)
	}

	if err := anonymousLoginCheck(); err != nil {
		return err
	}

	db, err := connectToDatabase(username, password, host, port, dbName, false)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer db.Close()

	if pingErr := db.Ping(); pingErr != nil {
		msg := describePingError(pingErr, username)
		fmt.Printf("Error: could not connect as %s via %s: %s\n", username, connTarget(), msg)
		return fmt.Errorf("could not connect as %s via %s: %s", username, connTarget(), msg)
	}
	userAccountsAndAuth(db)
	userRoleMappings(db)
	userPrivileges(db)
	databaseTableInventory(db)
	securityVars(db)
	return nil
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

	if err == nil {
		fmt.Printf("Server at %s allows ANONYMOUS login.\n", connTarget())
		return nil
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1045 {
		fmt.Println("Anonymous login disabled")
		return nil
	}

	// Anything else (network error, timeout, unexpected driver error) is
	// NOT evidence either way - report it plainly instead of guessing,
	// since misclassifying it as "allows anonymous login" would be a
	// false positive and misclassifying it as "disabled" would hide a
	// real connectivity problem.
	fmt.Printf("Could not determine anonymous login status: %v\n", err)
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
		fmt.Printf("Error retrieving security variables: %v\n", err)
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
// clientConnArgs returns the -h/-P or --socket flags to pass to the real
// mysql/mysqldump binaries, matching whichever connection mode
// connectToDatabase would use for the Go driver.
func clientConnArgs() []string {
	if socket != "" {
		return []string{"--socket", socket}
	}
	return []string{"-h", host, "-P", strconv.Itoa(port)}
}

func runBackup() error {
	if len(file) == 0 {
		fmt.Println("This command requires -f to be specified")
		return fmt.Errorf("no output file specified (-f)")
	} else if !utils.CheckCliCmdExist("mysqldump") {
		fmt.Println("This command requires mysqldump to be in path")
		return fmt.Errorf("mysqldump not found in PATH")
	}
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("failed to read password")
		return fmt.Errorf("failed to read password: %w", err)
	}

	ofile, err := os.Create(file)
	if err != nil {
		fmt.Printf("%s\n", err)
		return fmt.Errorf("could not create output file %s: %w", file, err)
	}
	defer ofile.Close()

	args := []string{"-u", username, "-p" + password}
	args = append(args, clientConnArgs()...)
	args = append(args, "--all-databases", "--events", "--routines", "--single-transaction")
	cmd := exec.Command("mysqldump", args...)

	cmd.Stdout = ofile
	cmd.Stderr = os.Stderr

	fmt.Printf("Starting Full Mysql backup from %s...\n", connTarget())

	err = cmd.Run()
	if err != nil {
		fmt.Printf("Backup Failed: %s\n", err)
		return fmt.Errorf("mysqldump failed: %w", err)
	}

	fmt.Println("Backup completed successfully")
	return nil
}

// ===========================================================
//
//											RESTORE COMMAND
//
// ===========================================================

func runRestore() error {
	if len(file) == 0 {
		fmt.Println("This command requires -f to be specified")
		return fmt.Errorf("no input file specified (-f)")
	} else if !utils.CheckCliCmdExist("mysql") {
		fmt.Println("This command requires mysql to be in path")
		return fmt.Errorf("mysql client not found in PATH")
	}
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("failed to read password")
		return fmt.Errorf("failed to read password: %w", err)
	}
	ifile, err := os.Open(file)
	if err != nil {
		fmt.Println("Could not open specified file")
		return fmt.Errorf("could not open %s: %w", file, err)
	}
	defer ifile.Close()

	args := []string{"-u", username, "-p" + password}
	args = append(args, clientConnArgs()...)
	cmd := exec.Command("mysql", args...)

	cmd.Stdin = ifile
	cmd.Stderr = os.Stderr

	fmt.Printf("Restoring backup from %s via %s...\n", file, connTarget())
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Restore Failed: %s\n", err)
		return fmt.Errorf("restore failed: %w", err)
	}

	fmt.Println("Restoration completed successfully")
	return nil
}

// describePingError turns a failed db.Ping() into an actionable message
// instead of a bare "authentication failed", since the actual driver
// error usually tells you exactly what's wrong (bad password vs. a grant
// that doesn't match this connection's host, vs. the server being
// unreachable entirely).
func describePingError(err error, user string) string {
	target := connTarget()
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1045:
			if socket != "" {
				return fmt.Sprintf(
					"Access denied for %s via %s (%v)\n"+
						"  Already connecting over the Unix socket, so this is most likely just a wrong\n"+
						"  username/password rather than a host-grant mismatch. Double check the credentials,\n"+
						"  or run `SELECT user, host FROM mysql.user WHERE user='%s';` on the server to see\n"+
						"  which host patterns this account is actually allowed to authenticate from.",
					user, target, mysqlErr.Message, user)
			}
			return fmt.Sprintf(
				"Access denied for %s@%s (%v)\n"+
					"  If this same user/password works with the `mysql` CLI locally, check whether that\n"+
					"  login used a Unix socket (matches a '%s'@'localhost' grant) while ccdc-cli is connecting\n"+
					"  over TCP here (needs a grant matching '%s'@'%s' or '%s'@'%%'). Run\n"+
					"  `SELECT user, host FROM mysql.user WHERE user='%s';` on the server to check, or pass\n"+
					"  --socket /path/to/mysqld.sock to connect the same way the local CLI does.",
				user, host, mysqlErr.Message, user, user, host, user, user)
		case 1044:
			return fmt.Sprintf("Access denied for %s via %s to the requested database (%v)", user, target, mysqlErr.Message)
		default:
			return fmt.Sprintf("MySQL error %d: %v", mysqlErr.Number, mysqlErr.Message)
		}
	}
	return fmt.Sprintf("could not reach %s: %v (wrong host/port/socket path, firewall, or server not listening)", target, err)
}

func runDefault() error {
	p, err := utils.GetPassword()
	if err != nil {
		return fmt.Errorf("failed to read password")
	}

	db, err := connectToDatabase(username, p, host, port, dbName, true)
	if err != nil {
		return fmt.Errorf("failed to open database handle: %w", err)
	}
	defer db.Close()
	err = db.Ping()
	if err != nil {
		return fmt.Errorf("MySQL connection failed: %v", err)
	}

	fmt.Println("MySQL connection successful!")

	return nil
}

// connTarget returns a human-readable description of the current
// connection target (TCP host:port, or a Unix socket path) for use in
// log/error messages.
func connTarget() string {
	if socket != "" {
		return fmt.Sprintf("unix:%s", socket)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func connectToDatabase(user string, password string, host string, port int, dbName string, shouldPrintConnecting bool) (*sql.DB, error) {
	// Build the DSN through the driver's own Config/FormatDSN instead of
	// hand-formatting "user:pass@tcp(host:port)/db" - a password containing
	// '@', ':', '/', or other DSN-meaningful characters would silently
	// corrupt a hand-built string (this is very plausible for generated
	// CCDC creds) even though the same password works fine with the real
	// `mysql` CLI, which never has to serialize it into a connection string.
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.DBName = dbName
	cfg.ParseTime = true
	cfg.Timeout = 5 * time.Second

	// socket is package-level (not a parameter of this function), so it
	// isn't shadowed by the host/port parameters above - it reflects
	// whatever -S/--socket was set to for this invocation.
	if socket != "" {
		cfg.Net = "unix"
		cfg.Addr = socket
	} else {
		cfg.Net = "tcp"
		cfg.Addr = fmt.Sprintf("%s:%d", host, port)
	}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open database handle: %w", err)
	}

	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(6)
	if shouldPrintConnecting {
		if socket != "" {
			fmt.Printf("Connecting to MySQL via socket %s...\n", socket)
		} else {
			fmt.Printf("Connecting to MySQL at %s:%d...\n", host, port)
		}
	}
	return db, nil
}

// ===========================================================
//
//	PROGRAMMATIC ENTRY POINTS (used by the TUI)
//
// ===========================================================

// RunInventoryCapture runs the full MySQL inventory against the given
// target and returns everything it would normally print to stdout as a
// single string. It does not touch package-level CLI flag state beyond
// what's needed to drive the existing inventory functions, and it resets
// the cached password afterward so a stale credential can't leak into a
// later call against a different target.
func RunInventoryCapture(targetHost string, targetPort int, targetUser string, targetSocket string, password string) (string, error) {
	host = targetHost
	port = targetPort
	username = targetUser
	socket = targetSocket
	utils.SetPassword(password)
	defer utils.ResetPassword()

	var runErr error
	out, err := utils.CaptureStdout(func() {
		runErr = runInventory()
	})
	if err != nil {
		return "", err
	}
	return utils.WithResultBanner(out, runErr), nil
}

// RunBackupCapture runs a full mysqldump-based backup to filePath and
// returns everything it would normally print to stdout.
func RunBackupCapture(targetHost string, targetPort int, targetUser string, targetSocket string, password string, filePath string) (string, error) {
	host = targetHost
	port = targetPort
	username = targetUser
	socket = targetSocket
	file = filePath
	utils.SetPassword(password)
	defer utils.ResetPassword()

	var runErr error
	out, err := utils.CaptureStdout(func() {
		runErr = runBackup()
	})
	if err != nil {
		return "", err
	}
	return utils.WithResultBanner(out, runErr), nil
}

// RunRestoreCapture restores from filePath and returns everything it
// would normally print to stdout. This is destructive - callers (like the
// TUI) should confirm with the user before invoking it.
func RunRestoreCapture(targetHost string, targetPort int, targetUser string, targetSocket string, password string, filePath string) (string, error) {
	host = targetHost
	port = targetPort
	username = targetUser
	socket = targetSocket
	file = filePath
	utils.SetPassword(password)
	defer utils.ResetPassword()

	var runErr error
	out, err := utils.CaptureStdout(func() {
		runErr = runRestore()
	})
	if err != nil {
		return "", err
	}
	return utils.WithResultBanner(out, runErr), nil
}

// TestConnectionCapture attempts a lightweight ping against the target and
// returns a human-readable result string plus an error if the connection
// itself failed (as opposed to just returning "connection failed" text).
func TestConnectionCapture(targetHost string, targetPort int, targetUser string, targetSocket string, password string) (string, error) {
	host = targetHost
	port = targetPort
	username = targetUser
	socket = targetSocket
	utils.SetPassword(password)
	defer utils.ResetPassword()

	var runErr error
	out, err := utils.CaptureStdout(func() {
		runErr = runDefault()
	})
	if err != nil {
		return "", err
	}
	if runErr != nil {
		return out + runErr.Error() + "\n", nil
	}
	return out, nil
}
