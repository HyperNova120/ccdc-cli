package psqlModule

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ccdc-cli/utils"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

var (
	port      int
	host      string
	socket    string
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
		SilenceErrors: true,
	}
	psqlCmd.Flags().IntVarP(&port, "port", "p", 5432, "Port to Connect to")
	psqlCmd.Flags().StringVarP(&host, "host", "H", "127.0.0.1", "Host to Connect to (TCP)")
	psqlCmd.Flags().StringVarP(&socket, "socket", "S", "", "Path to the directory containing a Unix socket to connect through instead of TCP (e.g. /var/run/postgresql)")
	psqlCmd.Flags().StringVarP(&username, "username", "u", "postgres", "User to Connect as")
	psqlCmd.Flags().BoolVarP(&inventory, "inventory", "i", false, "Should run Inventory Check")
	psqlCmd.Flags().BoolVarP(&backup, "backup", "b", false, "Should Backup")
	psqlCmd.Flags().BoolVarP(&restore, "restore", "r", false, "Should Restore")
	psqlCmd.Flags().StringVarP(&file, "file", "f", "", "File to Use for Backup/Restore")

	psqlCmd.MarkFlagsMutuallyExclusive("backup", "restore")
	psqlCmd.MarkFlagsMutuallyExclusive("host", "socket")
	return psqlCmd
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
		fmt.Println("Error Reading Password")
		return fmt.Errorf("failed to read password: %w", err)
	}

	db, err := connectToDatabase(username, password, host, port)
	if err != nil {
		fmt.Printf("%v\n", err)
		return err
	}
	defer db.Close()

	userAccounts(db)
	dataAccessPermissions(db)
	instanceInventory(db)
	return nil
}

func userAccounts(db *pgxpool.Pool) {
	utils.PrintHeader("USER ACCOUNTS")
	query := `
	SELECT rolname,
	CASE WHEN rolsuper THEN 'YES' ELSE 'NO' END,
	CASE WHEN rolpassword IS NULL THEN 'YES' ELSE 'NO' END,
	CASE WHEN rolcanlogin THEN 'YES' ELSE 'NO' END
	FROM pg_roles ORDER BY rolcanlogin DESC;`

	rows, err := db.Query(context.Background(), query)
	if err != nil {
		fmt.Printf("Error querying database: %v\n", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var rname, rsup, rnop, rlog string
		if err := rows.Scan(&rname, &rsup, &rnop, &rlog); err != nil {
			fmt.Printf("Error reading user: %v\n", err)
			continue
		}
		fmt.Printf("  |-- %-30s | Super: %-3s | NoPass: %-3s | Login: %s\n", rname, rsup, rnop, rlog)
	}
}

func dataAccessPermissions(db *pgxpool.Pool) {
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("Error Reading Password")
		return
	}

	utils.PrintHeader("DATA ACCESS PERMISSIONS")
	query := `
	SELECT datname 
	FROM pg_database
	WHERE datistemplate = false;`

	drows, err := db.Query(context.Background(), query)
	if err != nil {
		fmt.Printf("Error querying database: %v\n", err)
		return
	}
	defer drows.Close()

	for drows.Next() {
		var dname string
		if err := drows.Scan(&dname); err != nil {
			fmt.Printf("Error reading databases: %v\n", err)
			continue
		}
		db2, err := connectToDatabaseDB(username, password, host, port, dname, false)
		if err != nil {
			fmt.Printf("  |-- Unable to connect to %s\n", dname)
			continue
		}
		defer db2.Close()
		query = `
		SELECT current_database(), r.rolname,
		CASE WHEN has_database_privilege(r.rolname, current_database(), 'CONNECT') THEN 'YES' ELSE 'NO' END,
		CASE WHEN EXISTS (SELECT 1 FROM information_schema.table_privileges
			WHERE grantee = r.rolname AND privilege_type = 'SELECT')
			OR r.rolsuper THEN 'YES' ELSE 'NO' END,
		CASE WHEN EXISTS (SELECT 1 FROM information_schema.table_privileges
			WHERE grantee = r.rolname AND privilege_type IN ('INSERT','UPDATE','DELETE'))
			OR r.rolsuper THEN 'YES' ELSE 'NO' END
		FROM pg_roles r WHERE r.rolcanlogin = true;`

		arows, err := db2.Query(context.Background(), query)
		if err != nil {
			fmt.Printf("Error reading tables: %v\n", err)
			continue
		}
		defer arows.Close()
		fmt.Printf("  |-- Database: %s\n", dname)
		for arows.Next() {
			var dbName, uname, uconn, uread, uwrite string
			if err := arows.Scan(&dbName, &uname, &uconn, &uread, &uwrite); err != nil {
				fmt.Printf("Error scanning tables: %v\n", err)
				continue
			}
			if uconn == "YES" || uread == "YES" {
				fmt.Printf("        |-- User: %-15s | Conn: %-3s | Read: %-3s | Write: %s\n", uname, uconn, uread, uwrite)
			}
		}
		arows.Close()
		db2.Close()
	}
}

