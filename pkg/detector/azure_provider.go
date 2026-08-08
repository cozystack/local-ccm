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

package detector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	// AzureIMDSURL is the Azure Instance Metadata Service endpoint
	AzureIMDSURL = "http://169.254.169.254/metadata/instance/compute/resourceId?api-version=2021-02-01&format=text"

	// AzureIMDSTimeout is the timeout for IMDS requests
	AzureIMDSTimeout = 2 * time.Second
)

// DetectAzureProviderID detects Azure provider ID from Instance Metadata Service
// Returns empty string if not running in Azure environment
func DetectAzureProviderID(ctx context.Context) (string, error) {
	klog.V(3).Info("Attempting to detect Azure provider ID from IMDS")

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: AzureIMDSTimeout,
	}

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, AzureIMDSURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create IMDS request: %w", err)
	}

	// Azure IMDS requires Metadata header
	req.Header.Set("Metadata", "true")

	klog.V(4).Infof("Sending request to Azure IMDS: %s", AzureIMDSURL)

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		// This is expected when not running in Azure
		klog.V(3).Infof("Azure IMDS not available (likely not running in Azure): %v", err)
		return "", nil
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		klog.V(2).Infof("Azure IMDS returned non-OK status: %d", resp.StatusCode)
		return "", nil
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read IMDS response: %w", err)
	}

	resourceID := strings.TrimSpace(string(body))
	if resourceID == "" {
		klog.V(2).Info("Azure IMDS returned empty resource ID")
		return "", nil
	}

	// Format as Kubernetes provider ID
	providerID := fmt.Sprintf("azure://%s", resourceID)

	klog.V(2).Infof("Detected Azure provider ID: %s", providerID)

	return providerID, nil
}
