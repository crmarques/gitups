# Controllers — KRC and SRC CLI contracts

Read before changing `cmd/gitups/main.go` apply/wait paths,
`internal/cluster/kubeclient.go`, `internal/resolve/controllers.go`,
or the `spec.cli` block on any controller package in
gitups-packages.

## Why this exists

Gitups core carries **no** hardcoded cluster binary. When `gitups
apply` or `gitups wait` needs to touch the cluster, it looks up the
selected KRC (`Provision.spec.controllers.kubernetesResources`),
reads that KRC package's `spec.cli.binary` + `spec.cli.intents` from
its `package.yaml`, renders the relevant intent's args template, and
execs the binary. The KRC decides what gitups calls.

This makes the apply path **pluggable**: today argocd ships a
kubectl-backed intent set; tomorrow a flux-based KRC could declare a
`flux`-binary intent set of the same shape, and gitups core wouldn't
change.

## KRC intent set — what gitups apply/wait uses

Every cluster operation gitups core performs is named by a constant
in `api/v1alpha1/types.go`:

| Constant (`v1.Intent*`)        | String value            | Used by |
|-------------------------------|-------------------------|---------|
| `IntentApply`                 | `apply`                 | `gitups apply` for every non-SRC unit and for the `kubectl apply` step of SRC bootstrap-sync intents. |
| `IntentApplyDryRun`           | `apply-dry-run`         | `gitups apply --dry-run`. |
| `IntentGetJSON`               | `get-json`              | OLM subscription/CSV polling in `gitups wait` and `--wait-crds`. |
| `IntentWaitCondition`         | `wait-condition`        | Readiness-gated wave progression between apply waves. |
| `IntentWaitCRDsEstablished`   | `wait-crds-established` | CRD-establishment retry pass in `applyUnitDir`. |
| `IntentListCRDs`              | `list-crds`             | Fresh-cluster probe — skips the `--all` CRD wait when no CRDs exist yet. |
| `IntentServerVersion`         | `server-version`        | Compatibility probe (`spec.compatibility.kubernetes` vs live cluster). |
| `IntentListPodsJSONPath`      | `list-pods-jsonpath`    | CSV failure diagnostics — enumerate pods labelled `olm.owner=<csv>`. |
| `IntentPodLogs`               | `pod-logs`              | CSV failure diagnostics — tail last 40 lines. |

A KRC that wants to own the apply phase **must declare every intent
gitups uses**. Missing intents produce a clear error at apply start
naming the intent, so users fix the KRC package, not gitups core.

## Args template context

KRC intent args are Go `text/template` strings rendered with
`missingkey=error` against `cluster.CLIContext`:

```go
type CLIContext struct {
    KubeContext  string  // filled from --to flag
    ManifestPath string  // unit directory (IntentApply*)
    Namespace    string  // IntentGetJSON, WaitCondition, …
    Kind         string  // IntentGetJSON, WaitCondition
    Name         string  // IntentGetJSON, WaitCondition
    Selector     string  // IntentListPodsJSONPath
    Condition    string  // IntentWaitCondition
    Timeout      string  // "60s", "10m", … (IntentWait*)
    Pod          string  // IntentPodLogs
}
```

Only the fields a specific intent needs are populated; unused
fields render as the empty string. Any unknown `{{.X}}` in a
template is a loud load-time error.

## argocd's kubectl-backed intent set

Reference implementation: see
[gitups-packages/packages/argocd/package.yaml](../../../gitups-packages/packages/argocd/package.yaml).
Every entry is a kubectl argv template, e.g.

```yaml
apply:
  args:
    - "--context={{.KubeContext}}"
    - "apply"
    - "-k"
    - "{{.ManifestPath}}"
    - "--server-side"
    - "--force-conflicts"
wait-condition:
  args:
    - "--context={{.KubeContext}}"
    - "-n"
    - "{{.Namespace}}"
    - "wait"
    - "--for=condition={{.Condition}}"
    - "{{.Kind}}/{{.Name}}"
    - "--timeout={{.Timeout}}"
```

A parallel flux/argocd2 package would swap the binary + args for its
native CLI while keeping the intent names.

## SRC CLI — two invocation modes

SRC packages (declarest) use the same `spec.cli` block but for
different purposes:

1. **CLI-only intent** (`managed-script`): `spec.cli.args` is the
   full command the SRC CLI runs; gitups skips kubectl-apply entirely.
2. **Bootstrap-sync intent** (`sync-policy` today, others can be
   added): `spec.cli.intents[<resourceTemplate>].args` is a per-
   intent template. Gitups first applies the rendered CR via the
   KRC CLI (so the in-cluster operator can assume control at steady
   state), then runs the SRC binary with the intent's args for one
   immediate reconciliation pass. Without this, bootstrap reconciles
   block on the SRC operator's readiness probe.

Resolve tags every matching resource unit with
`Controller: {Kind: SRC, Intent: <resourceTemplate>}` — see
`internal/resolve/controllers.go::rewireSRCIntents`. Apply then
routes those units through either `invokeSRCCli` (mode 1) or
`kubectl-apply + invokeSRCCliWithArgs` (mode 2).

## Readiness-gated wave progression

Between apply waves, `applyBootstrapOnly` walks every just-applied
unit's `readiness` checks (package-level `spec.readiness[]` for
installs; descriptor-level `spec.readiness[]` for resources) and
blocks on `IntentWaitCondition` until each is satisfied. So
"configure gitea repo" (a declarest-owned unit) can't fire before
the gitea Deployment is Available.

Gating is best-effort: if the KRC doesn't declare
`IntentWaitCondition` the apply still works in a degraded mode —
the user gets a structured error at apply start so they can fix the
KRC package. No silent no-op.

## Anti-goals

- Gitups never shells to `kubectl` by name. Any new cluster op must
  be an intent the KRC declares.
- KRC intent name is a gitups-core contract; packages MUST use the
  exact names. No renames for readability.
- Template context stays minimal — adding a new field requires
  updating every KRC package in the catalog and this reference.
