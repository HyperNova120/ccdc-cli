package psqlModule

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ccdc-cli/utils"

	_ "github.com/jackc/pgx/v5"
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
	db, err := connectToDatabase(username, host, port)
	if err != nil {
		fmt.Printf("%v", err)
		return
	}
	defer db.Close()

	userAccounts(db)
	dataAccessPermissions(db)
	instanceInventory(db)
}

func userAccounts(db *sql.DB) {
	utils.PrintHeader("USER ACCOUNTS")
	query := `
	SELECT rolename,
	CASE WHEN rolsuper THEN 'YES' ELSE 'NO' END,
	CASE WHEN rolpassword IS NULL THEN 'YES' ELSE 'NO' END,
	CASE WHEN rolcanlogin THEN 'YES' ELSE 'NO' END
	FROM pg_roles ORDER BY rolcanlogin DESC;`

	rows, err := db.Query(query)
	if err != nil {
		fmt.Printf("Error querying database: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var rname, rsup, rnop, rlog string
		if err := rows.Scan(&rname, rsup, rnop, rlog); err != nil {
			continue
		}
		fmt.Printf(" |-- %-25s | Super: %-3s | NoPass: %-3s | Login: %s\n", rname, rsup, rnop, rlog)
	}
}

func dataAccessPermissions(db *sql.DB) {
	utils.PrintHeader("DATA ACCESS PERMISSIONS")
	query := `
	SELECT datname 
	FROM pg_database
	WHERE datistemplate = false;`

	drows, err := db.Query(query)
	if err != nil {
		fmt.Printf("Error querying database: %v\n", err)
		return
	}
	defer drows.Close()

	for drows.Next() {
		var dname string
		if err := drows.Scan(&dname); err != nil {
			continue
		}
		db2, err := connectToDatabaseDB(username, host, port, dname)
		if err != nil {
			fmt.Printf("  |-- Unable to connect to %s\n", dname)
			continue
		}
		defer db2.Close()
		query = `
		SELECT current_database(), r.rolname,
		CASE WHEN has_database_privilege(r.rolname, current_database(), 'CONNECT') THEN 'YES' ELSE 'NO' END,
		CASE WHEN EXISTS (SELECT 1 FROM information_schema.table_privileges
			WHERE grantee = r.rolname AND privilege_type = 'SELECT'
			OR r.rolsuper THEN 'YES' ELSE 'NO' END,
		CASE WHEN EXISTS (SELECT 1 FROM information_schema.table_privileges
			WHERE grantee = r.rolname AND privilege_type IN ('INSERT','UPDATE','DELETE')
			OR r.rolsuper THEN 'YES' ELSE 'NO' END
		FROM pg_roles r WHERE r.rolcanlogin = true;`

		arows, err := db.Query(query)
		if err != nil {
			continue
		}
		defer arows.Close()
		for arows.Next() {
			var dbName, uname, uconn, uread, uwrite string
			if err := arows.Scan(&dbName, &uname, &uconn, &uread, &uwrite); err != nil {
				continue
			}
			if uconn == "YES" || uread == "YES" {
				fmt.Printf("  |-- DB: %-15s | User: %-15s | Conn: %-3s | Read: %-3s | Write: %s\n", dbName, uname, uconn, uread, uwrite)
			}
		}
		arows.Close()
		db2.Close()
	}
}

func instanceInventory(db *sql.DB) {
	utils.PrintHeader("INSTANCE CONTENT INVENTORY")
	query := `
	SELECT datname 
	FROM pg_database
	WHERE datistemplate = false;`

	drows, err := db.Query(query)
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

		query = fmt.Sprintf("SELECT pg_size_pretty(pg_database_size('%s'));", dbName)

		var dsize string
		err = db.QueryRow(query).Scan(dsize)
		if err != nil {
			fmt.Printf("  |-- Error querying %s: %v\n", dbName, err)
			continue
		}
		fmt.Printf("  |-- DATABASE: %s (SIZE: %s)", dbName, dsize)

		db2, err := connectToDatabaseDB(username, host, port, dbName)
		if err != nil {
			continue
		}
		defer db2.Close()

		query = `
		SELECT c.relname, n.nspname, pg_size_pretty(pg_total_relation_size(c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname = 'public' LIMIT 5;`

		trows, err := db2.Query(query)
		if err != nil {
			continue
		}
		defer trows.Close()

		for trows.Next() {
			var tname, tns, tsize string
			if err := trows.Scan(&tname, &tns, &tsize); err != nil {
				continue
			}
			fmt.Printf("    |-- %-30s | Size: %s\n", tname, tsize)
		}
		trows.Close()
		db2.Close()
	}
}

func connectToDatabase(username, host string, port int) (*sql.DB, error) {
	return connectToDatabaseDB(username, host, port, "postgres")
}

func connectToDatabaseDB(username, host string, port int, dbname string) (*sql.DB, error) {
	fmt.Printf("Connecting to database: '%s' at %s:%d", dbname, host, port)
	dns := fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", username, host, port, dbname)
	db, err := sql.Open("pgx", dns)
	if err != nil {
		return nil, fmt.Errorf("Could not open connection: %w", err)
	}

	if err := db.Ping(); err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) || strings.Contains(err.Error(), "connection refused") {
			return nil, fmt.Errorf("Database not listening on %s:%d", host, port)
		}
		return nil, fmt.Errorf("Could not reach instance: %w", err)
	}
	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(6)

	return db, nil
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
