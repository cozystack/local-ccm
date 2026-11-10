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
	"errors"
	"fmt"
	"net"
	"slices"
	"syscall"

	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
)

// DetectIPs detects the local IP address by using netlink to query the route
// to the target IPs and extracting the source IPs from the routes
func DetectIPs(targetIPs []net.IP) ([]string, error) {
	var srcIPs []string

	for _, targetIP := range targetIPs {
		klog.V(4).Infof("Detecting IP using target: %s", targetIP)

		// Get route to target IP using netlink
		routes, err := netlink.RouteGet(targetIP)
		if err != nil {
			if errors.Is(err, syscall.ENETUNREACH) {
				klog.V(4).Infof("Skipping target due to ENETUNREACH: %s", targetIP)
				continue
			}

			return nil, fmt.Errorf("failed to get route to %s: %w", targetIP, err)
		}

		if len(routes) == 0 {
			klog.V(4).Infof("No routes to target: %s", targetIP)
			continue
		}

		// Get the first route (preferred route)
		route := routes[0]

		// Extract source IP from route
		if route.Src == nil {
			klog.V(4).Infof("Route to %s has no source IP, skipping", targetIP)
			continue
		}

		detectedIP := route.Src.String()

		if slices.Contains(srcIPs, detectedIP) {
			continue
		}

		klog.V(4).Infof("Detected IP: %s (target: %s)", detectedIP, targetIP)
		srcIPs = append(srcIPs, detectedIP)
	}

	if len(srcIPs) == 0 {
		return nil, fmt.Errorf("no route found to any of %v", targetIPs)
	}

	return srcIPs, nil
}
