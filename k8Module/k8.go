package k8Module

import (
	"context"
	b64 "encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"ccdc-cli/utils"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	inventory     bool
	rollCreds     bool
	credSequences string
	revealSecrets bool
)

func Getk8Cmd() *cobra.Command {
	k8Cmd := &cobra.Command{
		Use:   "k8",
		Short: "Module to Inventory Kubernetes",
		Long: `

Secret Rotation Info:

NAME      - secret name
NAMESPACE - namespace to rotate the secret in
KEY       - a key within a kubernetes secret that will be modified
STRATEGY  - new value creation strategy. (supported values are retainPrev, omitPrev), retainPrev will save off
            an old value in the key within the same secret, appending _PREV to the name of the key. If omitPrev
            is specified, the old value is not saved`,
		RunE:         runCmd,
		SilenceUsage: true,
	}

	k8Cmd.Flags().BoolVarP(&inventory, "inventory", "i", false, "Should Run Inventory")
	k8Cmd.Flags().BoolVarP(&rollCreds, "roll", "r", false, "Should Run Roll Credentials")
	k8Cmd.Flags().StringVarP(&credSequences, "sequences", "s", "", "SECRET_NAME,NAMESPACE,KEY,STRATEGY[|SECRET_NAME,NAMESPACE,KEY,STRATEGY]")
	k8Cmd.Flags().BoolVar(&revealSecrets, "reveal", false, "Print secret values in plaintext instead of redacted (use with care - shoulder surfing/logging risk)")
	return k8Cmd
}

func runCmd(cmd *cobra.Command, args []string) error {
	config, err := getKubeConfig()
	if err != nil {
		fmt.Printf("Error getting Kubernetes config: %v\n", err)
		return err
	}

	flagFound := false
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating client set: %v\n", err)
		return err
	}

	if cmd.Flags().Changed("inventory") {
		runInventory(clientset, config)
		flagFound = true
	}

	if cmd.Flags().Changed("roll") {
		if credSequences == "" {
			fmt.Println("-r flag requires -s to be properly set")
			return nil
		}
		rollCredentials(clientset, config)
		flagFound = true
	}

	if !flagFound {
		fmt.Println("This subcommand requires -i or -r to be set")
	}
	return nil
}

// ===============================================
//
//				  CREDENTIAL ROLLING CODE
//	CODE PULLED FROM: https://github.com/alexlokshin/kube-secret-rotator/tree/master
//
// ===============================================
type secretDef struct {
	name      string
	namespace string
	key       string
	strategy  string
}

var chars = []rune("01234567890$%#!abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func rollCredentials(clientset *kubernetes.Clientset, config *rest.Config) {
	secretDefs := []secretDef{}
	fmt.Printf("Kubernetes Secret Rotator\n")

	sequences := strings.Split(credSequences, "|")
	if len(sequences) < 1 {
		fmt.Println("At least one secret sequence has to be specified.")
		return
	}

	for i := 0; i < len(sequences); i++ {
		parts := strings.Split(sequences[i], ",")
		if len(parts) != 4 {
			fmt.Println("Invalid specification for the secret. Valid response is SECRET_NAME,NAMESPACE,KEY,STRATEGY. For Example: tempsecret,defualt,somekey,retainPrev")
			return

		}
		secret := secretDef{name: parts[0], namespace: parts[1], key: parts[2], strategy: parts[3]}
		secretDefs = append(secretDefs, secret)

		fmt.Printf("Rotating secret `%s` in the namespace of `%s`\n", secret.name, secret.namespace)
	}
	rotate(clientset, config, secretDefs)
}

func rotate(clientset *kubernetes.Clientset, config *rest.Config, secretDefs []secretDef) {
	for i := 0; i < len(secretDefs); i++ {
		newValue := ([]byte)(randomizeString(40))
		rotateSecret(clientset, secretDefs[i], newValue)
	}
}

