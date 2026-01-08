/*
Copyright 2025 The local-ccm Authors.

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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/cozystack/local-ccm/pkg/detector"
	"github.com/cozystack/local-ccm/pkg/node"
)

var (
	nodeName          string
	kubeconfig        string
	internalIPTarget  string
	externalIPTarget  string
	runOnce           bool
	removeTaint       bool
	reconcileInterval time.Duration
)

func init() {
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Name of the node to update (env: NODE_NAME)")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (for local testing)")
	flag.StringVar(&internalIPTarget, "internal-ip-target", "", "Target IP for internal IP detection via 'ip route get'. If empty, internal IP detection is disabled")
	flag.StringVar(&externalIPTarget, "external-ip-target", "8.8.8.8", "Target IP for external IP detection via 'ip route get'")
	flag.BoolVar(&runOnce, "run-once", false, "Run once and exit instead of running in a loop")
	flag.BoolVar(&removeTaint, "remove-taint", true, "Remove node.cloudprovider.kubernetes.io/uninitialized taint")
	flag.DurationVar(&reconcileInterval, "reconcile-interval", 10*time.Second, "Interval between reconciliation loops")

	klog.InitFlags(nil)
}

func main() {
	flag.Parse()

	if nodeName == "" {
		klog.Fatal("--node-name or NODE_NAME environment variable must be set")
	}

	klog.Infof("Starting local-ccm for node %s", nodeName)
	klog.V(2).Infof("Configuration: internalIPTarget=%q externalIPTarget=%q",
		internalIPTarget, externalIPTarget)

	// Create Kubernetes client
	k8sClient, err := createKubernetesClient(kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Create node updater
	nodeUpdater := node.NewUpdater(k8sClient, nodeName)

	ctx := context.Background()

	// Main reconciliation loop
	for {
		if err := reconcile(ctx, nodeUpdater); err != nil {
			klog.Errorf("Reconciliation failed: %v", err)
			if runOnce {
				os.Exit(1)
			}
		} else {
			klog.Infof("Reconciliation completed successfully")
			if runOnce {
				os.Exit(0)
			}
		}

		klog.V(2).Infof("Sleeping for %v until next reconciliation", reconcileInterval)
		time.Sleep(reconcileInterval)
	}
}

func reconcile(ctx context.Context, nodeUpdater *node.Updater) error {
	klog.V(2).Infof("Starting reconciliation for node %s", nodeName)

	// Get current node
	currentNode, err := nodeUpdater.GetNode(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	// Start with existing addresses
	addressMap := make(map[v1.NodeAddressType]string)
	for _, addr := range currentNode.Status.Addresses {
		addressMap[addr.Type] = addr.Address
	}

	// Detect Internal IP if configured
	if internalIPTarget != "" {
		klog.V(3).Infof("Detecting internal IP using target %s", internalIPTarget)
		internalIP, err := detector.DetectIP(internalIPTarget)
		if err != nil {
			return fmt.Errorf("failed to detect internal IP: %w", err)
		}
		klog.V(2).Infof("Detected internal IP: %s", internalIP)
		addressMap[v1.NodeInternalIP] = internalIP
	}
	// If internalIPTarget is not set, preserve existing InternalIP (e.g., set by kubelet)

	// Always detect and update External IP
	klog.V(3).Infof("Detecting external IP using target %s", externalIPTarget)
	detectedExternalIP, err := detector.DetectIP(externalIPTarget)
	if err != nil {
		return fmt.Errorf("failed to detect external IP: %w", err)
	}
	klog.V(2).Infof("Detected external IP: %s", detectedExternalIP)

	// Check if external IP equals internal IP - if so, don't set external IP
	if internalIP, hasInternal := addressMap[v1.NodeInternalIP]; hasInternal && internalIP == detectedExternalIP {
		klog.V(2).Infof("External IP %s matches internal IP, removing external IP from addresses", detectedExternalIP)
		delete(addressMap, v1.NodeExternalIP)
	} else {
		addressMap[v1.NodeExternalIP] = detectedExternalIP
	}

	// Convert map back to slice
	addresses := make([]v1.NodeAddress, 0, len(addressMap))
	for addrType, addrValue := range addressMap {
		addresses = append(addresses, v1.NodeAddress{
			Type:    addrType,
			Address: addrValue,
		})
	}

	// Check if addresses changed
	if addressesEqual(currentNode.Status.Addresses, addresses) {
		klog.V(3).Info("Addresses unchanged, skipping update")
	} else {
		klog.Info("Addresses changed, updating node")
		if err := nodeUpdater.UpdateAddresses(ctx, addresses); err != nil {
			return fmt.Errorf("failed to update addresses: %w", err)
		}
	}

	// Remove taint if requested
	if removeTaint {
		if err := nodeUpdater.RemoveTaint(ctx); err != nil {
			return fmt.Errorf("failed to remove taint: %w", err)
		}
	}

	return nil
}

func createKubernetesClient(kubeconfigPath string) (kubernetes.Interface, error) {
	var restConfig *rest.Config
	var err error

	if kubeconfigPath != "" {
		klog.V(2).Infof("Using kubeconfig from %s", kubeconfigPath)
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		klog.V(2).Info("Using in-cluster config")
		restConfig, err = rest.InClusterConfig()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create rest config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return client, nil
}

// addressesEqual checks if two address slices are equal
func addressesEqual(a, b []v1.NodeAddress) bool {
	if len(a) != len(b) {
		return false
	}

	// Create maps for comparison
	aMap := make(map[string]string)
	bMap := make(map[string]string)

	for _, addr := range a {
		aMap[string(addr.Type)] = addr.Address
	}
	for _, addr := range b {
		bMap[string(addr.Type)] = addr.Address
	}

	// Compare maps
	for k, v := range aMap {
		if bMap[k] != v {
			return false
		}
	}

	return true
}
