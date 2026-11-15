# AGENTS.md

Guide for AI agents working in this repository. The design lives in
[docs/rfcs/0001-on-demand-pod.md](docs/rfcs/0001-on-demand-pod.md); read it first.
Where the RFC is silent or contradicts reality, stop and report instead of guessing.

## The one idea

A pod with a lease. Everything derives from three rules:

1. The lease starts at CR creation and ends at `expiresAt`.
2. Nothing is deleted before `expiresAt` unless the user deletes the CR.
3. Spec is intent. Status is reality.

A change that does not follow from these rules does not belong in v1. Prefer
subtraction: remove rather than deprecate. RFC section 3 lists non-goals.

## Layout

```
api/v1alpha1/            OnDemandPod types and CEL rules
cmd/operator/            operator entry point
cmd/kubectl-ondemand/    kubectl plugin (added by hand)
internal/controller/     reconciler
config/                  CRD, RBAC, manager manifests (kustomize)
docs/rfcs/               design documents
```

Generated, never edit by hand: `config/crd/bases/*`, `config/rbac/role.yaml`,
`**/zz_generated.*.go`, `PROJECT`. Regenerate with `make manifests generate`.
Keep the `// +kubebuilder:scaffold:*` markers. The entry point was moved from the
kubebuilder default `cmd/main.go` to `cmd/operator/main.go` on purpose (RFC section 10).

## Conventions

Design:
- Single Responsibility Principle, strict dependency direction.
- Accept interfaces, return structs. Functional options where they fit.
- No `init()` for registration or wiring. Optional features live in their own packages.
- Prefer separate types over mode flags. Validate declarations at startup.
- Metrics on by default: at least an active-pods gauge by phase and an expired-pods counter.
- Comments are full sentences that explain why and name the cost.
- Pin dependencies. Prefer local copies where practical.

Testing:
- Integration tests per package, from an external `_test` package, against the package API.
- envtest for controller tests; an injected fake clock for expiry, never sleeping.
- Every bug fix gets a regression test that fails without the fix.
- Run with the race detector and shuffled order.

Git:
- One piece of work per branch and PR; PRs are squash-merged.
- Conventional Commits: `feat:`, `fix:`, `refactor:`, `chore:`, `feat!:`.
- Resolve conflicts and CI failures by merging the base branch in. Never rebase, never force-push.
- Fix root causes. Never disable checks or tests to get green.

## Verification (run all before opening a PR)

```bash
go vet ./...
go mod verify
govulncheck ./...
golangci-lint run
go test -race -shuffle=on ./...
```

## Working rules

- Work milestone by milestone (RFC section 12). Stop after each PR and wait for review.
- Report design problems with evidence and a proposed fix. Do not redesign silently.
- If a step needs a decision the RFC does not make, ask. Do not pick a default.
- Do not build anything in RFC section 3, even if it seems helpful.

## Toolchain

Versions used for the scaffold are recorded in the README.
