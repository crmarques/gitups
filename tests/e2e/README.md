# tests/e2e

End-to-end test cases for the `init -> expand -> fill -> check -> generate ->
(apply + wait)` flow. Each case lives in its own subdirectory with a
self-contained `run.sh` and fixtures; the dispatcher here picks which one
to run.

## Running

```
# default case (dsv), current behavior pre-split
tests/e2e/run.sh

# explicit case selection
tests/e2e/run.sh --case basic-binding

# enumerate cases
tests/e2e/run.sh --list

# per-case scripts are also directly runnable
tests/e2e/basic-binding/run.sh
```

Arguments after a `--` are forwarded to the selected per-case `run.sh`.

Every case expects `make build` to have been run so `bin/gitups` exists.

## Cases

| Name            | Scenario                                                          |
| --------------- | ------------------------------------------------------------------|
| `dsv`           | Full-stack Provision (olm, metallb, nginx-ingress, vault, gitea, keycloak, service-mesh, argocd, declarest). No bindings. |
| `basic-binding` | `argocd` consumes `git-provider` from `gitea` via one binding. Exercises provider fan-out, shared placeholder propagation, and apply-wave ordering. |

## Shared env vars

Cluster-gated steps (`apply`, `wait`) run only when
`GITUPS_E2E_APPLY=true` (the default). Set `GITUPS_E2E_APPLY=false` to
stop after the render + filesystem assertions — useful when no cluster is
available.

| Var                       | Default                                                  | Used by         |
| ------------------------- | -------------------------------------------------------- | --------------- |
| `GITUPS_OUTPUT_DIR`       | `gitups-output-dir`                                      | all             |
| `GITUPS_E2E_APPLY`        | `true`                                                   | all             |
| `GITUPS_E2E_WAIT_TIMEOUT` | `10m`                                                    | all             |
| `KUBE_CONTEXT`            | `kind-<case>`                                            | all             |
| `METALLB_ADDRESS_POOL`    | `172.18.255.200-172.18.255.250`                          | `dsv`           |
| `GITOPS_REPO_URL`         | `https://example.invalid/gitops/gitops-controllers-<env>.git` | `dsv`, `basic-binding` |
| `VAULT_DEV_ROOT_TOKEN`    | `gitups-e2e-root`                                        | `dsv`           |
| `GITEA_ARGOCD_BOT_TOKEN`  | `e2e-argocd-bot-token`                                   | `basic-binding` |

See each case's README for the exact variables it honors.
