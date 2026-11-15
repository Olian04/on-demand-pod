# on-demand-pod: design brief and implementation plan

Audience: Claude Code, working autonomously in a cloud session on `github.com/Olian04/on-demand-pod`.
The owner designs, you implement. Where this brief is silent or contradicts reality, stop and report instead of guessing.

## 1. Goal

A Kubernetes operator that manages temporary pods on a lease.

- A user creates an `OnDemandPod` custom resource.
- The operator runs a Pod from it and deletes everything once the lease expires.
- Users already have cluster access. The problem being solved is the friction of IaC and Argo reconciliation for throwaway work, plus orphaned pods.
- A `kubectl ondemand` plugin is a thin convenience client. Applying YAML by hand must behave identically.

## 2. The one idea

**A pod with a lease.** Everything else derives from three rules:

1. The lease starts at CR creation and ends at `expiresAt`.
2. Nothing is deleted before `expiresAt` unless the user deletes the CR.
3. Spec is intent. Status is reality.

If a proposed change does not follow from these rules, it does not belong in v1.

## 3. Non-goals (do not build)

- TUI, SSH server, or any second auth path. Auth is Kubernetes RBAC only.
- An identity CRD, admission webhooks, idle detection, SFTP, port-forward.
- Multi-cluster support. One operator manages one cluster.
- Image building. Users push images to a registry themselves.
- Sandboxed runtimes. Images are trusted, but can contain bugs.
- Cost features of any kind.

Prefer subtraction. If something can be removed, remove it rather than deprecate it.

## 4. Naming

- Repo: `on-demand-pod`
- Go module: `github.com/Olian04/on-demand-pod`
- API group: `ondemand.olian04.github.io`, version `v1alpha1`
- Kind `OnDemandPod`, resource `ondemandpods`, short name `odp`
- kubebuilder domain: `olian04.github.io`
- Plugin binary: `kubectl-ondemand`
- Operator binary: `cmd/operator`

## 5. API

### Spec

Every field is immutable except `ttl`. Enforce immutability with CEL transition rules (`self == oldSelf`) on each field.

```go
// OnDemandPodSpec describes the pod to run and how long it may live.
type OnDemandPodSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="image is immutable"
	Image string `json:"image"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="command is immutable"
	Command []string `json:"command,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="args is immutable"
	Args []string `json:"args,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="env is immutable"
	Env []corev1.EnvVar `json:"env,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="resources is immutable"
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// TTL is the total lease length measured from creation. It is the only
	// mutable field. Extending a lease means increasing this value. When
	// empty, the operator's default TTL applies.
	TTL *metav1.Duration `json:"ttl,omitempty"`
}
```

Notes:

- No `serviceAccountName`. Pods use the namespace default ServiceAccount, which carries image pull secrets.
- Pod name equals CR name. There is no `status.podName`.
- If a Pod with that name exists and is not owned by this CR, set `Ready=False` with reason `NameConflict` and do nothing else.

### Status

```go
type OnDemandPodStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	Phase string `json:"phase,omitempty"`

	// ExpiresAt is creationTimestamp + min(spec.ttl, operator max TTL).
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// Conditions: Ready mirrors Pod Ready. TTLClamped is true when
	// spec.ttl exceeds the operator max TTL.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

Printer columns: Image, Phase, Ready, Expires.

### Phases

There is no `Expired` or `Rejected` phase. Phases mirror the Pod.

```
Pending --(pod Running)--> Running --(exit 0)--> Succeeded
   |                          |
   |                          +--(exit != 0, deadline, evicted)--> Failed
   +--(unschedulable or pull error: stays Pending, reason in a condition)

Any phase: now >= expiresAt  -> CR deleted, Pod garbage-collected
Any phase: user deletes CR   -> same
```

- Succeeded and Failed Pods stay until `expiresAt` so logs remain readable.
- A Pod stuck in Pending is bounded by the lease. No separate pending timeout.

## 6. Reconcile loop

Idempotent. Every step is derived from the CR plus the Pod. No operator-local state.

1. Compute `expiresAt = creationTimestamp + min(spec.ttl or defaultTTL, maxTTL)`. Write it to status. Set `TTLClamped` when clamped. Clamp, never reject, because there is no webhook.
2. If `now >= expiresAt`, delete the CR with background propagation and return.
3. If the Pod is missing, create it (section 7).
4. Mirror Pod phase and Ready into status.
5. Requeue after `time.Until(expiresAt)`.

Requirements:

- Inject a clock interface so expiry is testable without sleeping.
- Ownership via `controllerutil.SetControllerReference`. No finalizer, because the Pod is the only child and owner-reference GC covers it.
- Operator flags: `--default-ttl` (default 2h), `--max-ttl` (default 8h).

## 7. Pod construction

- `restartPolicy: Never`, so a crashed Pod stays visible as Failed.
- `activeDeadlineSeconds` set once at creation to `maxTTL` in seconds. This is a kubelet-enforced hard ceiling that survives operator downtime.
- Pod Security `restricted` settings: `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, capabilities drop `ALL`, `seccompProfile: RuntimeDefault`.
- `automountServiceAccountToken: false`.
- Labels `app.kubernetes.io/managed-by: on-demand-pod` and a label pointing at the CR name.

