# on-demand-pod

A Kubernetes operator that runs temporary pods on a lease. Create an
`OnDemandPod`; the operator runs the Pod and deletes everything when the lease
expires.

Design: [RFC 0001](docs/rfcs/0001-on-demand-pod.md). Contributor and agent
guide: [AGENTS.md](AGENTS.md).

## How it works

```yaml
apiVersion: ondemand.olian04.github.io/v1alpha1
kind: OnDemandPod
metadata:
  name: scratch
spec:
  image: busybox:1.37
  command: ["sleep", "infinity"]
  ttl: 30m
```

The operator runs a Pod named `scratch` and deletes the CR, and with it the Pod,
at `creationTimestamp + ttl`. Every spec field except `ttl` is immutable.
Extend a lease by raising `ttl`. The lease is capped at the operator's
`--max-ttl`; a longer request is clamped and the `TTLClamped` condition is set.

```bash
kubectl get odp        # image, phase, ready, expiry
```

## kubectl plugin

`make build` produces `bin/kubectl-ondemand`; put it on your `PATH` and kubectl
finds it as `kubectl ondemand`. It is a thin client: every command is one plain
operation on the CR, and applying YAML by hand behaves identically.

| Command | Equivalent |
| --- | --- |
| `kubectl ondemand run <image> [--ttl 2h] [--name N] [-it] [-- cmd args...]` | create the CR, wait for Ready, optionally exec `sh` |
| `kubectl ondemand ls` | `kubectl get odp`, plus time remaining |
| `kubectl ondemand extend <name> <duration>` | patch `spec.ttl` to the new total lease length |
| `kubectl ondemand rm <name>` | `kubectl delete odp <name>` |

`--namespace` and `--context` behave as in kubectl.

## Deploy

```bash
make deploy IMG=<your-registry>/on-demand-pod:<tag>
```

Operator flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--default-ttl` | `2h` | Lease for a CR that sets no `ttl`. |
| `--max-ttl` | `8h` | Longest lease. Also the kubelet-enforced Pod `activeDeadlineSeconds`. |
| `--metrics-bind-address` | `:8443` | Metrics endpoint (HTTPS, authenticated). `0` disables it. |

Metrics: `ondemandpod_active_pods{phase}` (gauge) and
`ondemandpod_expired_pods_total` (counter).

Pods run with the Pod Security `restricted` profile and no service account
token. Images that run as root will not start.

## Limiting usage per namespace

Object-count quotas work for the custom resource, so a namespace can cap how
many leases it holds:

```yaml
apiVersion: v1
kind: ResourceQuota
metadata:
  name: ondemandpods
spec:
  hard:
    count/ondemandpods.ondemand.olian04.github.io: "5"
```

Creating a sixth `OnDemandPod` is then rejected with `exceeded quota`. This is
covered by an envtest test.

## Toolchain

| Tool | Version |
| --- | --- |
| kubebuilder | v4.16.0 (layout `go.kubebuilder.io/v4`) |
| controller-gen | v0.22.0 |
| Go | 1.26.11 (`go.mod`; raised from 1.26.0 to clear stdlib advisories) |

## License

MIT, see [LICENSE](LICENSE).
