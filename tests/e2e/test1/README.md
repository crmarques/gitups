# test1 — argocd + nginx-ingress via Helm on kind

Smallest possible end-to-end case. A single `kind` cluster runs
`nginx-ingress` as the ingress controller and `argocd` as the KRC, both
installed from their upstream Helm charts. No MetalLB, no OLM, no
bindings — this is the baseline.

## Topology

| Repo                      | Package         | Install    | Notes                                  |
| ------------------------- | --------------- | ---------- | -------------------------------------- |
| `basic-infra`             | `nginx-ingress` | helm       | `controller.service.type=ClusterIP`    |
| `basic-infra-dev`         | (env overlay)   | —          | derived from `basic-infra`             |
| `gitops-controllers`      | `argocd`        | helm       | KRC for the env                        |
| `gitops-controllers-dev`  | (env overlay)   | —          | derived from `gitops-controllers`      |

## Prerequisites

- `kind`, `kubectl`, `helm`, `kustomize`, `go`
- `make build` has been run in the gitups repo so `./bin/gitups` exists
- Sibling `gitops-workspace/gitups-packages` checkout (the provision's
  `sources[0].path` resolves relative to this `provision.yaml`)

## 1. Bring up kind

```bash
kind create cluster --name test1
kubectl --context kind-test1 cluster-info
```

## 2. Seed the workspace

From the gitups repo root:

```bash
OUT=$(mktemp -d /tmp/gitups-test1.XXXXXX)
mkdir -p "$OUT/test1"
cp tests/e2e/test1/provision.yaml "$OUT/test1/provision.yaml"
```

`$OUT/test1/provision.yaml` is now the input for every subsequent
gitups command. Use `-d "$OUT"` to point the CLI at it.

## 3. Check + expand + fill argo KRC placeholders

```bash
./bin/gitups check  test1 -d "$OUT"
./bin/gitups expand test1 -d "$OUT" --force
```

`expand` writes `$OUT/test1/full-provision.yaml`. The only placeholders
are the per-repo `repoURL` the argocd KRC needs for every managed output
repo (it has to know where each rendered tree will be pushed). Fill
them in-place — for a kind-only smoke run, any URL works because we
skip `gitups push`:

```bash
for r in basic-infra basic-infra-dev gitops-controllers gitops-controllers-dev; do
  ./bin/gitups fill test1 -d "$OUT" \
    --set "argocd-managed-repo-${r}.repoURL=https://example.invalid/gitops/${r}.git"
done
./bin/gitups check test1 -d "$OUT"   # must now report zero placeholders
```

## 4. Generate rendered trees

```bash
./bin/gitups generate test1 -d "$OUT" --context test1 --prune
ls "$OUT/test1/"
# expected: basic-infra/  basic-infra-dev/  gitops-controllers/  gitops-controllers-dev/
```

## 5. Apply to kind

```bash
./bin/gitups apply test1 -d "$OUT" --to kind-test1 --wait-crds
./bin/gitups wait  test1 -d "$OUT" --to kind-test1 --timeout 10m
```

## 6. Verify

```bash
kubectl --context kind-test1 -n ingress-nginx get pods
kubectl --context kind-test1 -n argocd        get pods

# Argo CD UI via port-forward (no ingress wired in test1):
kubectl --context kind-test1 -n argocd port-forward svc/argocd-server 8080:443
# then open https://localhost:8080
```

Admin password (argo-cd helm chart default):

```bash
kubectl --context kind-test1 -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d; echo
```

## 7. Tear down

```bash
kind delete cluster --name test1
rm -rf "$OUT"
```
