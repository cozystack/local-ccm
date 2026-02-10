/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/cozystack/local-ccm/pkg/checker"
)

// AutoscalerTaintKey is the taint key set by cluster-autoscaler on nodes marked for deletion.
const AutoscalerTaintKey = "ToBeDeletedByClusterAutoscaler"

// Config holds the controller configuration
type Config struct {
	NodeSelector         string
	ProtectedLabels      string
	NotReadyTimeout      time.Duration
	PingTimeout          time.Duration
	PingCount            int
	ReconcileInterval    time.Duration
	DryRun               bool
	WatchAutoscalerTaint bool
}

// Controller manages node lifecycle
type Controller struct {
	client          kubernetes.Interface
	checker         *checker.Checker
	config          Config
	protectedLabels []string

	// Track when nodes first became NotReady
	notReadySince map[string]time.Time
	// Track last log time for "waiting" messages to avoid spam
	lastWaitingLog map[string]time.Time
}

// New creates a new controller
func New(client kubernetes.Interface, config Config) *Controller {
	// Parse protected labels
	var protectedLabels []string
	if config.ProtectedLabels != "" {
		for _, label := range strings.Split(config.ProtectedLabels, ",") {
			label = strings.TrimSpace(label)
			if label != "" {
				protectedLabels = append(protectedLabels, label)
			}
		}
	}

	if len(protectedLabels) > 0 {
		klog.Infof("Protected labels (nodes with these labels will NOT be deleted): %v", protectedLabels)
	}

	return &Controller{
		client:          client,
		checker:         checker.New(config.PingTimeout, config.PingCount),
		config:          config,
		protectedLabels: protectedLabels,
		notReadySince:   make(map[string]time.Time),
		lastWaitingLog:  make(map[string]time.Time),
	}
}

// Run starts the controller loop
func (c *Controller) Run(ctx context.Context) {
	klog.Info("Starting controller loop")

	ticker := time.NewTicker(c.config.ReconcileInterval)
	defer ticker.Stop()

	// Run immediately on start
	c.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			klog.Info("Controller stopped")
			return
		case <-ticker.C:
			c.reconcile(ctx)
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) {
	klog.V(4).Info("Starting reconciliation")

	nodes, err := c.listManagedNodes(ctx)
	if err != nil {
		klog.Errorf("Failed to list nodes: %v", err)
		return
	}

	klog.V(4).Infof("Found %d managed nodes", len(nodes))

	// Track currently existing nodes to clean up notReadySince map
	currentNodes := make(map[string]bool)

	for _, node := range nodes {
		currentNodes[node.Name] = true
		c.processNode(ctx, &node)
	}

	// Clean up tracking for deleted nodes
	for nodeName := range c.notReadySince {
		if !currentNodes[nodeName] {
			delete(c.notReadySince, nodeName)
			delete(c.lastWaitingLog, nodeName)
		}
	}
}

func (c *Controller) listManagedNodes(ctx context.Context) ([]corev1.Node, error) {
	listOpts := metav1.ListOptions{}
	if c.config.NodeSelector != "" {
		listOpts.LabelSelector = c.config.NodeSelector
	}

	nodeList, err := c.client.CoreV1().Nodes().List(ctx, listOpts)
	if err != nil {
		return nil, err
	}

	// Filter out control-plane nodes and apply taint filtering
	filterByTaint := c.config.NodeSelector == "" && c.config.WatchAutoscalerTaint
	var managedNodes []corev1.Node
	for _, node := range nodeList.Items {
		if isControlPlane(&node) {
			klog.V(3).Infof("Skipping control-plane node %s", node.Name)
			continue
		}
		if filterByTaint && !hasAutoscalerTaint(&node) {
			klog.V(4).Infof("Skipping node %s: missing %s taint", node.Name, AutoscalerTaintKey)
			continue
		}
		managedNodes = append(managedNodes, node)
	}

	return managedNodes, nil
}

// hasAutoscalerTaint checks if node has the cluster-autoscaler deletion taint with NoSchedule effect.
func hasAutoscalerTaint(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Key == AutoscalerTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
			return true
		}
	}
	return false
}

