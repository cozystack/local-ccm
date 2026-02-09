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

package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	"github.com/cozystack/local-ccm/pkg/controller"
)

var (
	kubeconfig        string
	nodeSelector      string
	protectedLabels   string
	notReadyTimeout   time.Duration
	pingTimeout       time.Duration
	pingCount         int
	reconcileInterval time.Duration
	leaderElect       bool
	leaderElectID     string
	namespace         string
	dryRun            bool
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (for local testing)")
	flag.StringVar(&nodeSelector, "node-selector", "", "Label selector for nodes to manage (e.g., 'node.kubernetes.io/instance-type')")
	flag.StringVar(&protectedLabels, "protected-labels", "", "Comma-separated list of label keys that protect nodes from deletion (e.g., 'kilo.squat.ai/leader,important-node')")
	flag.DurationVar(&notReadyTimeout, "not-ready-timeout", 5*time.Minute, "Duration a node must be NotReady before considering deletion")
	flag.DurationVar(&pingTimeout, "ping-timeout", 5*time.Second, "Timeout for ping checks")
	flag.IntVar(&pingCount, "ping-count", 3, "Number of ping attempts before considering node unreachable")
	flag.DurationVar(&reconcileInterval, "reconcile-interval", 30*time.Second, "Interval between reconciliation loops")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Enable leader election for HA")
	flag.StringVar(&leaderElectID, "leader-elect-id", "node-lifecycle-controller", "Name of the leader election resource")
	flag.StringVar(&namespace, "namespace", os.Getenv("POD_NAMESPACE"), "Namespace for leader election (env: POD_NAMESPACE)")
	flag.BoolVar(&dryRun, "dry-run", false, "Log actions without actually deleting nodes")

	klog.InitFlags(nil)
}

func main() {
	flag.Parse()

	if namespace == "" {
		namespace = "kube-system"
	}

	klog.Infof("Starting node-lifecycle-controller")
	klog.Infof("Configuration: nodeSelector=%q protectedLabels=%q notReadyTimeout=%v pingTimeout=%v pingCount=%d dryRun=%v",
		nodeSelector, protectedLabels, notReadyTimeout, pingTimeout, pingCount, dryRun)

	// Create Kubernetes client
	k8sClient, err := createKubernetesClient(kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Create controller
	ctrl := controller.New(k8sClient, controller.Config{
		NodeSelector:      nodeSelector,
		ProtectedLabels:   protectedLabels,
		NotReadyTimeout:   notReadyTimeout,
		PingTimeout:       pingTimeout,
		PingCount:         pingCount,
		ReconcileInterval: reconcileInterval,
		DryRun:            dryRun,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		klog.Info("Received shutdown signal")
		cancel()
	}()

	if leaderElect {
		runWithLeaderElection(ctx, k8sClient, ctrl)
	} else {
		ctrl.Run(ctx)
	}
}

func runWithLeaderElection(ctx context.Context, client kubernetes.Interface, ctrl *controller.Controller) {
	id, err := os.Hostname()
	if err != nil {
		klog.Fatalf("Failed to get hostname: %v", err)
	}

	lock, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		namespace,
		leaderElectID,
		client.CoreV1(),
		client.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity: id,
		},
	)
	if err != nil {
		klog.Fatalf("Failed to create leader election lock: %v", err)
	}

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				klog.Info("Started leading")
				ctrl.Run(ctx)
			},
			OnStoppedLeading: func() {
				klog.Info("Stopped leading")
			},
			OnNewLeader: func(identity string) {
				if identity != id {
					klog.Infof("New leader elected: %s", identity)
				}
			},
		},
	})
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
		return nil, err
	}

	return kubernetes.NewForConfig(restConfig)
}