## 8. Facts to verify before relying on them

Report findings to the owner. Do not silently work around a failure.

1. **`activeDeadlineSeconds` is believed to be lowerable but not raisable on a live Pod.** That is why the backstop uses `maxTTL` and the operator enforces the precise TTL. Verify on the target Kubernetes version in envtest or kind. If it can be raised, report it, because the design could then simplify.
2. **`runAsNonRoot` will break images that run as root.** Confirm the failure surfaces as a readable condition (for example `CreateContainerConfigError`). Do not loosen the security context to make it pass. Report instead.
3. **Object-count quota for the CR.** Verify `count/ondemandpods.ondemand.olian04.github.io` works in a `ResourceQuota`, and document it in the README.
4. **kubebuilder version and generated layout.** Record the version used.

## 9. kubectl plugin

A thin client with no business logic. Uses `genericclioptions` so `--namespace` and `--context` behave like kubectl.

- `kubectl ondemand run <image> [--ttl 2h] [--name N] [-it] [-- cmd args...]`: create the CR, wait for Ready, optionally exec a shell. Default name is generated.
- `kubectl ondemand ls`: list CRs with time remaining.
- `kubectl ondemand extend <name> <duration>`: patch `spec.ttl`. Print a warning if the result reports `TTLClamped`.
- `kubectl ondemand rm <name>`: delete the CR.

Each command must map to a plain `kubectl` operation on the CR.

## 10. Repository layout

```
on-demand-pod/
  api/v1alpha1/            OnDemandPod types and CEL rules
  cmd/operator/
  cmd/kubectl-ondemand/    added by hand after scaffolding
  internal/controller/
  config/                  CRD, RBAC, manager manifests (kustomize)
  docs/rfcs/               this brief lands here as 0001
  AGENTS.md
  README.md                links the RFC
```

## 11. Engineering conventions

Follow these. They are the owner's standing rules.

### Design

- Single Responsibility Principle. Strict dependency direction.
- Go: accept interfaces, return structs. Functional options where they fit.
- No `init()` for registration or wiring. Optional features live in their own packages.
- Prefer separate types over mode flags. Validate declarations at startup.
- Metrics are enabled by default. Expose at least an active-pods gauge by phase and an expired-pods counter.
- Comments are full sentences that explain why and name the cost.
- Pin dependencies. Prefer local copies where practical.

### Testing

- Integration tests per package, exercising the package API from an external `_test` package. Prefer these over unit tests of internals.
- Use envtest for controller tests. Use the fake clock for expiry.
- Every bug fix gets a regression test that fails without the fix.
- Tests run with the race detector and shuffled order.

### Verification (run all before opening a PR)

```bash
go vet ./...
go mod verify
govulncheck ./...
golangci-lint run
go test -race -shuffle=on ./...
```

### Git workflow

- One piece of work per branch, worktree and PR. PRs are squash-merged.
- Conventional Commits: `feat:`, `fix:`, `refactor:`, `chore:`, `feat!:`.
- Resolve conflicts and CI failures by merging the base branch in. Never rebase and never force-push.
- Understand what is wrong and fix it. No shortcuts, no disabling checks to get green.

## 12. Milestones

Each milestone is one branch and one PR. Stop after each PR and wait for review.

1. `chore: scaffold project`: kubebuilder init and create api per section 4. Review the diff against the existing `.gitignore`, `LICENSE` and `README.md`. Add `AGENTS.md` and commit this brief as `docs/rfcs/0001-on-demand-pod.md`, linked from the README. Record the kubebuilder and Go versions.
2. `feat: define OnDemandPod API`: types, CEL immutability, printer columns. envtest tests proving each field is immutable and `ttl` is mutable.
3. `feat: run pod for each OnDemandPod`: Pod creation, owner reference, hardening, status mirroring, `NameConflict`. Resolve fact 2 from section 8.
4. `feat: enforce lease expiry`: `expiresAt`, clamping, `TTLClamped`, deletion at expiry, requeue, operator flags, the `activeDeadlineSeconds` backstop. Resolve fact 1 from section 8. Fake-clock tests.
5. `feat: operator manifests and metrics`: RBAC, kustomize config, metrics. Resolve fact 3.
6. `feat: add kubectl-ondemand plugin`: the four commands from section 9, with integration tests.
7. `chore: add CI`: GitHub Actions running the verification suite, plus an end-to-end run on kind that creates an `OnDemandPod`, observes expiry, and confirms the Pod is gone.

## 13. Working rules for this session

- Work milestone by milestone. Do not start the next until the owner approves the current PR.
- If you find a design problem, report it with evidence and a proposed fix. Do not redesign silently.
- If a step needs a decision that is not in this brief, stop and ask. Do not pick a default and continue.
- Do not add features outside section 3's boundaries, even if they seem helpful.
