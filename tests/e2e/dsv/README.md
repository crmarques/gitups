# dsv e2e case

Exercises the full gitups flow against a kitchen-sink Provision:
olm, metallb, nginx-ingress, vault, gitea, keycloak, service-mesh, argocd,
declarest. No capability bindings — this case proves the baseline
two-stage flow on every renderer in the catalog.

## Flow

1. Seed `${output_dir}/dsv/provision.yaml` from `provision.yaml`.
2. `gitups check` then `gitups expand --force`.
3. `fill_placeholders.go` fills metallb CIDR, vault dev token, argocd
   `repoURL`, declarest `repoURL`, and service-mesh mesh id.
4. `gitups check`, `gitups generate --prune`, `gitups status`.
5. Assert every expected repo directory was emitted.
6. Optional: `gitups apply` + `gitups wait` against the selected context
   (guarded by `GITUPS_E2E_APPLY`).

## Env vars

| Var                       | Default                                                  |
| ------------------------- | -------------------------------------------------------- |
| `KUBE_CONTEXT`            | `kind-dsv`                                               |
| `GITUPS_OUTPUT_DIR`       | temp dir under `${TMPDIR:-/tmp}`; removed after success  |
| `METALLB_ADDRESS_POOL`    | `172.18.255.200-172.18.255.250`                          |
| `GITOPS_REPO_URL`         | `https://example.invalid/gitops/gitops-controllers-dsv.git` |
| `VAULT_DEV_ROOT_TOKEN`    | `gitups-e2e-root`                                        |
| `GITUPS_E2E_APPLY`        | `true`                                                   |
| `GITUPS_E2E_WAIT_TIMEOUT` | `10m`                                                    |

Run directly with `tests/e2e/dsv/run.sh` or via the dispatcher with
`tests/e2e/run.sh --case dsv`.
