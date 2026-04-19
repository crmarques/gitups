# nginx-metallb-binding e2e case

Acceptance test for the `lb-ip-pool` capability. `nginx-ingress` (consumer)
binds `metallb` (provider) through one binding named `nginx-ingress-lb`.
Because both packages live in the `basic-infra` generic repo, the provider
and consumer sides of the synthesized units both land in
`basic-infra-dsv/`.

## What it proves

- Expand materializes:
  - provider-side `metallb/resources/pool/nginx-ingress-lb-basic-infra-dsv/`
    with an `IPAddressPool` named `ingress-pool` and a scoped
    `L2Advertisement`;
  - consumer-side
    `nginx-ingress/resources/lb-binding/nginx-ingress-lb-basic-infra-dsv/`
    with a documentation `ConfigMap` recording the pool name + CIDR list.
- Apply-wave annotations are emitted on both sides and the provider wave is
  strictly less than the consumer wave.
- All binding values are literal; expand produces zero placeholders.
- No `__GITUPS_PLACEHOLDER__` sentinel leaks into the rendered tree.

## Env vars

| Var                       | Default                                 |
| ------------------------- | --------------------------------------- |
| `KUBE_CONTEXT`            | `kind-nginx-metallb-binding`            |
| `GITUPS_OUTPUT_DIR`       | temp dir under `${TMPDIR:-/tmp}`; removed after success |
| `GITUPS_E2E_APPLY`        | `false` (set `true` to also `apply`/`wait`) |
| `GITUPS_E2E_WAIT_TIMEOUT` | `10m`                                   |

Run directly with `tests/e2e/nginx-metallb-binding/run.sh` or via the
dispatcher with `tests/e2e/run.sh --case nginx-metallb-binding`.
