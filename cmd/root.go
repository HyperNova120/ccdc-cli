package cmd

import (
	"fmt"
	"os"

	"ccdc-cli/mysqlModule"
	"ccdc-cli/psqlModule"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ccdc-cli",
	Short: "A portable cli tool for ccdc",
}

func init() {
	rootCmd.AddCommand(mysqlModule.GetmysqlCmd())
	rootCmd.AddCommand(psqlModule.GetpsqlCmd())
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
