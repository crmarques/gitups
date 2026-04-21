# test5 — haproxy fronting nginx fronting gitea, with declarest managing per-service git repos

End-to-end smoke that exercises **every major test5-era contract change** in one
Provision:

- **Lean Provision, fat FullProvision.** The `provision.yaml` carries only
  what's declarative intent: which packages, which renderer, which repo each
  package lands in, and which provider/consumer binding wires metallb to
  nginx-ingress. Concrete IPs (`172.18.255.200-172.18.255.249`), service
  hostnames (`http://gitea-http.gitea.svc.cluster.local:3000`), and bundle
  references (`gitea:0.0.1`) are filled into `full-provision.yaml`
  *after* expand, via `gitups fill --set`.
- **KRC-declared kubectl.** Gitups core carries no hard-coded cluster binary.
  The argocd package's `spec.cli` (see
  [gitups-packages/packages/argocd/package.yaml](../../../../gitups-packages/packages/argocd/package.yaml))
  declares `binary: kubectl` and one args template per intent — `apply`,
  `apply-dry-run`, `get-json`, `wait-condition`, `wait-crds-established`,
  `list-crds`, `server-version`, `list-pods-jsonpath`, `pod-logs`. Gitups
  apply looks up the selected KRC, builds a `cluster.KubeClient` from that
  block, and every cluster operation it performs is that KRC's declared
  binary + rendered args. A different KRC (flux, cluster-api-style,
  whatever) can be swapped in without gitups core changes.
- **Service-resources repos.** `gitea-dsv` and `haproxy-dsv` are rendered
  as skeleton repos (README + empty kustomization) whose
  `managedServiceRef: {repo, instance}` points at the declarest
  `ManagedService` CR in `controllers-dev`. Users (or
  `declarest resource save`) seed their actual declarest payloads —
  gitea org/repo/user JSON, haproxy backends/frontends — directly in
  these repos. Gitups does not author payloads.
- **Readiness-gated apply waves.** Between waves, gitups blocks on each
  prior unit's package-level or descriptor-level readiness checks using
  the KRC's `wait-condition` intent. So "configure gitea repo" (a
  future declarest payload sync pass) can't fire until gitea's
  Deployment/StatefulSet is Available.
- **Bootstrap-sync via declarest CLI (indirect).** Every `SyncPolicy`
  resource unit is tagged by
  [spec.cli.intents.sync-policy](../../../../gitups-packages/packages/declarest/package.yaml)
  at resolve time. At apply time gitups `kubectl apply`s the CR
  (through the KRC CLI) so the operator can take over, then runs
  `declarest resource apply --kube-context=… --sync-policy=…` for one
  immediate reconciliation. No operator readiness on the critical path.

## Topology

Traffic flow at runtime:

```
  external client
      │ http://<metallb-pool-ip>/
      ▼
  haproxy (Service type=LoadBalancer, gets IP from metallb pool)
      │
      ▼
  nginx-ingress (ClusterIP, sits behind haproxy)
      │
      ▼
  gitea (Deployment + ClusterIP Service, fronted by an Ingress)
```

Declarest side:

```
  controllers-dev/
    ResourceRepository/gitea  ─► gitea-dsv (service-resources repo)
    ResourceRepository/haproxy ─► haproxy-dsv (service-resources repo)
    ManagedService/gitea (bundle: gitea:0.0.1)
    ManagedService/haproxy (bundle: haproxy:0.0.1)
    SecretStore/local (file mode — no vault in this smoke)
    SyncPolicy/gitea  wires (repo=gitea, svc=gitea, secrets=local, path=/)
    SyncPolicy/haproxy wires (repo=haproxy, svc=haproxy, secrets=local, path=/)
```

| Repo                        | Type                  | Contents                                                                 |
| --------------------------- | --------------------- | ------------------------------------------------------------------------ |
| `basic-infra`               | kubernetes-resources  | OLM + metallb (OLM) + nginx-ingress (helm).                              |
| `basic-infra-dev`           | kubernetes-resources  | Binding: metallb → nginx-ingress `lb-pool` (materialises IPAddressPool). |
| `support-services`          | kubernetes-resources  | haproxy (helm, Service type=LoadBalancer) + gitea (helm).                |
| `support-services-dev`      | kubernetes-resources  | No overrides (defaults suffice).                                         |
| `controllers`               | kubernetes-resources  | argocd (OLM) + declarest (OLM).                                          |
| `controllers-dev`           | kubernetes-resources  | declarest CRs: 1× SecretStore, 2× ResourceRepository, 2× ManagedService, 2× SyncPolicy. |
| `gitea-dsv`                 | **service-resources** | Skeleton for gitea's declarest payloads (user-authored).                 |
| `haproxy-dsv`               | **service-resources** | Skeleton for haproxy's declarest payloads (user-authored).               |

## Prerequisites

- `kind`, `kubectl`, `helm`, `kustomize`, `go`
- `make build` has run (so `./bin/gitups` exists)
- Sibling `gitups-workspace/gitups-packages` checkout (this test's
  `spec.sources[0].path: ../../../../gitups-packages/packages` assumes
  the in-tree layout; run the commands below from the gitups repo root)

## 1. Bring up kind

```bash
kind create cluster --name test5
kubectl --context kind-test5 cluster-info
```

The metallb CIDR `172.18.255.200-172.18.255.249` should be inside the
kind docker network (usually `172.18.0.0/16`). Check with
`docker network inspect kind | grep Subnet`.

## 2. Check, expand, fill, generate — in-tree

```bash
./bin/gitups check    test5 -d tests/e2e
./bin/gitups expand   test5 -d tests/e2e --force
```

