# local-ccm

A lightweight Kubernetes cloud controller for bare-metal and on-premise clusters. Provides automatic node IP detection and cleanup of unreachable nodes.

## Overview

`local-ccm` includes two components:

- **local-ccm** — a DaemonSet that automatically detects and sets `NodeInternalIP` and `NodeExternalIP` addresses on Kubernetes nodes using netlink API.
- **node-lifecycle-controller** — a Deployment that monitors NotReady nodes and deletes unreachable ones. Designed to work with cluster-autoscaler by watching nodes with the `ToBeDeletedByClusterAutoscaler` taint.

### Features

**IP Address Controller (local-ccm):**
- **DaemonSet Architecture**: Runs on every node, each managing itself
- **Automatic IP Detection**: Uses netlink API to detect source IP addresses for routing to target
- **Configurable Targets**: Separate configuration for internal and external IP detection
- **Non-Destructive Updates**: Preserves existing addresses (Hostname, InternalIP from kubelet), updates only managed fields
- **Taint Removal**: Automatically removes `node.cloudprovider.kubernetes.io/uninitialized` taint

**Node Lifecycle Controller:**
- **Autoscaler Integration**: Watches nodes tainted with `ToBeDeletedByClusterAutoscaler:NoSchedule` by default
- **Label Selector Fallback**: Optionally filter by label selector instead of taint
- **Ping Verification**: ICMP ping check before deleting to avoid false positives
- **Protected Nodes**: Labels/annotations to protect specific nodes from deletion
- **Leader Election**: HA-ready with lease-based leader election
- **Dry-Run Mode**: Test behavior without actually deleting nodes

## How It Works

1. Kubelet starts with `--cloud-provider=external` flag (optional)
2. If kubelet has `--cloud-provider=external`, it adds taint `node.cloudprovider.kubernetes.io/uninitialized:NoSchedule`
3. `local-ccm` pod starts on the node via DaemonSet
4. Pod detects node's IP addresses using netlink API to query routes to target IPs:
   - Queries route to target (e.g., 8.8.8.8)
   - Extracts source IP from the route
   - Example: Route to 8.8.8.8 via 192.168.1.1 has source IP 192.168.1.100
5. Pod updates only managed addresses (preserves other addresses):
   - Always updates: `ExternalIP`
   - Updates `InternalIP` only if `--internal-ip-target` is set
   - Preserves all other addresses (Hostname, InternalIP from kubelet, etc.)
6. Pod removes the initialization taint (if present)
7. Pod continues to run, reconciling addresses every 10 seconds (configurable)

## Node Lifecycle Controller

The node-lifecycle-controller watches for NotReady nodes and deletes them after they become unreachable. This is useful for cleaning up stale node objects left behind by cluster-autoscaler scale-down operations.

### How It Works

1. Controller lists nodes matching the configured filter (taint or label selector)
2. Control-plane and protected nodes are always excluded
3. When a node becomes NotReady, the controller starts tracking it
4. After `--not-ready-timeout` (default: 5m), the controller pings the node via ICMP
5. If the node is unreachable, it is deleted from the cluster

### Node Filtering

The controller uses priority-based filtering:

| `--node-selector` | `--watch-autoscaler-taint` | Behavior |
|---|---|---|
| set | (ignored) | Only nodes matching label selector |
| empty | `true` (default) | Only nodes with `ToBeDeletedByClusterAutoscaler:NoSchedule` taint |
| empty | `false` | All non-control-plane nodes |

### Configuration Options

| Flag | Description | Default |
|------|-------------|---------|
| `--watch-autoscaler-taint` | Watch only nodes with autoscaler deletion taint | `true` |
| `--node-selector` | Label selector for nodes (overrides taint filter) | `""` |
| `--protected-labels` | Comma-separated labels that protect nodes from deletion | `""` |
| `--not-ready-timeout` | Duration a node must be NotReady before deletion | `5m` |
| `--ping-timeout` | Timeout for ICMP ping checks | `5s` |
| `--ping-count` | Number of ping attempts | `3` |
| `--reconcile-interval` | Interval between reconciliation loops | `30s` |
| `--leader-elect` | Enable leader election | `true` |
| `--dry-run` | Log actions without deleting | `false` |

## Installation

### Prerequisites

- Kubernetes cluster 1.28+
- Linux nodes with netlink support (standard in all modern kernels)

### Deploy local-ccm

1. Apply the manifests:

```bash
kubectl apply -f https://raw.githubusercontent.com/cozystack/local-ccm/main/deploy/rbac.yaml
kubectl apply -f https://raw.githubusercontent.com/cozystack/local-ccm/main/deploy/daemonset.yaml
```

2. Verify deployment:

