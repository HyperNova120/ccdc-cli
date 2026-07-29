package cmd

import (
	"ccdc-cli/tui"

	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the interactive dashboard",
	Long: `Launch an interactive dashboard: pick a saved target (see
'ccdc-cli targets'), run its inventory, and browse the scrollable output.
All flag-based commands (mysql/psql/k8) keep working exactly as before for
scripting - this is a second interface on top of the same logic.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.Run()
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
