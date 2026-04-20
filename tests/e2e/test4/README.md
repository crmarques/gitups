# test4 — test3 + vault + declarest reconciling keycloak

Builds on [test3](../test3/README.md) by adding:

- **vault** (helm, dev mode) — secret backend for declarest
- **declarest** (OLM) — service-resource-controller that reconciles
  Keycloak's Admin REST API from Git via a metadata bundle

The env repo composes declarest's four CRDs explicitly, one per
scope:

- `ResourceRepository` → env git URL
- `ManagedService` → Keycloak Admin API + `keycloak-bundle:1.0.0`
- `SecretStore` → vault (emitted by vault's `secret-store` resource)
- `SyncPolicy` → `/realms/master` under the above three

Read [agents/references/declarest.md](../../../agents/references/declarest.md)
before changing this case — test4 is the reference wiring for
declarest-reconciled services.

## Topology

| Repo                      | Package / Resource                        | Install / Template     | Notes                                                               |
| ------------------------- | ----------------------------------------- | ---------------------- | ------------------------------------------------------------------- |
| `basic-infra`             | `olm`, `metallb`, `nginx-ingress`         | raw / olm / helm       | Same as test3.                                                      |
| `basic-infra-dev`         | metallb→nginx-ingress `lb-ip-pool`        | binding                | Same as test3.                                                      |
| `support-services`        | `keycloak`, `vault`                       | olm / helm             | Keycloak operator + vault dev server.                               |
| `support-services-dev`    | `vault/secret-store/vault`                | resource               | Emits a declarest `SecretStore` CR pointing at in-cluster vault.    |
| `gitops-controllers`      | `argocd`, `declarest`                     | olm / olm              | Both controllers via OLM catalog.                                   |
| `gitops-controllers-dev`  | declarest `resource-repository/env-repo`  | resource               | `ResourceRepository` CR → env git URL.                              |
| `gitops-controllers-dev`  | declarest `managed-service/keycloak`      | resource               | `ManagedService` CR → Keycloak admin REST + `keycloak-bundle:1.0.0`.|
| `gitops-controllers-dev`  | declarest `sync-policy/keycloak-master-realm` | resource           | `SyncPolicy` CR gluing the three above, scoped to `/realms/master`. |

Everything in `gitops-controllers-dev` is a normal declarative
composition — no interface projection, no per-integration CRD.
Gitups emits the four declarest CRs and declarest takes it from
there.

## Prerequisites

- `kind`, `kubectl`, `helm`, `kustomize`, `go`
- `make build` has run (so `./bin/gitups` exists)
- Sibling `gitops-workspace/gitups-packages` checkout
- The metallb CIDR `172.18.255.200-172.18.255.250` is inside the kind
  docker network (usually `172.18.0.0/16`)

## 1. Bring up kind

```bash
kind create cluster --name test4
kubectl --context kind-test4 cluster-info
```

## 2. Seed the workspace

```bash
OUT=$(mktemp -d /tmp/gitups-test4.XXXXXX)
mkdir -p "$OUT/test4"
cp tests/e2e/test4/provision.yaml "$OUT/test4/provision.yaml"
```

## 3. Check, expand, fill, generate

```bash
./bin/gitups check    test4 -d "$OUT"
./bin/gitups expand   test4 -d "$OUT" --force
```

Placeholders to fill after expand:

- one `argocd-managed-repo-<repo>.repoURL` per managed output repo
- `declarest-resource-repository-env-repo.git.url` — the env repo's
  git URL declarest should watch
- `vault.server.dev.devRootToken` — dev-mode root token seeded into
  the vault helm install (the same value goes into the
  `vault-root-token` Secret in step 5)

```bash
for r in basic-infra basic-infra-dev support-services support-services-dev \
         gitops-controllers-dev; do
  ./bin/gitups fill test4 -d "$OUT" \
    --set "argocd-managed-repo-${r}.repoURL=https://example.invalid/gitops/${r}.git"
done

ENV_REPO_URL="https://example.invalid/gitops/gitops-controllers-dev.git"
VAULT_ROOT_TOKEN="test4-vault-root"

./bin/gitups fill test4 -d "$OUT" \
  --set "declarest-resource-repository-env-repo.git.url=${ENV_REPO_URL}"
./bin/gitups fill test4 -d "$OUT" \
  --set "vault.server.dev.devRootToken=${VAULT_ROOT_TOKEN}"

./bin/gitups check    test4 -d "$OUT"
./bin/gitups generate test4 -d "$OUT" --context test4 --prune
```

Inspect the rendered declarest CRs:

```bash
ls "$OUT/test4/gitops-controllers-dev/packages/declarest/resources/"
# resource-repository/  managed-service/  sync-policy/  kustomization.yaml

cat "$OUT/test4/gitops-controllers-dev/packages/declarest/resources/managed-service/keycloak/managed-service.yaml"
# ManagedService pointing at keycloak-service.keycloak.svc:8080
# metadata.bundle: keycloak-bundle:1.0.0

cat "$OUT/test4/support-services-dev/packages/vault/resources/secret-store/vault/secret-store.yaml"
# SecretStore named "vault" in declarest-system, vault mode
```

## 4. Apply to kind

```bash
./bin/gitups apply test4 -d "$OUT" --to kind-test4 --wait-crds
./bin/gitups wait  test4 -d "$OUT" --to kind-test4 --timeout 15m
```

`gitups wait` polls OLM subscriptions until every CSV is `Succeeded`
(OLM itself, metallb-operator, keycloak-operator, argocd-operator,
declarest-operator).

### Bootstrap-sync via declarest CLI

For the `SyncPolicy` unit, gitups follows the bootstrap-sync flow
declared by the declarest package's `spec.cli.intents.sync-policy`:

1. `kubectl apply -k …/sync-policy/keycloak-master-realm/` — the SP
   CR lands in the cluster so the operator can take over at steady
   state.
2. `declarest resource apply --kube-context=kind-test4 --sync-policy=<rendered-sp.yaml>` —
   the CLI immediately reconciles `/realms/master` using the co-
   resident `ResourceRepository`, `ManagedService`, and `SecretStore`
   CRs (already applied in earlier apply-waves).

This makes bootstrap independent of the operator's readiness. The
binary needs to be on `$PATH` (see
[declarest/README](https://github.com/crmarques/declarest#install)).

## 5. Provision the tokens declarest's CRs reference

The rendered `SecretStore` references `vault-root-token` in
`declarest-system`, and the rendered `ManagedService` references
`keycloak-admin-token` (same namespace). Declarest itself doesn't
create these Secrets — they are environment state. Seed them by
hand for the smoke:

```bash
# Vault dev-mode ships the token set at install time. Mirror it into
# a K8s Secret declarest can pull from.
kubectl --context kind-test4 -n declarest-system create secret generic vault-root-token \
  --from-literal=token="${VAULT_ROOT_TOKEN}"

# Keycloak admin bearer for declarest's ManagedService. In a real
# deployment this is an API client bound to the Keycloak "admin-cli"
# client. For the smoke it can be any non-empty value until the
# operator brings up a Keycloak server instance.
kubectl --context kind-test4 -n declarest-system create secret generic keycloak-admin-token \
  --from-literal=token='PLACEHOLDER-REPLACE-WITH-REAL-ADMIN-TOKEN'

# Git token for declarest's ResourceRepository. Example.invalid will
# not work at runtime; swap the URL + token for a real repo to
# exercise the sync.
kubectl --context kind-test4 -n declarest-system create secret generic repository-credentials \
  --from-literal=token='PLACEHOLDER-GIT-TOKEN'
```

## 6. Verify

```bash
# Declarest CRDs and operator
kubectl --context kind-test4 -n declarest-system get csv
kubectl --context kind-test4 -n declarest-system get resourcerepositories,managedservices,secretstores,syncpolicies

# ManagedService spec.metadata.bundle
kubectl --context kind-test4 -n declarest-system get managedservice keycloak \
  -o jsonpath='{.spec.metadata.bundle}'; echo
# → keycloak-bundle:1.0.0

# SyncPolicy status (will likely carry errors until real repo + tokens are wired)
kubectl --context kind-test4 -n declarest-system describe syncpolicy keycloak-master-realm
```

Resource payloads that the `SyncPolicy` syncs live at
`/realms/master/**` inside the `ResourceRepository`'s git URL. They
are **not** generated by this Provision — authoring them is the
user's responsibility (a realm JSON + any nested collections the
bundle's metadata exposes). Declarest's CLI (`declarest resource save
/realms/master`) is the usual way to seed them from a running
Keycloak.

## Limitations

- The Keycloak operator is installed but **no `Keycloak` CR** is
  instantiated (same caveat as test3). Without a Keycloak server
  instance + real admin credentials, the `SyncPolicy` will not
  reconcile anything meaningful. The case proves the gitups-level
  wiring, not a live reconcile.
- `repository-credentials`, `keycloak-admin-token`, and
  `vault-root-token` are environment state the user provisions out
  of band (or via a secrets management follow-up).
- `keycloak-bundle:1.0.0` is a declared bundle reference; declarest
  resolves it from OCI at runtime. In an air-gapped smoke, you may
  need to pre-populate the bundle cache or swap `metadata.bundle`
  for an in-repo `bundleFile` path.

## 7. Tear down

```bash
kind delete cluster --name test4
rm -rf "$OUT"
```