// rotateSecret rotates exactly one secret key to newValue, creating the
// secret if it doesn't exist yet. This is the single source of truth for
// rotation logic - both the CLI's multi-sequence rotate() above and the
// TUI's single-secret RotateSecretValueCapture below call into this so
// they can't drift apart on behavior.
func rotateSecret(clientset *kubernetes.Clientset, def secretDef, newValue []byte) {
	_, err := clientset.CoreV1().Namespaces().Get(context.TODO(), def.namespace, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Cannot get the namespace %s: skipping secret creation for now.\n", def.namespace)
		return
	}

	secret, err := clientset.CoreV1().Secrets(def.namespace).Get(context.TODO(), def.name, metav1.GetOptions{})

	fmt.Printf("Rotating secret %s.%s\n", def.namespace, def.name)

	t := time.Now()

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		dataMap := make(map[string][]byte)
		if def.strategy == "retainPrev" {
			dataMap[def.key+"_PREV"] = newValue
		}
		dataMap[def.key] = newValue

		annotations := make(map[string]string)
		annotations["kube-secret-rotator/rotated"] = t.Format(time.RFC850)

		secret = &corev1.Secret{
			Type: corev1.SecretTypeOpaque,
			ObjectMeta: metav1.ObjectMeta{
				Name:        def.name,
				Namespace:   def.namespace,
				Annotations: annotations,
			},
			Data: dataMap,
		}

		fmt.Printf("Secret %s.%s doesn't exist. Creating.\n", def.namespace, def.name)
		_, err = clientset.CoreV1().Secrets(def.namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
		if err != nil {
			fmt.Printf("Failed to create secret: %s\n", err)
		} else {
			if revealSecrets {
				fmt.Printf("Updated Secret %s.%s->%s with new value: %s\n", def.namespace, def.name, def.key, string(newValue))
			} else {
				fmt.Printf("Updated Secret %s.%s->%s with new value: %s (use --reveal to print plaintext)\n", def.namespace, def.name, def.key, utils.Redact(string(newValue)))
			}
		}
	} else {
		if secret == nil {
			fmt.Printf("No Existing secret found.\n")
			return
		}
		// NOTE: client-go already base64-decodes Secret.Data for us, so
		// secret.Data[key] is the raw value, not base64 text. Decoding
		// it again here previously produced garbage/errors.
		currentValue := secret.Data[def.key]
		if revealSecrets {
			fmt.Printf("Current value of the secret %s.%s->%s is %s\n", def.namespace, def.name, def.key, string(currentValue))
		} else {
			fmt.Printf("Current value of the secret %s.%s->%s is %s (use --reveal to print plaintext)\n", def.namespace, def.name, def.key, utils.Redact(string(currentValue)))
		}
		if def.strategy == "retainPrev" {
			secret.Data[def.key+"_PREV"] = secret.Data[def.key]
		}
		secret.Data[def.key] = newValue
		if secret.Annotations == nil {
			secret.Annotations = make(map[string]string)
		}
		secret.Annotations["kube-secret-rotator/rotated"] = t.Format(time.RFC850)
		_, err = clientset.CoreV1().Secrets(def.namespace).Update(context.TODO(), secret, metav1.UpdateOptions{})
		if err != nil {
			fmt.Printf("Failed to update secret: %v\n", err)
		} else {
			if revealSecrets {
				fmt.Printf("Updated Secret %s.%s->%s with new value: %s\n", def.namespace, def.name, def.key, string(newValue))
			} else {
				fmt.Printf("Updated Secret %s.%s->%s with new value: %s (use --reveal to print plaintext)\n", def.namespace, def.name, def.key, utils.Redact(string(newValue)))
			}
		}
	}
}

func randomizeString(i int) string {
	byteArray := make([]rune, i)
	for i := range byteArray {
		byteArray[i] = chars[rand.IntN(len(chars))]
	}
	return strings.Replace(b64.StdEncoding.EncodeToString([]byte(string(byteArray))), "=", "", -1)
}

//===============================================
//							INVENTORY CODE
//===============================================

func runInventory(clientset *kubernetes.Clientset, config *rest.Config) {
	_ = clusterTopologyAndNodes(clientset, config)
	_ = podAndNetworkInventory(clientset)
	secretInventory(clientset)
	_ = ingressAndExternalExposure(clientset)
	_ = securityAndVulnerabilityAudit(clientset)
	_ = storageInventory(clientset)
	_ = systemWarnings(clientset)
	_ = workloadInventory(clientset)
	_ = ingressInventory(clientset)
	_ = rbacAdminAudit(clientset)
	apiDiscoveryAudit(clientset)
	_ = auditSummary(clientset)
}

