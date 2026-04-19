# basic-binding e2e case

Acceptance test for the capability-bindings feature. The Provision declares
one `argocd-gitea-bot` binding where `argocd` (consumer) consumes the
`git-provider` capability from `gitea` (provider). The case exercises the
full flow — `check`, `expand --force`, placeholder fill, re-expand (for
cross-side fill propagation), `check`, `generate --prune`, and a series of
filesystem assertions on the rendered tree. Cluster-side `apply` / `wait`
are gated behind `GITUPS_E2E_APPLY=true` (default `false`, so the case is
runnable without a cluster).

## What it proves

- Expand materializes a provider-side `gitea/resources/user` ResolvedPackage
  per env repo that references `support-services`, and a consumer-side
  `argocd/resources/repo-secret` ResolvedPackage in the env repo where the
  binding is declared.
- Filling only the provider's secret export propagates to the consumer
  through `overlayPlaceholderFills` / `nonSentinelFill` on re-expand, with
  no user intervention on the consumer side.
- Render emits a deterministic `Job` trio for the script-style provider
  (`configmap-scripts.yaml`, `configmap-values.yaml`, `job-provision.yaml`)
  plus a `kustomization.yaml` carrying the `gitups.io/apply-wave` and
  `gitups.io/readiness` annotations.
- Consumer `Secret` is labeled `argocd.argoproj.io/secret-type: repository`
  and `stringData.password` carries the filled token verbatim.
- Provider wave < consumer wave (the consumer dependsOn the provider).
- No `__GITUPS_PLACEHOLDER__` sentinel survives into the rendered tree.

## Env vars

| Var                       | Default                                                         |
| ------------------------- | ----------------------------------------------------------------|
| `KUBE_CONTEXT`            | `kind-basic-binding`                                            |
| `GITUPS_OUTPUT_DIR`       | temp dir under `${TMPDIR:-/tmp}`; removed after success         |
| `METALLB_ADDRESS_POOL`    | `172.18.255.200-172.18.255.250`                                 |
| `GITOPS_REPO_URL`         | `https://example.invalid/gitops/gitops-controllers-dev.git`     |
| `GITEA_ARGOCD_BOT_TOKEN`  | `e2e-argocd-bot-token`                                          |
| `GITUPS_E2E_APPLY`        | `false` (set `true` to also run `apply` + `wait`)               |
| `GITUPS_E2E_WAIT_TIMEOUT` | `10m`                                                           |

Run directly with `tests/e2e/basic-binding/run.sh` or via the dispatcher
with `tests/e2e/run.sh --case basic-binding`.