```bash
kubectl -n kube-system get ds local-ccm
kubectl -n kube-system get pods -l app=local-ccm
```

3. Check node addresses:

```bash
kubectl get nodes -o wide
kubectl get node <node-name> -o jsonpath='{.status.addresses}' | jq
```

### Deploy with Helm

The Helm chart deploys both local-ccm and node-lifecycle-controller:

```bash
helm install local-ccm ./charts/local-ccm --namespace kube-system
```

The node-lifecycle-controller is enabled by default. To configure it:

```yaml
# values.yaml
nodeLifecycleController:
  enabled: true
  controller:
    watchAutoscalerTaint: true   # Watch nodes with autoscaler taint (default)
    # nodeSelector: ""           # Set to use label selector instead
    # protectedLabels: "kilo.squat.ai/leader"
    notReadyTimeout: 5m
    pingCount: 3
    dryRun: false
```

### Talos Linux

For Talos Linux clusters, use the following configuration:

```yaml
machine:
  kubelet:
    extraArgs:
      cloud-provider: external
cluster:
  manifests:
  - url: https://raw.githubusercontent.com/cozystack/local-ccm/main/deploy/rbac.yaml
  - url: https://raw.githubusercontent.com/cozystack/local-ccm/main/deploy/daemonset.yaml
```

This configuration:
- Enables `--cloud-provider=external` flag for kubelet automatically
- Applies the `node.cloudprovider.kubernetes.io/uninitialized` taint on node startup
- Deploys local-ccm manifests during cluster bootstrap
- local-ccm removes the taint after setting node addresses

## Configuration

Configuration is done via command-line arguments in the DaemonSet spec. Edit the DaemonSet to customize:

```bash
kubectl -n kube-system edit daemonset local-ccm
```

### Configuration Options

The following command-line flags are available:

| Flag | Description | Default | Required |
|------|-------------|---------|----------|
| `--node-name` | Name of the node to update (use NODE_NAME env var) | - | Yes |
| `--internal-ip-target` | Target IP for internal IP detection via netlink. If empty, internal IP detection is disabled | `""` (disabled) | No |
| `--external-ip-target` | Target IP for external IP detection via netlink | `"8.8.8.8"` | No |
| `--remove-taint` | Remove node.cloudprovider.kubernetes.io/uninitialized taint | `true` | No |
| `--reconcile-interval` | Interval between reconciliation loops | `10s` | No |
| `--run-once` | Run once and exit instead of running in a loop | `false` | No |
| `--kubeconfig` | Path to kubeconfig file (for local testing only) | In-cluster config | No |
| `--v` | Log level (0-5) | `0` | No |

### Example Configurations

#### Only External IP (Default)

```yaml
args:
- --node-name=$(NODE_NAME)
- --external-ip-target=8.8.8.8
- --reconcile-interval=10s
```

Result:
```json
{
  "addresses": [
    {"type": "Hostname", "address": "node1"},
    {"type": "ExternalIP", "address": "203.0.113.10"}
  ]
}
```

#### Both Internal and External IPs

```yaml
args:
- --node-name=$(NODE_NAME)
- --internal-ip-target=10.0.0.1
- --external-ip-target=8.8.8.8
- --reconcile-interval=10s
```

Result:
```json
{
  "addresses": [
    {"type": "Hostname", "address": "node1"},
    {"type": "InternalIP", "address": "10.0.0.5"},
    {"type": "ExternalIP", "address": "203.0.113.10"}
  ]
}
```

After updating the DaemonSet args, restart the pods:

```bash
kubectl -n kube-system rollout restart ds/local-ccm
```

## Kubelet Configuration (Optional)

While not strictly required, you can configure kubelet with `--cloud-provider=external` to set the uninitialized taint which `local-ccm` will remove:

```bash
kubelet \
  --cloud-provider=external \
  --node-ip=<node-ip>  # Optional: for bootstrap before local-ccm starts
```

Or in kubelet config file:

