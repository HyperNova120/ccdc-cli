package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"ccdc-cli/config"

	"github.com/spf13/cobra"
)

var (
	targetType           string
	targetHost           string
	targetPort           int
	targetUsername       string
	targetKubeconfigPath string
	targetNotes          string
)

var targetsCmd = &cobra.Command{
	Use:   "targets",
	Short: "Manage saved connection targets (~/.ccdc-cli/targets.yaml)",
	Long: `Save hosts once and reference them by name instead of retyping
-H/-u/-p flags for every box during a competition. Passwords are never
stored - just connection info.`,
}

var targetsAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add or update a saved target",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		switch targetType {
		case "mysql", "psql", "k8":
		default:
			return fmt.Errorf("--type must be one of: mysql, psql, k8")
		}

		t := config.Target{
			Name:           name,
			Type:           config.TargetType(targetType),
			Host:           targetHost,
			Port:           targetPort,
			Username:       targetUsername,
			KubeconfigPath: targetKubeconfigPath,
			Notes:          targetNotes,
		}

		if err := config.AddTarget(t); err != nil {
			return err
		}
		fmt.Printf("Saved target %q (%s)\n", name, targetType)
		return nil
	},
}

var targetsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved targets",
	RunE: func(cmd *cobra.Command, args []string) error {
		targets, err := config.LoadTargets()
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			fmt.Println("No saved targets yet. Add one with: ccdc-cli targets add <name> --type mysql --host <ip> --username <user>")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintf(w, "NAME\tTYPE\tHOST\tPORT\tUSERNAME\tNOTES\n")
		for _, t := range targets {
			host := t.Host
			if t.Type == config.TypeK8s {
				host = t.KubeconfigPath
				if host == "" {
					host = "<default kubeconfig>"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", t.Name, t.Type, host, t.Port, t.Username, t.Notes)
		}
		return w.Flush()
	},
}

var targetsRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a saved target",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.RemoveTarget(args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed target %q\n", args[0])
		return nil
	},
}

func init() {
	targetsAddCmd.Flags().StringVar(&targetType, "type", "", "Target type: mysql, psql, or k8 (required)")
	targetsAddCmd.Flags().StringVar(&targetHost, "host", "127.0.0.1", "Host to connect to (mysql/psql)")
	targetsAddCmd.Flags().IntVar(&targetPort, "port", 0, "Port to connect to (mysql/psql; defaults per-type if 0)")
	targetsAddCmd.Flags().StringVar(&targetUsername, "username", "", "Username to connect as (mysql/psql)")
	targetsAddCmd.Flags().StringVar(&targetKubeconfigPath, "kubeconfig", "", "Path to kubeconfig (k8 only; empty = default discovery)")
	targetsAddCmd.Flags().StringVar(&targetNotes, "notes", "", "Free-text note (e.g. \"team box 3\")")
	targetsAddCmd.MarkFlagRequired("type")

	targetsCmd.AddCommand(targetsAddCmd)
	targetsCmd.AddCommand(targetsListCmd)
	targetsCmd.AddCommand(targetsRemoveCmd)

	rootCmd.AddCommand(targetsCmd)
}
