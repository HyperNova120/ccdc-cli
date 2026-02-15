package k8Module

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"ccdc-cli/utils"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
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
	podAndNetworkInventory(clientset)
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
	w.Flush()

	// 4. Node Pressure & Resource Warnings (Integrated Logic)
	fmt.Printf("\n>>> Node Pressure & Resource Warnings\n")
	foundPressure := false
	for _, n := range nodes.Items {
		var activePressures []string
		for _, cond := range n.Status.Conditions {
			// Logic: If status is "True" and it's NOT the "Ready" condition, it's a pressure flag
			if cond.Status == "True" && cond.Type != "Ready" {
				activePressures = append(activePressures, string(cond.Type))
			}
		}

		if len(activePressures) > 0 {
			if !foundPressure {
				fmt.Println("[!] CRITICAL: Node Resource Pressure Detected:")
				foundPressure = true
			}
			fmt.Printf("  - Node: %-22s Condition: %s\n", n.Name, strings.Join(activePressures, ", "))
		}
	}

	if !foundPressure {
		fmt.Println("[OK] No Node pressure flags detected.")
	}

	return nil
}

func podAndNetworkInventory(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("POD & NETWORK INVENTORY")
	// Fetch all pods once to avoid expensive API calls in a loop
	pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods: %v", err)
	}

	// --- Section: Pod-to-Node Mapping ---
	fmt.Printf(">>> Pod-to-Node Mapping\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintf(w, "NAMESPACE\tPOD-NAME\tPOD-IP\tNODE-IP\tSTATUS\n")

	for _, p := range pods.Items {
		podIP := p.Status.PodIP
		if podIP == "" {
			podIP = "<none>"
		}
		hostIP := p.Status.HostIP
		if hostIP == "" {
			hostIP = "<none>"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			p.Namespace, p.Name, podIP, hostIP, p.Status.Phase)
	}
	w.Flush()

	// --- Section: Pod Health & Error Analytics ---
	fmt.Printf("\n>>> Pod Health & Error Analytics\n")
	foundErrors := false

	for _, p := range pods.Items {
		// Replicate grep -vE "Running|Completed"
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded {
			continue
		}

		if !foundErrors {
			fmt.Printf("  %-45s %-20s %-50s\n", "POD (NS/NAME)", "REASON", "MESSAGE")
			foundErrors = true
		}

		var reason, message string
		podFullName := fmt.Sprintf("%s/%s", p.Namespace, p.Name)

		// 1. Check Container States (Waiting/Terminated)
		containerFound := false
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				reason = cs.State.Waiting.Reason
				message = cs.State.Waiting.Message
				containerFound = true
				break
			} else if cs.State.Terminated != nil {
				reason = cs.State.Terminated.Reason
				message = cs.State.Terminated.Message
				containerFound = true
				break
			}
		}

		// 2. Fallback to Scheduling Issues (matches your shell logic)
		if !containerFound {
			reason = "Scheduling"
			for _, cond := range p.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
					message = cond.Message
					break
				}
			}
		}

		// Clean up empty strings
		if reason == "" {
			reason = "Unknown"
		}
		if message == "" {
			message = "No diagnostic message"
		}

		// Truncate message for display like your shell script's %.70s
		if len(message) > 70 {
			message = message[:67] + "..."
		}

		fmt.Printf("  %-45s %-20s %-50s\n", podFullName, reason, message)
	}

	if !foundErrors {
		fmt.Println("[OK] All pods are healthy (Running/Completed).")
	}

	return nil
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
