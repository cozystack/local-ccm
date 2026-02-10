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

package checker

import (
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"k8s.io/klog/v2"
)

// Checker performs health checks on nodes
type Checker struct {
	timeout time.Duration
	count   int
}

// New creates a new checker
func New(timeout time.Duration, count int) *Checker {
	return &Checker{
		timeout: timeout,
		count:   count,
	}
}

// IsReachable checks if the given IP is reachable via ICMP ping
func (c *Checker) IsReachable(ip string) (bool, error) {
	klog.V(3).Infof("Pinging %s (count=%d, timeout=%v)", ip, c.count, c.timeout)

	pinger, err := probing.NewPinger(ip)
	if err != nil {
		return false, err
	}

	// Configure pinger
	pinger.Count = c.count
	pinger.Timeout = c.timeout
	pinger.SetPrivileged(true) // Requires CAP_NET_RAW or root

	err = pinger.Run()
	if err != nil {
		return false, err
	}

	stats := pinger.Statistics()

	klog.V(3).Infof("Ping stats for %s: sent=%d, received=%d, loss=%.1f%%",
		ip, stats.PacketsSent, stats.PacketsRecv, stats.PacketLoss)

	// Consider reachable if at least one packet was received
	return stats.PacketsRecv > 0, nil
}
