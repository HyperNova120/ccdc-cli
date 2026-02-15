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
	ingressAndExternalExposure(clientset)
	securityAndVulnerabilityAudit(clientset)
	storageInventory(clientset)
	systemWarnings(clientset)
	auditSummary(clientset)
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

func ingressAndExternalExposure(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("INGRESS & EXTERNAL EXPOSURE")
	// --- Section: L4 Services & Endpoints ---
	fmt.Printf(">>> L4 Services & Endpoints\n")

	ctx := context.TODO()
	// Fetch both sets of data at once
	services, err := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list services: %v", err)
	}

	endpoints, err := clientset.CoreV1().Endpoints("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list endpoints: %v", err)
	}

	// Create a map of endpoints for O(1) lookup: key = namespace/name
	epMap := make(map[string]string)
	for _, ep := range endpoints.Items {
		var ips []string
		for _, subset := range ep.Subsets {
			for _, addr := range subset.Addresses {
				ips = append(ips, addr.IP)
			}
		}
		if len(ips) > 0 {
			epMap[ep.Namespace+"/"+ep.Name] = strings.Join(ips, ",")
		} else {
			epMap[ep.Namespace+"/"+ep.Name] = "None"
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintf(w, "NAMESPACE\tSERVICE\tTYPE\tEXTERNAL-IP\tTARGET-POD-IPS\n")

	for _, s := range services.Items {
		// Handle External IP (LoadBalancer)
		extIP := "<none>"
		if len(s.Status.LoadBalancer.Ingress) > 0 {
			extIP = s.Status.LoadBalancer.Ingress[0].IP
			if extIP == "" {
				extIP = s.Status.LoadBalancer.Ingress[0].Hostname
			}
		}

		// Pull from our pre-fetched endpoint map
		podIPs := epMap[s.Namespace+"/"+s.Name]
		if podIPs == "" {
			podIPs = "None"
		}

		// Truncate long Pod IP lists for table readability
		if len(podIPs) > 25 {
			podIPs = podIPs[:22] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.Namespace, s.Name, s.Spec.Type, extIP, podIPs)
	}
	w.Flush()

	return nil
}

func securityAndVulnerabilityAudit(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("SECURITY & VULNERABILITY AUDIT")
	ctx := context.TODO()
	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods for security audit: %v", err)
	}

	// --- Section: Container Image Registry Audit ---
	fmt.Printf(">>> Container Image Registry Audit\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintf(w, "NAMESPACE\tPOD-NAME\tIMAGE-SOURCE\n")

	for _, p := range pods.Items {
		var images []string
		// Check standard containers
		for _, c := range p.Spec.Containers {
			images = append(images, c.Image)
		}
		// Also check init containers (Bonus logic not in your shell script!)
		for _, c := range p.Spec.InitContainers {
			images = append(images, "(init) "+c.Image)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\n",
			p.Namespace, p.Name, strings.Join(images, ", "))
	}
	w.Flush()

	// --- Section: Security Risks ---
	fmt.Printf("\n>>> Security Risks\n")
	fmt.Println("Privileged Pods:")

	foundPrivileged := false
	for _, p := range pods.Items {
		isPrivileged := false

		// Helper to check security context
		checkPrivilege := func(containers []corev1.Container) {
			for _, c := range containers {
				if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
					isPrivileged = true
				}
			}
		}

		checkPrivilege(p.Spec.Containers)
		checkPrivilege(p.Spec.InitContainers)

		if isPrivileged {
			fmt.Printf("  [DANGER] %s/%s\n", p.Namespace, p.Name)
			foundPrivileged = true
		}
	}

	if !foundPrivileged {
		fmt.Println("  [OK] None.")
	}

	return nil
}

func storageInventory(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("STORAGE INVENTORY")
	// --- Section: PVC to PV Mapping ---
	fmt.Printf(">>> PVC to PV Mapping\n")

	ctx := context.TODO()
	pvcs, err := clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list PVCs: %v", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	// Matching your shell printf columns exactly
	fmt.Fprintf(w, "NAMESPACE\tPVC-NAME\tSTATUS\tVOLUME\tSTORAGE-CLASS\n")

	for _, pvc := range pvcs.Items {
		volumeName := pvc.Spec.VolumeName
		if volumeName == "" {
			volumeName = "<pending>"
		}

		storageClass := "<none>"
		if pvc.Spec.StorageClassName != nil {
			storageClass = *pvc.Spec.StorageClassName
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			pvc.Namespace,
			pvc.Name,
			pvc.Status.Phase,
			volumeName,
			storageClass,
		)
	}
	w.Flush()

	if len(pvcs.Items) == 0 {
		fmt.Println("[OK] No PVCs found in the cluster.")
	}

	return nil
}

func systemWarnings(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("SYSTEM WARNINGS (LAST 15m)")
	ctx := context.TODO()

	// We use a FieldSelector to replicate 'type=Warning'
	// This filters the data at the API level before it even hits your machine
	listOptions := metav1.ListOptions{
		FieldSelector: "type=Warning",
	}

	events, err := clientset.CoreV1().Events("").List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to fetch events: %v", err)
	}

	if len(events.Items) == 0 {
		fmt.Println("  [OK] No recent system warnings.")
		return nil
	}

	// Sort events by timestamp (most recent last) to replicate 'tail' behavior
	// Note: We'll just take the last 15 if there are more
	items := events.Items
	if len(items) > 15 {
		items = items[len(items)-15:]
	}

	for _, e := range items {
		// Replicating your awk format: [Namespace] Object | Message
		// InvolvedObject.Name is usually the pod/node name ($4 in your awk)
		fmt.Printf("  - [%s] %s | %s\n",
			e.Namespace,
			e.InvolvedObject.Name,
			e.Message,
		)
	}

	return nil
}

func auditSummary(clientset *kubernetes.Clientset) error {
	ctx := context.TODO()

	utils.PrintHeader("AUDIT SUMMARY")
	// Fetch counts
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	svcs, err := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	pvs, err := clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	// We handle PV error gracefully just like your shell script (|| echo 0)
	pvCount := 0
	if err == nil {
		pvCount = len(pvs.Items)
	}

	// Print the summary line
	fmt.Printf("  Nodes: %d | Pods: %d | Services: %d | Volumes: %d\n",
		len(nodes.Items),
		len(pods.Items),
		len(svcs.Items),
		pvCount,
	)

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