func (c *Controller) processNode(ctx context.Context, node *corev1.Node) {
	nodeName := node.Name

	// Check if node is protected by labels
	if c.isProtected(node) {
		klog.V(3).Infof("Node %s is protected by label, skipping", nodeName)
		return
	}

	isReady := isNodeReady(node)

	if isReady {
		// Node is ready, clear tracking
		if _, tracked := c.notReadySince[nodeName]; tracked {
			klog.Infof("Node %s is now Ready, clearing NotReady tracking", nodeName)
			delete(c.notReadySince, nodeName)
			delete(c.lastWaitingLog, nodeName)
		}
		return
	}

	// Node is NotReady
	now := time.Now()

	// Start tracking if not already
	if _, tracked := c.notReadySince[nodeName]; !tracked {
		c.notReadySince[nodeName] = now
		klog.Infof("Node %s is NotReady, starting tracking", nodeName)
		return
	}

	// Check if timeout exceeded
	notReadyDuration := now.Sub(c.notReadySince[nodeName])
	if notReadyDuration < c.config.NotReadyTimeout {
		// Log "waiting" only once per minute to reduce log spam
		if lastLog, exists := c.lastWaitingLog[nodeName]; !exists || now.Sub(lastLog) >= time.Minute {
			klog.V(2).Infof("Node %s has been NotReady for %v (timeout: %v), waiting",
				nodeName, notReadyDuration.Round(time.Second), c.config.NotReadyTimeout)
			c.lastWaitingLog[nodeName] = now
		}
		return
	}
	// Clear waiting log tracker when proceeding to deletion check
	delete(c.lastWaitingLog, nodeName)

	klog.Infof("Node %s has been NotReady for %v, checking reachability",
		nodeName, notReadyDuration.Round(time.Second))

	// Skip ping check if disabled (pingCount=0)
	if c.config.PingCount > 0 {
		// Get internal IP for ping check
		internalIP := getInternalIP(node)
		if internalIP == "" {
			klog.Warningf("Node %s has no InternalIP, cannot perform ping check, skipping", nodeName)
			return
		}

		// Perform ping check
		reachable, err := c.checker.IsReachable(internalIP)
		if err != nil {
			// Ping check failed due to error (e.g., no permissions) - do NOT delete, skip this node
			klog.Errorf("Ping check error for node %s (%s): %v - skipping deletion (ping check required)", nodeName, internalIP, err)
			return
		}

		if reachable {
			klog.Infof("Node %s (%s) is reachable via ping, not deleting", nodeName, internalIP)
			return
		}

		klog.Infof("Node %s (%s) is NOT reachable via ping", nodeName, internalIP)
	} else {
		klog.V(2).Infof("Ping check disabled (ping-count=0), skipping for node %s", nodeName)
	}

	// Node is NotReady and unreachable - delete it
	c.deleteNode(ctx, nodeName)
}

func (c *Controller) deleteNode(ctx context.Context, nodeName string) {
	if c.config.DryRun {
		klog.Infof("[DRY-RUN] Would delete node %s", nodeName)
		return
	}

	klog.Infof("Deleting node %s", nodeName)

	err := c.client.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	if err != nil {
		klog.Errorf("Failed to delete node %s: %v", nodeName, err)
		return
	}

	klog.Infof("Successfully deleted node %s", nodeName)
	delete(c.notReadySince, nodeName)
}

// isNodeReady checks if node has Ready condition with status True
func isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// isControlPlane checks if node is a control-plane node
func isControlPlane(node *corev1.Node) bool {
	labels := node.Labels
	if labels == nil {
		return false
	}

	// Check for control-plane labels
	if _, ok := labels["node-role.kubernetes.io/control-plane"]; ok {
		return true
	}
	if _, ok := labels["node-role.kubernetes.io/master"]; ok {
		return true
	}

	return false
}

// getInternalIP returns the InternalIP of the node
func getInternalIP(node *corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

// FormatDuration formats duration in a human-readable way
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// isProtected checks if node has any of the protected labels
func (c *Controller) isProtected(node *corev1.Node) bool {
	if len(c.protectedLabels) == 0 {
		return false
	}

	labels := node.Labels
	if labels == nil {
		return false
	}

	for _, protectedLabel := range c.protectedLabels {
		// Check if it's a key=value pair or just a key
		if strings.Contains(protectedLabel, "=") {
			parts := strings.SplitN(protectedLabel, "=", 2)
			key, value := parts[0], parts[1]
			if nodeValue, exists := labels[key]; exists && nodeValue == value {
				klog.V(4).Infof("Node %s is protected by label %s=%s", node.Name, key, value)
				return true
			}
		} else {
			// Just check if key exists (any value)
			if _, exists := labels[protectedLabel]; exists {
				klog.V(4).Infof("Node %s is protected by label key %s", node.Name, protectedLabel)
				return true
			}
		}
	}

	// Also check annotations
	annotations := node.Annotations
	if annotations != nil {
		for _, protectedLabel := range c.protectedLabels {
			if strings.Contains(protectedLabel, "=") {
				parts := strings.SplitN(protectedLabel, "=", 2)
				key, value := parts[0], parts[1]
				if nodeValue, exists := annotations[key]; exists && nodeValue == value {
					klog.V(4).Infof("Node %s is protected by annotation %s=%s", node.Name, key, value)
					return true
				}
			} else {
				if _, exists := annotations[protectedLabel]; exists {
					klog.V(4).Infof("Node %s is protected by annotation key %s", node.Name, protectedLabel)
					return true
				}
			}
		}
	}

	return false
}
