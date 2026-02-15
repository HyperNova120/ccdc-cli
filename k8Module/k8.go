package k8Module

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"ccdc-cli/utils"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func Getk8Cmd() *cobra.Command {
	k8Cmd := &cobra.Command{
		Use:          "k8",
		Short:        "Module to Inventory Kubernetes",
		RunE:         runCmd,
		SilenceUsage: true,
	}

	return k8Cmd
}

func runCmd(cmd *cobra.Command, args []string) error {
	config, err := getKubeConfig()
	if err != nil {
		fmt.Printf("Error getting Kubernetes config: %v\n", err)
		return err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating client set: %v\n", err)
		return err
	}

	clusterTopologyAndNodes(clientset, config)

	return nil
}

func clusterTopologyAndNodes(clientset *kubernetes.Clientset, config *rest.Config) error {
	utils.PrintHeader("CLUSTER TOPOLOGY & NODES")

	fmt.Printf("[+] API Server: %s\n", config.Host)
	fmt.Printf("\n>>> Node Hardware & Runtime\n")
	nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %v", err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintf(w, "NAME\tSTATUS\tINTERNAL-IP\tOS-IMAGE\tKERNEL\n")

	for _, n := range nodes.Items {
		internalIP := "<none>"
		for _, addr := range n.Status.Addresses {
			if addr.Type == "InternalIP" {
				internalIP = addr.Address
				break
			}
		}

		// Replicates the 'Ready' status check
		status := "Unknown"
		for _, cond := range n.Status.Conditions {
			if cond.Type == "Ready" {
				if cond.Status == "True" {
					status = "Ready"
				} else {
					status = "NotReady"
				}
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			n.Name,
			status,
			internalIP,
			n.Status.NodeInfo.OSImage,
			n.Status.NodeInfo.KernelVersion,
		)
	}
	return w.Flush()
}

func getKubeConfig() (*rest.Config, error) {
	var kubeconfig string

	if env := os.Getenv("KUBECONFIG"); env != "" {
		kubeconfig = env
	} else {
		home := homedir.HomeDir()
		defaultPath := filepath.Join(home, ".kube", "config")

		if _, err := os.Stat(defaultPath); err == nil {
			kubeconfig = defaultPath
		} else {
			k3sDefault := "/etc/rancher/k3s/k3s.yaml"
			if _, err := os.Stat(k3sDefault); err == nil {
				kubeconfig = k3sDefault
			}
		}
	}

	if kubeconfig == "" {
		if config, err := rest.InClusterConfig(); err == nil {
			return config, nil
		}
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}
