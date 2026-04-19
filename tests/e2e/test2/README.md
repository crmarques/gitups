# test2 — argocd + nginx-ingress + keycloak via Helm on kind

Builds on [test1](../test1/README.md) by adding Keycloak (Bitnami chart)
and publishing it through the nginx-ingress controller as
`keycloak.test2.local`. Still helm-only; still no MetalLB, no OLM.

## Topology

| Repo                      | Package         | Install    | Notes                                                                        |
| ------------------------- | --------------- | ---------- | ---------------------------------------------------------------------------- |
| `basic-infra`             | `nginx-ingress` | helm       | `controller.service.type=ClusterIP`                                          |
| `basic-infra-dev`         | (env overlay)   | —          |                                                                              |
| `support-services`        | `keycloak`      | helm       | bitnami `24.4.3` / app `26.0.7`; `ingress.enabled=true`, class `nginx`       |
| `support-services-dev`    | (env overlay)   | —          |                                                                              |
| `gitops-controllers`      | `argocd`        | helm       |                                                                              |
| `gitops-controllers-dev`  | (env overlay)   | —          |                                                                              |

## Prerequisites

- `kind`, `kubectl`, `helm`, `kustomize`, `go`
- `make build` has been run in the gitups repo so `./bin/gitups` exists
- Sibling `gitops-workspace/gitups-packages` checkout

## 1. Bring up kind with host ports

Since `nginx-ingress` runs as `ClusterIP`, the cleanest way to reach the
controller from outside the cluster is to map host ports onto the
control-plane node:

```bash
cat >/tmp/kind-test2.yaml <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 80
        hostPort: 8080
        protocol: TCP
      - containerPort: 443
        hostPort: 8443
        protocol: TCP
EOF
kind create cluster --name test2 --config /tmp/kind-test2.yaml
```

Alternative: skip `extraPortMappings` and use
`kubectl port-forward -n ingress-nginx svc/ingress-nginx-controller 8080:80`.

## 2. Seed the workspace

```bash
OUT=$(mktemp -d /tmp/gitups-test2.XXXXXX)
mkdir -p "$OUT/test2"
cp tests/e2e/test2/provision.yaml "$OUT/test2/provision.yaml"
```

## 3. Check, expand, fill, generate

```bash
./bin/gitups check    test2 -d "$OUT"
./bin/gitups expand   test2 -d "$OUT" --force
```

The keycloak helm values are seeded from `provision.yaml`'s `values`
block, so the only remaining placeholders are the per-repo `repoURL`
entries the argocd KRC needs for each managed output repo:

```bash
for r in basic-infra basic-infra-dev support-services support-services-dev \
         gitops-controllers gitops-controllers-dev; do
  ./bin/gitups fill test2 -d "$OUT" \
    --set "argocd-managed-repo-${r}.repoURL=https://example.invalid/gitops/${r}.git"
done
./bin/gitups check    test2 -d "$OUT"
./bin/gitups generate test2 -d "$OUT" --context test2 --prune
```

## 4. Apply to kind

```bash
./bin/gitups apply test2 -d "$OUT" --to kind-test2 --wait-crds
./bin/gitups wait  test2 -d "$OUT" --to kind-test2 --timeout 15m
```

## 5. Verify

```bash
kubectl --context kind-test2 -n ingress-nginx get pods
kubectl --context kind-test2 -n keycloak     get pods,svc,ingress
kubectl --context kind-test2 -n argocd       get pods
```

Resolve `keycloak.test2.local` to the host port nginx exposes (8080/8443
when the `extraPortMappings` block above was used):

```bash
echo '127.0.0.1 keycloak.test2.local' | sudo tee -a /etc/hosts
curl -kI https://keycloak.test2.local:8443/
# or HTTP on 8080 if TLS is not configured:
curl  -I http://keycloak.test2.local:8080/
```

Then open `http://keycloak.test2.local:8080/` and log in with
`admin` / `admin-dev-password` (from `provision.yaml`).

## 6. Tear down

```bash
kind delete cluster --name test2
sudo sed -i '/keycloak.test2.local/d' /etc/hosts
rm -rf "$OUT"
```