func secretInventory(clientset *kubernetes.Clientset) {
	utils.PrintHeader("Cluster Secrets")

	namespaces, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Could not list namespaces: %v\n", err)
		return
	}

	for _, ns := range namespaces.Items {
		if ns.Name == "kube-system" || ns.Name == "kube-public" {
			fmt.Printf("  |-- %-35s [SYSTEM NAMESPACE]\n", ns.Name)
		} else {
			fmt.Printf("  |-- %s\n", ns.Name)
		}

		secrets, err := clientset.CoreV1().Secrets(ns.Name).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Could not get secrets for namspace %s: %v\n", ns.Name, err)
			continue
		}
		for _, secret := range secrets.Items {
			fmt.Printf("    |-- %-35s %s\n", secret.Name, getSecretTag(secret))

			if getSecretTag(secret) == "[OPAQUE: ROTATE]" {
				for key, valueBytes := range secret.Data {
					fmt.Printf("      |-- Key: %s\n", key)
					if revealSecrets {
						fmt.Printf("        |-- Value: \n%s\n        |-- END VALUE\n", string(valueBytes))
					} else {
						fmt.Printf("        |-- Value: %s (use --reveal to print plaintext)\n", utils.Redact(string(valueBytes)))
					}
				}
			}
		}
	}
}

