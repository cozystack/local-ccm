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
	"errors"
	"flag"
	"fmt"
	"maps"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/cozystack/local-ccm/pkg/detector"
	"github.com/cozystack/local-ccm/pkg/node"
)

const kubeletNodeIPAnnotation = "alpha.kubernetes.io/provided-node-ip"

var (
	nodeName          string
	kubeconfig        string
	internalIPTargets ipAddrs
	externalIPTargets ipAddrs
	runOnce           bool
	removeTaint       bool
	reconcileInterval time.Duration
)

type ipAddrs []net.IP

// String is an implementation of the flag.Value interface
func (i *ipAddrs) String() string {
	if i == nil {
		return "<nil>"
	}

	return fmt.Sprintf("%v", *i)
}

// Set is an implementation of the flag.Value interface
func (i *ipAddrs) Set(value string) error {
	values := strings.Split(value, ",")
	for _, value := range values {
		ip := net.ParseIP(value)
		if ip == nil {
			return fmt.Errorf("invalid target IP address: %q", value)
		}

		*i = append(*i, ip)
	}

	return nil
}

func init() {
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Name of the node to update (env: NODE_NAME)")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (for local testing)")
	flag.Var(&internalIPTargets, "internal-ip-target", "One or more target IP(s) for internal IP detection via 'ip route get'. If empty, internal IP detection is disabled")
	flag.Var(&externalIPTargets, "external-ip-target", "One or more target IP(s) for external IP detection via 'ip route get' (Default 8.8.8.8)")
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

	if len(externalIPTargets) == 0 {
		externalIPTargets = []net.IP{net.ParseIP("8.8.8.8")}
	}

	klog.Infof("Starting local-ccm for node %s", nodeName)
	klog.V(2).Infof("Configuration: internalIPTarget=%q externalIPTarget=%q",
		internalIPTargets, externalIPTargets)

	// Create Kubernetes client
	k8sClient, err := createKubernetesClient(kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Create node updater
	nodeUpdater := node.NewUpdater(k8sClient, nodeName)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Main reconciliation loop
	for {
		if err := reconcile(ctx, nodeUpdater); err != nil {
			klog.Errorf("Reconciliation failed: %v", err)
			if runOnce {
				os.Exit(1)
			}
			if errors.Is(err, context.Canceled) {
				os.Exit(0)
			}
		} else {
			klog.Infof("Reconciliation completed successfully")
			if runOnce {
				os.Exit(0)
			}
		}

		klog.V(2).Infof("Sleeping for %v until next reconciliation", reconcileInterval)
		select {
		case <-ctx.Done():
			os.Exit(0)
		case <-time.After(reconcileInterval):
		}
	}
}

func reconcile(ctx context.Context, nodeUpdater *node.Updater) error {
	klog.V(2).Infof("Starting reconciliation for node %s", nodeName)

	// Get current node
	currentNode, err := nodeUpdater.GetNode(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	addresses := slices.Clone(currentNode.Status.Addresses)

	var (
		nodeIPAnnotationValue   string
		kubeletProvidedAddreses []v1.NodeAddress
	)

	if currentNode.Annotations != nil {
		// With --cloud-provider=external, the kubelet stops updating the addreses,
		// instead, it stores --node-ip, if any, in an annotation on startup. It's up
		// to the CCM to whether it uses that to set the InternalIP, which we do down
		// below if no explicit internalIPTargets are provided
		if annotation, exists := currentNode.Annotations[kubeletNodeIPAnnotation]; exists {
			nodeIPAnnotationValue = annotation
			nodeIPs := strings.Split(annotation, ",")
			for _, nodeIP := range nodeIPs {
				kubeletProvidedAddreses = append(kubeletProvidedAddreses, v1.NodeAddress{
					Type:    v1.NodeInternalIP,
					Address: nodeIP,
				})
			}
		}
	}

	// Detect Internal IP if configured, otherwise preserve existing InternalIP
	// (e.g., set by kubelet)
	if len(internalIPTargets) != 0 {
		// Drop all existing Internal IP addreses
		addresses = slices.DeleteFunc(addresses, func(address v1.NodeAddress) bool {
			return address.Type == v1.NodeInternalIP
		})

		klog.V(3).Infof("Detecting internal IP using target(s) %s", internalIPTargets)
		internalIPs, err := detector.DetectIPs(internalIPTargets)
		if err != nil {
			return fmt.Errorf("failed to detect internal IP: %w", err)
		}
		klog.V(2).Infof("Detected internal IP(s): %s", strings.Join(internalIPs, ", "))

		for _, internalIP := range internalIPs {
			addresses = append(addresses, v1.NodeAddress{
				Type:    v1.NodeInternalIP,
				Address: internalIP,
			})
		}
	} else if len(kubeletProvidedAddreses) != 0 {
		// Drop all existing Internal IP addreses
		addresses = slices.DeleteFunc(addresses, func(address v1.NodeAddress) bool {
			return address.Type == v1.NodeInternalIP
		})

		klog.V(2).Infof("Using kubelet provided internal IP(s): %s", nodeIPAnnotationValue)
		addresses = append(addresses, kubeletProvidedAddreses...)
	}

	// Drop all existing External IP addreses
	addresses = slices.DeleteFunc(addresses, func(address v1.NodeAddress) bool {
		return address.Type == v1.NodeExternalIP
	})

	// Always detect and update External IP
	klog.V(3).Infof("Detecting external IP using target(s) %s", externalIPTargets)
	externalIPs, err := detector.DetectIPs(externalIPTargets)
	if err != nil {
		return fmt.Errorf("failed to detect external IP: %w", err)
	}
	klog.V(2).Infof("Detected external IP(s): %s", strings.Join(externalIPs, ", "))

	for _, externalIP := range externalIPs {
		// Check if external IP equals internal IP - if so, don't set external IP
		if slices.ContainsFunc(addresses, func(address v1.NodeAddress) bool {
			return address.Address == externalIP
		}) {
			klog.V(2).Infof("External IP %s matches internal IP, removing external IP from addresses", externalIP)
			continue
		}

		addresses = append(addresses, v1.NodeAddress{
			Type:    v1.NodeExternalIP,
			Address: externalIP,
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

// addressesEqual checks if two address slices are equal, regardless of order
func addressesEqual(a, b []v1.NodeAddress) bool {
	if len(a) != len(b) {
		return false
	}

	// Create maps for comparison
	aMap := make(map[string]struct{})
	bMap := make(map[string]struct{})

	for _, addr := range a {
		aMap[addr.String()] = struct{}{}
	}
	for _, addr := range b {
		bMap[addr.String()] = struct{}{}
	}

	// Compare maps
	return maps.Equal(aMap, bMap)
}
