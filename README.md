# local-ccm

A lightweight Kubernetes node IP address controller that runs as a DaemonSet and automatically detects and sets node IP addresses using netlink on bare-metal and on-premise clusters.

## Overview

`local-ccm` solves the problem of automatically setting `NodeInternalIP` and `NodeExternalIP` addresses on Kubernetes nodes in environments without a cloud provider. Each node runs its own instance that detects local IP addresses and updates the node object accordingly.

### Features

- **DaemonSet Architecture**: Runs on every node, each managing itself
- **Automatic IP Detection**: Uses netlink API to detect source IP addresses for routing to target
- **Configurable Targets**: Separate configuration for internal and external IP detection
- **Non-Destructive Updates**: Preserves existing addresses (Hostname, InternalIP from kubelet), updates only managed fields
- **Taint Removal**: Automatically removes `node.cloudprovider.kubernetes.io/uninitialized` taint
- **Minimal Dependencies**: No external tools required, uses native netlink
- **Lightweight**: Small memory footprint (~32MB per node)
- **Continuous Reconciliation**: Periodically checks and updates IP addresses

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

## Command-Line Flags

The `local-ccm` binary supports the following flags:

| Flag | Description | Default |
|------|-------------|---------|
| `--node-name` | Name of the node to update (env: NODE_NAME) | Required |
| `--internal-ip-target` | Target IP for internal IP detection. If empty, disabled | `""` |
| `--external-ip-target` | Target IP for external IP detection | `"8.8.8.8"` |
| `--remove-taint` | Remove node.cloudprovider.kubernetes.io/uninitialized taint | `true` |
| `--run-once` | Run once and exit instead of running in a loop | `false` |
| `--reconcile-interval` | Interval between reconciliation loops | `10s` |
| `--kubeconfig` | Path to kubeconfig file (for local testing) | In-cluster config |
| `--v` | Log level (0-5) | `0` |

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

### Build Binary

```bash
cd local-ccm
go mod tidy
GOWORK=off CGO_ENABLED=0 go build -o local-ccm ./cmd/local-ccm
```

### Build Container Image

```bash
docker build -t ghcr.io/cozystack/local-ccm:latest .
```

## Development

### Project Structure

```
local-ccm/
├── cmd/
│   └── local-ccm/
│       └── main.go           # Main entrypoint
├── pkg/
│   ├── node/
│   │   └── updater.go        # Node address/taint updater
│   └── detector/
│       └── ip_detector.go    # IP detection logic
├── deploy/
│   ├── rbac.yaml            # ServiceAccount + ClusterRole
│   └── daemonset.yaml       # DaemonSet
├── Containerfile
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