func instanceInventory(db *pgxpool.Pool) {
	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("Error Reading Password")
		return
	}

	utils.PrintHeader("INSTANCE CONTENT INVENTORY")
	query := `
	SELECT datname 
	FROM pg_database
	WHERE datistemplate = false;`

	drows, err := db.Query(context.Background(), query)
	if err != nil {
		fmt.Printf("Error querying database: %v\n", err)
		return
	}
	defer drows.Close()

	for drows.Next() {
		var dbName string
		if err := drows.Scan(&dbName); err != nil {
			fmt.Printf("  |-- Error With Query: %s", err)
			continue
		}

		query = fmt.Sprintf("SELECT pg_size_pretty(pg_database_size('%s'))::text;", dbName)

		var dsize string
		err = db.QueryRow(context.Background(), query).Scan(&dsize)
		if err != nil {
			fmt.Printf("  |-- Error querying %s: %v\n", dbName, err)
			continue
		}
		fmt.Printf("  |-- DATABASE: %s (SIZE: %s)\n", dbName, dsize)

		db2, err := connectToDatabaseDB(username, password, host, port, dbName, false)
		if err != nil {
			fmt.Printf("Error connecting for tables: %v\n", err)
			continue
		}
		defer db2.Close()

		query = `
		SELECT c.relname, n.nspname, pg_size_pretty(pg_total_relation_size(c.oid))::text
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname = 'public' LIMIT 5;`

		trows, err := db2.Query(context.Background(), query)
		if err != nil {
			fmt.Printf("Error querying for tables: %v\n", err)
			continue
		}
		defer trows.Close()

		for trows.Next() {
			var tname, tns, tsize string
			if err := trows.Scan(&tname, &tns, &tsize); err != nil {
				fmt.Printf("Error scanning for tables: %v\n", err)
				continue
			}
			fmt.Printf("        |-- %-35s | Size: %s\n", tname, tsize)
		}
		trows.Close()
		db2.Close()
	}
}

func connectToDatabase(username, password, host string, port int) (*pgxpool.Pool, error) {
	return connectToDatabaseDB(username, password, host, port, "postgres", true)
}

