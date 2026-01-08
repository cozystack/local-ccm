/*
Copyright 2025 The simple-ccm Authors.

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
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
)

// DetectIP detects the local IP address by using netlink to query the route
// to the target IP and extracting the source IP from the route
func DetectIP(targetIP string) (string, error) {
	if targetIP == "" {
		return "", fmt.Errorf("target IP is empty")
	}

	// Parse target IP
	dstIP := net.ParseIP(targetIP)
	if dstIP == nil {
		return "", fmt.Errorf("invalid target IP address: %s", targetIP)
	}

	klog.V(4).Infof("Detecting IP using target: %s", targetIP)

	// Get route to target IP using netlink
	routes, err := netlink.RouteGet(dstIP)
	if err != nil {
		return "", fmt.Errorf("failed to get route to %s: %w", targetIP, err)
	}

	if len(routes) == 0 {
		return "", fmt.Errorf("no route found to %s", targetIP)
	}

	// Get the first route (preferred route)
	route := routes[0]

	// Extract source IP from route
	if route.Src == nil {
		return "", fmt.Errorf("route to %s has no source IP", targetIP)
	}

	detectedIP := route.Src.String()

	klog.V(4).Infof("Detected IP: %s (target: %s)", detectedIP, targetIP)

	return detectedIP, nil
}