func getSecretTag(secret corev1.Secret) any {
	if secret.Name == "kube-system" || strings.HasPrefix(secret.Name, "sh.helm") {
		return "[SYSTEM: SKIP]"
	}

	if secret.Type == corev1.SecretTypeServiceAccountToken || secret.Type == corev1.SecretTypeTLS {
		return "[INFRA: SKIP]"
	}

	if secret.Type == corev1.SecretTypeOpaque {
		return "[OPAQUE: ROTATE]"
	}

	return "[OTHER]"
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

	ctx := context.TODO()
	services, err := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list services: %v", err)
	}

	// Use EndpointSlices for modern k8s compatibility (standard in k3s/minikube)
	slices, err := clientset.DiscoveryV1().EndpointSlices("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list endpoint slices: %v", err)
	}

	// Map Service Name to IPs
	epMap := make(map[string][]string)
	for _, slice := range slices.Items {
		svcName := slice.Labels["kubernetes.io/service-name"]
		for _, endpoint := range slice.Endpoints {
			epMap[slice.Namespace+"/"+svcName] = append(epMap[slice.Namespace+"/"+svcName], endpoint.Addresses...)
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintf(w, "NAMESPACE\tSERVICE\tTYPE\tEXTERNAL-IP\tTARGET-POD-IPS\n")

	for _, s := range services.Items {
		extIP := "<none>"

		// 1. Check LoadBalancer Status (Works for k3s ServiceLB and Cloud LBs)
		if len(s.Status.LoadBalancer.Ingress) > 0 {
			if s.Status.LoadBalancer.Ingress[0].IP != "" {
				extIP = s.Status.LoadBalancer.Ingress[0].IP
			} else if s.Status.LoadBalancer.Ingress[0].Hostname != "" {
				extIP = s.Status.LoadBalancer.Ingress[0].Hostname
			}
		}

		// 2. Fallback to Spec External IPs (Common in manual Debian/k3s setups)
		if extIP == "<none>" && len(s.Spec.ExternalIPs) > 0 {
			extIP = s.Spec.ExternalIPs[0]
		}

		// 3. Fallback to ClusterIP so the column isn't just empty (Matches bash script intent)
		if extIP == "<none>" && s.Spec.ClusterIP != "" && s.Spec.ClusterIP != "None" {
			extIP = s.Spec.ClusterIP + " (Int)"
		}

		podIPs := strings.Join(epMap[s.Namespace+"/"+s.Name], ",")
		if len(podIPs) > 25 {
			podIPs = podIPs[:22] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Namespace, s.Name, s.Spec.Type, extIP, podIPs)
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

// --- 7. WORKLOAD BLUEPRINTS ---
// Pods tell you what is running NOW. This tells you what is scheduled to stay running.
func workloadInventory(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("WORKLOAD CONTROLLERS (BLUEPRINTS)")
	ctx := context.TODO()

	// Deployments
	deps, err := clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err == nil {
		fmt.Printf(">>> Deployments\n")
		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintf(w, "NAMESPACE\tNAME\tREPLICAS\tUP-TO-DATE\tAVAILABLE\n")
		for _, d := range deps.Items {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\n", d.Namespace, d.Name, *d.Spec.Replicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas)
		}
		w.Flush()
	}

	// DaemonSets (Common for networking/logging agents)
	dss, err := clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	if err == nil {
		fmt.Printf("\n>>> DaemonSets\n")
		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintf(w, "NAMESPACE\tNAME\tDESIRED\tCURRENT\tREADY\n")
		for _, ds := range dss.Items {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\n", ds.Namespace, ds.Name, ds.Status.DesiredNumberScheduled, ds.Status.CurrentNumberScheduled, ds.Status.NumberReady)
		}
		w.Flush()
	}

	// CronJobs (Hidden tasks that run on schedules)
	cjs, err := clientset.BatchV1().CronJobs("").List(ctx, metav1.ListOptions{})
	if err == nil && len(cjs.Items) > 0 {
		fmt.Printf("\n>>> CronJobs\n")
		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintf(w, "NAMESPACE\tNAME\tSCHEDULE\tSUSPEND\tLAST-RUN\n")
		for _, cj := range cjs.Items {
			lastRun := "<none>"
			if cj.Status.LastScheduleTime != nil {
				lastRun = cj.Status.LastScheduleTime.Time.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n", cj.Namespace, cj.Name, cj.Spec.Schedule, *cj.Spec.Suspend, lastRun)
		}
		w.Flush()
	}
	return nil
}

// --- 8. L7 INGRESS RULES ---
// This tells you how URLs map to Services (Standard K8s Ingress)
func ingressInventory(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("L7 INGRESS RULES")
	ings, err := clientset.NetworkingV1().Ingresses("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Println("  [!] Skip: Ingress API not accessible or no Ingresses found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintf(w, "NAMESPACE\tNAME\tHOSTS\tADDRESS\tPORTS\n")
	for _, ing := range ings.Items {
		var hosts []string
		for _, rule := range ing.Spec.Rules {
			hosts = append(hosts, rule.Host)
		}
		addr := "<none>"
		if len(ing.Status.LoadBalancer.Ingress) > 0 {
			addr = ing.Status.LoadBalancer.Ingress[0].IP
			if addr == "" {
				addr = ing.Status.LoadBalancer.Ingress[0].Hostname
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t80,443\n", ing.Namespace, ing.Name, strings.Join(hosts, ","), addr)
	}
	w.Flush()
	return nil
}

// --- 9. RBAC PRIVILEGE AUDIT ---
// Finds the "Keys to the Kingdom"
func rbacAdminAudit(clientset *kubernetes.Clientset) error {
	utils.PrintHeader("RBAC PRIVILEGE AUDIT")
	crbs, err := clientset.RbacV1().ClusterRoleBindings().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Println("  [!] Skip: RBAC API not accessible (Insufficient Permissions).")
		return nil
	}

	fmt.Println("Cluster-Admin Role Holders:")
	found := false
	for _, crb := range crbs.Items {
		if crb.RoleRef.Name == "cluster-admin" {
			for _, sub := range crb.Subjects {
				fmt.Printf("  [!] DANGER: %s/%s (%s)\n", sub.Namespace, sub.Name, sub.Kind)
				found = true
			}
		}
	}
	if !found {
		fmt.Println("  [OK] No suspicious cluster-admin bindings detected.")
	}
	return nil
}

// --- 10. API DISCOVERY ---
// Tells you what capabilities this cluster has (Cilium, Istio, etc.)
func apiDiscoveryAudit(clientset *kubernetes.Clientset) {
	utils.PrintHeader("API CAPABILITIES & EXTENSIONS")
	groups, err := clientset.Discovery().ServerGroups()
	if err != nil {
		fmt.Println("  [!] Failed to discover API groups.")
		return
	}

	fmt.Printf("Detected API Groups (%d total):\n", len(groups.Groups))
	var detected []string
	for _, g := range groups.Groups {
		// Just pull the short names for scannability
		name := g.Name
		if name == "" {
			name = "core"
		}
		detected = append(detected, name)
	}
	// Print in a compact list
	fmt.Println("  " + strings.Join(detected, " | "))
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
			kubeadmDefault := "/etc/kubernetes/admin.conf"
			if _, err := os.Stat(k3sDefault); err == nil {
				kubeconfig = k3sDefault
			} else if _, err := os.Stat(kubeadmDefault); err != nil {
				kubeconfig = kubeadmDefault
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

// ===========================================================
//
//	PROGRAMMATIC ENTRY POINTS (used by the TUI)
//
// ===========================================================

// RunInventoryCapture runs the full Kubernetes inventory against the
// cluster reachable via kubeconfigPath (pass "" to use the same discovery
// logic as the CLI: $KUBECONFIG, ~/.kube/config, k3s/kubeadm defaults, or
// in-cluster config) and returns everything it would normally print to
// stdout as a single string. reveal controls whether secret values are
// shown in plaintext or redacted, matching the --reveal flag.
func buildClientset(kubeconfigPath string) (*kubernetes.Clientset, *rest.Config, error) {
	var config *rest.Config
	var err error
	if kubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		config, err = getKubeConfig()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("error getting Kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating client set: %w", err)
	}

	return clientset, config, nil
}

func RunInventoryCapture(kubeconfigPath string, reveal bool) (string, error) {
	prevReveal := revealSecrets
	revealSecrets = reveal
	defer func() { revealSecrets = prevReveal }()

	clientset, config, err := buildClientset(kubeconfigPath)
	if err != nil {
		return "", err
	}

	return utils.CaptureStdout(func() {
		runInventory(clientset, config)
	})
}

// RollCredentialsCapture rotates the given secret sequence(s) - same
// SECRET_NAME,NAMESPACE,KEY,STRATEGY[|...] syntax as the -s CLI flag - and
// returns everything it would normally print to stdout. This is
// destructive (it overwrites secret values); callers (like the TUI)
// should confirm with the user before invoking it.
func RollCredentialsCapture(kubeconfigPath string, sequences string, reveal bool) (string, error) {
	prevReveal := revealSecrets
	revealSecrets = reveal
	defer func() { revealSecrets = prevReveal }()

	prevSeq := credSequences
	credSequences = sequences
	defer func() { credSequences = prevSeq }()

	clientset, config, err := buildClientset(kubeconfigPath)
	if err != nil {
		return "", err
	}

	return utils.CaptureStdout(func() {
		rollCredentials(clientset, config)
	})
}

// ===========================================================
//
//	BROWSE PRIMITIVES (used by the TUI's interactive secret browser)
//
// ===========================================================

// GenerateRandomSecretValue returns a fresh random value using the same
// generator rotate() uses, so a TUI-triggered "random" rotation is
// identical in strength to a CLI-triggered one.
func GenerateRandomSecretValue(length int) string {
	if length <= 0 {
		length = 40
	}
	return randomizeString(length)
}

// ListNamespaces returns every namespace name in the cluster, sorted.
func ListNamespaces(kubeconfigPath string) ([]string, error) {
	clientset, _, err := buildClientset(kubeconfigPath)
	if err != nil {
		return nil, err
	}

	nsList, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not list namespaces: %w", err)
	}

	names := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names, nil
}

// SecretSummary is a lightweight view of a secret for TUI browsing - no
// values included, just enough to list and select from.
type SecretSummary struct {
	Name string
	Tag  string
	Keys []string
}

// ListSecrets returns a summary of every secret in the given namespace,
// sorted by name.
func ListSecrets(kubeconfigPath, namespace string) ([]SecretSummary, error) {
	clientset, _, err := buildClientset(kubeconfigPath)
	if err != nil {
		return nil, err
	}

	secrets, err := clientset.CoreV1().Secrets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not list secrets in %s: %w", namespace, err)
	}

	out := make([]SecretSummary, 0, len(secrets.Items))
	for _, secret := range secrets.Items {
		keys := make([]string, 0, len(secret.Data))
		for k := range secret.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, SecretSummary{
			Name: secret.Name,
			Tag:  fmt.Sprintf("%v", getSecretTag(secret)),
			Keys: keys,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetSecretKeyValue returns the decoded value of one key in one secret.
// The caller is responsible for redacting/displaying it appropriately -
// this always returns plaintext.
func GetSecretKeyValue(kubeconfigPath, namespace, secretName, key string) (string, error) {
	clientset, _, err := buildClientset(kubeconfigPath)
	if err != nil {
		return "", err
	}

	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.TODO(), secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("could not get secret %s.%s: %w", namespace, secretName, err)
	}

	value, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s.%s has no key %q", namespace, secretName, key)
	}
	return string(value), nil
}

// RotateSecretValueCapture rotates exactly one secret key - to a fresh
// random value if useRandom is true, otherwise to userValue - and returns
// everything it would normally print to stdout. strategy is "retainPrev"
// or "omitPrev", same meaning as the CLI's -s flag. This calls into the
// same rotateSecret() the CLI's multi-sequence rotation uses, so a
// TUI-triggered rotation behaves identically to a CLI one.
func RotateSecretValueCapture(kubeconfigPath, namespace, secretName, key, strategy string, useRandom bool, userValue string, reveal bool) (string, error) {
	prevReveal := revealSecrets
	revealSecrets = reveal
	defer func() { revealSecrets = prevReveal }()

	clientset, _, err := buildClientset(kubeconfigPath)
	if err != nil {
		return "", err
	}

	var newValue []byte
	if useRandom {
		newValue = []byte(randomizeString(40))
	} else {
		newValue = []byte(userValue)
	}

	def := secretDef{name: secretName, namespace: namespace, key: key, strategy: strategy}

	return utils.CaptureStdout(func() {
		rotateSecret(clientset, def, newValue)
	})
}
