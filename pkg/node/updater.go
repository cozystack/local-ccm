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

package node

import (
	"context"
	"encoding/json"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	// TaintKey is the taint key set by kubelet when --cloud-provider=external
	TaintKey = "node.cloudprovider.kubernetes.io/uninitialized"
)

// Updater handles updating node addresses and removing taints
type Updater struct {
	client   kubernetes.Interface
	nodeName string
}

// NewUpdater creates a new node updater
func NewUpdater(client kubernetes.Interface, nodeName string) *Updater {
	return &Updater{
		client:   client,
		nodeName: nodeName,
	}
}

// UpdateAddresses updates the node's status addresses
func (u *Updater) UpdateAddresses(ctx context.Context, addresses []v1.NodeAddress) error {
	klog.V(2).Infof("Updating addresses for node %s: %v", u.nodeName, addresses)

	// Create JSON patch for addresses
	patch := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/status/addresses",
			"value": addresses,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	klog.V(4).Infof("Applying patch to node %s: %s", u.nodeName, string(patchBytes))

	// Apply patch
	_, err = u.client.CoreV1().Nodes().Patch(
		ctx,
		u.nodeName,
		types.JSONPatchType,
		patchBytes,
		metav1.PatchOptions{},
		"status",
	)
	if err != nil {
		return fmt.Errorf("failed to patch node addresses: %w", err)
	}

	klog.Infof("Successfully updated addresses for node %s", u.nodeName)
	return nil
}

// RemoveTaint removes the cloud provider taint from the node
func (u *Updater) RemoveTaint(ctx context.Context) error {
	klog.V(2).Infof("Removing taint %s from node %s", TaintKey, u.nodeName)

	// Get current node
	node, err := u.client.CoreV1().Nodes().Get(ctx, u.nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	// Check if taint exists
	taintIndex := -1
	for i, taint := range node.Spec.Taints {
		if taint.Key == TaintKey {
			taintIndex = i
			break
		}
	}

	if taintIndex == -1 {
		klog.V(3).Infof("Taint %s not found on node %s, skipping removal", TaintKey, u.nodeName)
		return nil
	}

	// Remove taint
	newTaints := append(node.Spec.Taints[:taintIndex], node.Spec.Taints[taintIndex+1:]...)

	// Create JSON patch for taints
	patch := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/spec/taints",
			"value": newTaints,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	klog.V(4).Infof("Applying taint removal patch to node %s: %s", u.nodeName, string(patchBytes))

	// Apply patch
	_, err = u.client.CoreV1().Nodes().Patch(
		ctx,
		u.nodeName,
		types.JSONPatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to remove taint: %w", err)
	}

	klog.Infof("Successfully removed taint %s from node %s", TaintKey, u.nodeName)
	return nil
}

// GetNode retrieves the current node object
func (u *Updater) GetNode(ctx context.Context) (*v1.Node, error) {
	return u.client.CoreV1().Nodes().Get(ctx, u.nodeName, metav1.GetOptions{})
}
