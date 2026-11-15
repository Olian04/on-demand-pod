# on-demand-pod

A Kubernetes operator that runs temporary pods on a lease. Create an
`OnDemandPod`; the operator runs the Pod and deletes everything when the lease
expires.

Design: [RFC 0001](docs/rfcs/0001-on-demand-pod.md). Contributor and agent
guide: [AGENTS.md](AGENTS.md).

Status: scaffold only. Nothing is implemented yet.

## Toolchain

| Tool | Version |
| --- | --- |
| kubebuilder | v4.16.0 (layout `go.kubebuilder.io/v4`) |
| controller-gen | v0.22.0 |
| Go | 1.26.0 (`go.mod`) |

## License

MIT, see [LICENSE](LICENSE).