// connTarget returns a human-readable description of the current
// connection target (TCP host:port, or a Unix socket directory) for use
// in log/error messages.
func connTarget() string {
	if socket != "" {
		return fmt.Sprintf("unix:%s", socket)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// escapeConnInfo quotes a value for inclusion in a libpq keyword/value
// connection string (e.g. "host=/var/run/postgresql port=5432 ..."),
// which is needed for the Unix-socket path since it contains '/' and
// potentially other characters conninfo parsing would otherwise choke on.
func escapeConnInfo(v string) string {
	if v == "" {
		return "''"
	}
	if !strings.ContainsAny(v, " '\\\t\n") {
		return v
	}
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

func connectToDatabaseDB(username, password, host string, port int, dbname string, shouldPrint bool) (*pgxpool.Pool, error) {
	if shouldPrint {
		fmt.Printf("Connecting to database: '%s' via %s", dbname, connTarget())
	}

	var dns string
	if socket != "" {
		// libpq keyword/value form: a host starting with '/' tells it to
		// use a Unix socket in that directory (looking for
		// <dir>/.s.PGSQL.<port>), which is exactly what psql/pg_dumpall
		// do too when given the same -h <dir>.
		dns = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
			escapeConnInfo(socket), port, escapeConnInfo(username), escapeConnInfo(password), escapeConnInfo(dbname))
	} else {
		userInfo := url.UserPassword(username, password)
		dns = fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", userInfo, host, port, dbname)
	}

	config, err := pgxpool.ParseConfig(dns)
	if err != nil {
		return nil, fmt.Errorf("Could not open connection: %w", err)
	}

	config.MaxConns = 6
	config.MaxConnLifetime = 8 * time.Minute

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to create connetion pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable or auth failed: %w", err)
	}

	return pool, nil
}

// clientConnHost returns what to pass as -h to the real psql/pg_dumpall
// binaries: the socket directory in socket mode (same convention psql
// itself uses - a -h value starting with '/' means "Unix socket here"),
// or the TCP host otherwise.
func clientConnHost() string {
	if socket != "" {
		return socket
	}
	return host
}

func runRestore() error {
	if !utils.CheckCliCmdExist("psql") {
		fmt.Println("This command requires 'psql' to be in path")
		return fmt.Errorf("psql client not found in PATH")
	} else if len(file) == 0 {
		fmt.Println("This command requires the -f flag to be set")
		return fmt.Errorf("no input file specified (-f)")
	}

	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("Failed to read password!")
		return fmt.Errorf("failed to read password: %w", err)
	}

	ifile, err := os.Open(file)
	if err != nil {
		fmt.Printf("Failed to open backup file: %v\n", err)
		return fmt.Errorf("could not open %s: %w", file, err)
	}
	defer ifile.Close()

	cmd := exec.Command("psql",
		"-h", clientConnHost(),
		"-p", strconv.Itoa(port),
		"-U", username,
		"-d", "postgres")

	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", password))

	cmd.Stdin = ifile
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	fmt.Printf("Starting full restoration from %s via %s\n", file, connTarget())
	if err := cmd.Run(); err != nil {
		fmt.Printf("Restore failed: %v\n", err)
		return fmt.Errorf("restore failed: %w", err)
	}
	fmt.Println("Restoration completed successfully!")
	return nil
}

func runBackup() error {
	if !utils.CheckCliCmdExist("pg_dumpall") {
		fmt.Println("This command requires 'pg_dumpall' to be in path")
		return fmt.Errorf("pg_dumpall not found in PATH")
	} else if len(file) == 0 {
		fmt.Println("This command requires the -f flag to be set")
		return fmt.Errorf("no output file specified (-f)")
	}

	password, err := utils.GetPassword()
	if err != nil {
		fmt.Println("Failed to read password!")
		return fmt.Errorf("failed to read password: %w", err)
	}

	cmd := exec.Command("pg_dumpall",
		"-h", clientConnHost(),
		"-p", strconv.Itoa(port),
		"-U", username)

	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", password))

	ofile, err := os.Create(file)
	if err != nil {
		fmt.Printf("Failed to create backup file: %v\n", err)
		return fmt.Errorf("could not create %s: %w", file, err)
	}
	defer ofile.Close()

	cmd.Stdout = ofile
	cmd.Stderr = os.Stderr

	fmt.Printf("Backing up instance from %s\n", connTarget())
	if err := cmd.Run(); err != nil {
		fmt.Printf("Backup Failed: %v\n", err)
		os.Remove(file)
		return fmt.Errorf("pg_dumpall failed: %w", err)
	}

	fmt.Printf("Created Backup: %s\n", file)
	return nil
}

// ===========================================================
//
//	PROGRAMMATIC ENTRY POINTS (used by the TUI)
//
// ===========================================================

// RunInventoryCapture runs the full PostgreSQL inventory against the given
// target and returns everything it would normally print to stdout as a
// single string. Resets the cached password afterward so it can't leak
// into a later call against a different target.
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

// RunBackupCapture runs a full pg_dumpall-based backup to filePath and
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