Fill the 11 placeholders (the URLs and bundle refs that vary per env):

```bash
for r in basic-infra basic-infra-dev support-services support-services-dev controllers-dev; do
  ./bin/gitups fill test5 -d tests/e2e \
    --set "argocd-managed-repo-${r}.repoURL=https://gitea.dsv.local/gitops/${r}.git"
done

./bin/gitups fill test5 -d tests/e2e \
  --set "declarest-resource-repository-gitea.git.url=https://gitea.dsv.local/gitops/gitea-dsv.git" \
  --set "declarest-resource-repository-haproxy.git.url=https://gitea.dsv.local/gitops/haproxy-dsv.git" \
  --set "declarest-managed-service-gitea.http.baseURL=http://gitea-http.gitea.svc.cluster.local:3000" \
  --set "declarest-managed-service-gitea.metadata.bundle=gitea:0.0.1" \
  --set "declarest-managed-service-haproxy.http.baseURL=http://haproxy-kubernetes-ingress.haproxy.svc.cluster.local:5555" \
  --set "declarest-managed-service-haproxy.metadata.bundle=haproxy:0.0.1"

./bin/gitups check    test5 -d tests/e2e
./bin/gitups generate test5 -d tests/e2e --context test5 --prune
```

Inspect the rendered CRs:

```bash
cat tests/e2e/test5/basic-infra-dev/packages/metallb/resources/pool/lb-pool-basic-infra-dev/ipaddresspool.yaml
# → IPAddressPool "edge-pool" with CIDR 172.18.255.200-172.18.255.249

cat tests/e2e/test5/controllers-dev/packages/declarest/resources/managed-service/gitea/managed-service.yaml
# → ManagedService "gitea" pointing at gitea-http.gitea.svc:3000, bundle: gitea:0.0.1

cat tests/e2e/test5/gitea-dsv/README.md
# → "Reconciled by declarest against the ManagedService/gitea CR declared in repo controllers-dev."
```

## 3. Apply to kind

```bash
./bin/gitups apply test5 -d tests/e2e --to kind-test5 --wait-crds
./bin/gitups wait  test5 -d tests/e2e --to kind-test5 --timeout 15m
```

Under the hood:

- Gitups loads `Provision`, resolves the KRC
  (`controllers/argocd`), loads its `spec.cli.intents` from the
  argocd package, and builds a `cluster.KubeClient`. Every cluster op
  from here on runs `kubectl …` only because argocd declares it.
- The apply plan is ordered by `ApplyWave` (dependsOn topological
  sort). Between waves, gitups waits for each applied unit's
  `readiness[]` checks via the KRC's `wait-condition` intent.
- `SyncPolicy` units follow the declarest bootstrap-sync intent:
  kubectl-apply the CR first, then run
  `declarest resource apply --kube-context=kind-test5 --sync-policy=…`
  for an immediate reconciliation pass. `declarest` must be on
  `$PATH` (see [declarest/README](https://github.com/crmarques/declarest#install)).

## 4. Provision declarest-side secrets

The rendered CRs reference several K8s Secrets that declarest reads at
reconcile time; gitups does not write real secret values into rendered
trees. Seed them by hand for the smoke:

```bash
kubectl --context kind-test5 -n declarest-system create secret generic gitea-admin-token \
  --from-literal=token='PLACEHOLDER-REPLACE-WITH-REAL-GITEA-PAT'

kubectl --context kind-test5 -n declarest-system create secret generic haproxy-dataplane-token \
  --from-literal=token='PLACEHOLDER-REPLACE-WITH-REAL-HAPROXY-DATAPLANE-CREDENTIALS'

kubectl --context kind-test5 -n declarest-system create secret generic repository-credentials \
  --from-literal=token='PLACEHOLDER-GIT-TOKEN'
```

## 5. Verify the traffic flow

```bash
# haproxy Service gets an external IP from the metallb pool.
kubectl --context kind-test5 -n haproxy get svc haproxy-kubernetes-ingress -o wide

# nginx-ingress stays ClusterIP (internal).
kubectl --context kind-test5 -n ingress-nginx get svc ingress-nginx-controller -o wide

# gitea is reachable via haproxy's external IP → nginx Ingress → gitea Service.
LB_IP=$(kubectl --context kind-test5 -n haproxy get svc haproxy-kubernetes-ingress -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
curl -sS -H "Host: gitea.dsv.local" "http://${LB_IP}/"
```

## Limitations

- Haproxy's Data Plane API is not enabled in the default helm install,
  so declarest's `ManagedService/haproxy` + `SyncPolicy/haproxy` will
  log errors trying to authenticate until a user enables the Data
  Plane sidecar. The case verifies gitups-level wiring (CRs, bundle
  refs, per-service repos), not a live haproxy reconcile.
- `gitea-admin-bundle` / `haproxy-dataplane-bundle` are declared bundle
  references; declarest resolves them from OCI at runtime. Pre-populate
  the bundle cache or swap `metadata.bundle` for an in-repo
  `bundleFile` path in air-gapped smokes.
- The Ingress routing haproxy → nginx → gitea requires either
  `DEFAULT_INGRESS_CLASS` + a haproxy Backend that forwards to the
  nginx Service, or declarest to author haproxy backends/frontends via
  the Data Plane API. Both are out-of-scope for the
  gitups-level wiring this test proves.

## 6. Tear down

```bash
kind delete cluster --name test5
rm -rf tests/e2e/test5/{basic-infra,basic-infra-dev,support-services,support-services-dev,controllers,controllers-dev,gitea-dsv,haproxy-dsv,full-provision.yaml}
```
