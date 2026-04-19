# test3 — OLM-first stack with metallb → nginx-ingress → keycloak

Promotes the stack to OLM-managed installs wherever a catalog entry
exists. MetalLB assigns a LoadBalancer IP to the nginx-ingress
controller through the `lb-ip-pool` capability binding. The Keycloak
operator is installed from OperatorHub but a Keycloak server instance
is **not** instantiated here — see the "Limitations" note below.

## Topology

| Repo                      | Package         | Install | Notes                                                              |
| ------------------------- | --------------- | ------- | ------------------------------------------------------------------ |
| `basic-infra`             | `olm`           | raw     | Operator Lifecycle Manager bootstrap                               |
| `basic-infra`             | `metallb`       | olm     | `metallb-operator.v0.14.0`                                         |
| `basic-infra`             | `nginx-ingress` | helm    | `controller.service.type=LoadBalancer`                             |
| `basic-infra-dev`         | `nginx-ingress` | —       | adds `lb-ip-pool` binding: metallb → nginx-ingress, pool `ingress-pool` |
| `support-services`        | `keycloak`      | olm     | `keycloak-operator.v26.5.0`                                        |
| `support-services-dev`    | —               | —       |                                                                    |
| `gitops-controllers`      | `argocd`        | olm     | `argocd-operator.v0.17.0`                                          |
| `gitops-controllers-dev`  | —               | —       |                                                                    |

Renderer priority from [AGENTS.md §2](../../../AGENTS.md) is followed:
OLM first, Helm where no OLM catalog entry exists (nginx-ingress), raw
only for OLM itself.

## Prerequisites

- `kind`, `kubectl`, `helm`, `kustomize`, `go`
- `make build` so `./bin/gitups` exists
- Sibling `gitops-workspace/gitups-packages` checkout
- The MetalLB pool CIDR `172.18.255.200-172.18.255.250` must be inside
  the kind docker network (the default `kind` network uses `172.18.0.0/16`)

## 1. Bring up kind

```bash
kind create cluster --name test3
kubectl --context kind-test3 cluster-info
docker network inspect kind -f '{{(index .IPAM.Config 0).Subnet}}'   # should contain 172.18.0.0/16
```

## 2. Seed the workspace

```bash
OUT=$(mktemp -d /tmp/gitups-test3.XXXXXX)
mkdir -p "$OUT/test3"
cp tests/e2e/test3/provision.yaml "$OUT/test3/provision.yaml"
```

## 3. Check, expand, fill, generate

```bash
./bin/gitups check    test3 -d "$OUT"
./bin/gitups expand   test3 -d "$OUT" --force

for r in basic-infra basic-infra-dev support-services support-services-dev \
         gitops-controllers gitops-controllers-dev; do
  ./bin/gitups fill test3 -d "$OUT" \
    --set "argocd-managed-repo-${r}.repoURL=https://example.invalid/gitops/${r}.git"
done

./bin/gitups check    test3 -d "$OUT"
./bin/gitups generate test3 -d "$OUT" --context test3 --prune
```

Inspect the binding output:

```bash
ls "$OUT/test3/basic-infra-dev/packages/metallb/resources/pool/nginx-ingress-lb-basic-infra-dev/"
ls "$OUT/test3/basic-infra-dev/packages/nginx-ingress/resources/lb-binding/nginx-ingress-lb-basic-infra-dev/"
```

Both sides carry matching `gitups.io/apply-wave` annotations and the
provider wave must be less than the consumer wave.

## 4. Apply to kind

```bash
./bin/gitups apply test3 -d "$OUT" --to kind-test3 --wait-crds
./bin/gitups wait  test3 -d "$OUT" --to kind-test3 --timeout 15m
```

`gitups wait` polls OLM subscriptions until every CSV reaches
`Succeeded` (OLM itself, metallb-operator, keycloak-operator,
argocd-operator).

## 5. Verify

```bash
# OLM + operators
kubectl --context kind-test3 -n olm            get csv
kubectl --context kind-test3 -n metallb-system get pods
kubectl --context kind-test3 -n keycloak       get pods      # operator only
kubectl --context kind-test3 -n argocd         get pods

# nginx-ingress should hold a LoadBalancer IP from the metallb pool
kubectl --context kind-test3 -n ingress-nginx get svc ingress-nginx-controller \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}'; echo
```

## Limitations

The Keycloak **operator** is installed from OperatorHub but this case
does not create a `Keycloak` custom resource — doing so requires a
reachable Postgres, which is intentionally out of scope for the
"installs + binding" smoke. Exposing Keycloak through the ingress
controller therefore has no origin to proxy to yet; test4 addresses the
missing wiring by moving Keycloak configuration under declarest.

If you want to exercise the ingress path end-to-end here, add an
env-repo resource that deploys a throwaway HTTP backend + `Ingress`
targeting the nginx class — or run test2, which uses the helm-based
Keycloak chart that ships its own server + ingress.

## 6. Tear down

```bash
kind delete cluster --name test3
rm -rf "$OUT"
```