```yaml
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cloudProvider: external
```

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  Kubernetes Cluster                  │
│                                                       │
│  ┌─────────────────────────────────────────────┐   │
│  │              Node 1                          │   │
│  │                                               │   │
│  │  ┌────────────────┐    ┌──────────────────┐ │   │
│  │  │ local-ccm Pod  │───▶│  Node 1 Object   │ │   │
│  │  │ (DaemonSet)    │    │  via API         │ │   │
│  │  └────────────────┘    └──────────────────┘ │   │
│  │         │                                     │   │
│  │         │ hostNetwork: true                   │   │
│  │         ▼                                     │   │
│  │  ┌──────────────────┐                        │   │
│  │  │ Host Network     │                        │   │
│  │  │ netlink API      │                        │   │
│  │  └──────────────────┘                        │   │
│  └─────────────────────────────────────────────┘   │
│                                                       │
│  ┌─────────────────────────────────────────────┐   │
│  │              Node 2                          │   │
│  │                                               │   │
│  │  ┌────────────────┐    ┌──────────────────┐ │   │
│  │  │ local-ccm Pod  │───▶│  Node 2 Object   │ │   │
│  │  │ (DaemonSet)    │    │  via API         │ │   │
│  │  └────────────────┘    └──────────────────┘ │   │
│  │         │                                     │   │
│  │         │ hostNetwork: true                   │   │
│  │         ▼                                     │   │
│  │  ┌──────────────────┐                        │   │
│  │  │ Host Network     │                        │   │
│  │  │ netlink API      │                        │   │
│  │  └──────────────────┘                        │   │
│  └─────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────┘
```

### Components

- **DaemonSet Controller**: Ensures one pod runs on each node
- **IP Detector**: Uses netlink API to query routes and extract source IPs
- **Node Updater**: Updates node addresses via Kubernetes API

## Building

### Build Binaries

```bash
CGO_ENABLED=0 go build -o local-ccm ./cmd/local-ccm
CGO_ENABLED=0 go build -o node-lifecycle-controller ./cmd/node-lifecycle-controller
```

### Build Container Images

```bash
docker build --target local-ccm -t ghcr.io/cozystack/local-ccm:latest .
docker build --target node-lifecycle-controller -t ghcr.io/cozystack/node-lifecycle-controller:latest .
```

## Development

### Project Structure

```
local-ccm/
├── cmd/
│   ├── local-ccm/
│   │   └── main.go                # IP controller entrypoint
│   └── node-lifecycle-controller/
│       └── main.go                # NLC entrypoint
├── pkg/
│   ├── detector/
│   │   └── ip_detector.go         # IP detection via netlink
│   ├── node/
│   │   └── updater.go             # Node address/taint updater
│   ├── controller/
│   │   └── controller.go          # Node lifecycle controller logic
│   └── checker/
│       └── checker.go             # ICMP reachability checker
├── charts/
│   └── local-ccm/                 # Helm chart
├── deploy/
│   ├── rbac.yaml                  # Static manifests (local-ccm only)
│   └── daemonset.yaml
├── Dockerfile
├── go.mod
└── README.md
```

### Run Locally

```bash
go run ./cmd/local-ccm \
  --node-name=$(hostname) \
  --external-ip-target=8.8.8.8 \
  --internal-ip-target=10.0.0.1 \
  --kubeconfig=$HOME/.kube/config \
  --run-once \
  --v=4
```

## Troubleshooting

### Pods not starting

Check DaemonSet status:

```bash
kubectl -n kube-system describe ds local-ccm
kubectl -n kube-system get pods -l app=local-ccm
```

### IP detection fails

Check pod logs:

```bash
kubectl -n kube-system logs -l app=local-ccm --tail=100
```

Common causes:
- No route to target IP (check routing table)
- Network namespace issues (ensure hostNetwork: true)
- Missing CAP_NET_ADMIN capability

### Addresses not updating

1. Check RBAC permissions:
   ```bash
   kubectl auth can-i patch nodes --as=system:serviceaccount:kube-system:local-ccm
   ```

2. Verify arguments are correct:
   ```bash
   kubectl -n kube-system get ds local-ccm -o yaml | grep -A10 args
   ```

3. Enable debug logging:
   Edit DaemonSet and change `--v=2` to `--v=5`

### Enable debug logging

Edit the DaemonSet:

```bash
kubectl -n kube-system edit ds local-ccm
```

Change the args:
```yaml
args:
- --v=5  # Debug level logging
```

## Performance

- **CPU**: ~10m per node (requests), ~100m (limits)
- **Memory**: ~32Mi per node (requests), ~64Mi (limits)
- **Network**: Minimal (only API calls to update node object)

## Comparison with CCM Approach

| Feature | DaemonSet (local-ccm) | Cloud Controller Manager |
|---------|----------------------|-------------------------|
| **Architecture** | Distributed (one pod per node) | Centralized (control-plane) |
| **Complexity** | Simple, direct | Complex, requires CCM framework |
| **Dependencies** | Only client-go | Full cloud-provider stack |
| **Leader Election** | ❌ Not needed | ✅ Required |
| **Scaling** | Linear with nodes | Single control-plane component |
| **Network** | Direct from each node | Centralized API calls |
| **Best For** | Simple bare-metal setups | Cloud environments, complex logic |

## License

Apache License 2.0

## References

- [Kubernetes Node Objects](https://kubernetes.io/docs/concepts/architecture/nodes/)
- [Kubernetes Cloud Controller Manager](https://kubernetes.io/docs/concepts/architecture/cloud-controller/)
