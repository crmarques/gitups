# Declarest Integration

Gitups's canonical service-resource-controller (SRC) is
[DeclaREST](https://github.com/crmarques/declarest). Read this file
before touching any SRC package, any service-level reconciliation
plumbing, or the test4-class e2e flows.

## What declarest is

DeclaREST reconciles **REST-API-backed systems** from desired state
stored in Git. It does **not** define custom CRDs for each product; it
has a single, generic operator model with four CRDs that reconcile
**any** REST API, driven by **metadata bundles** that describe how the
logical paths map to the target API.

Two ways to use it:

- **CLI**: `declarest resource save | diff | apply /path/to/resource`
  — on-demand, deterministic, good for local workflows and CI.
- **Operator**: four CRDs in `declarest.io/v1alpha1` that reconcile a
  Git repository → target REST API continuously.

## The four CRDs

`ResourceRepository + ManagedService + SecretStore → SyncPolicy` — the
`SyncPolicy` is the execution unit and references the other three.

| Kind | Purpose | Key fields |
|------|---------|-----------|
| `ResourceRepository` | Git repo holding desired-state files | `spec.type=git`, `spec.git.url`, `spec.git.branch`, `spec.git.auth.tokenRef`, `spec.storage.pvc.accessModes` |
| `ManagedService` | Target REST API + auth + bundle | `spec.http.baseURL`, `spec.http.auth.{oauth2|basicAuth|customHeaders}`, `spec.metadata.bundle: <name>:<version>` |
| `SecretStore` | Where secrets resolve | exactly one of `spec.file.*` or `spec.vault.*` (with `spec.vault.auth.{token|userpass|appRole}.*Ref`) |
| `SyncPolicy` | Glues the three and scopes the sync | `spec.resourceRepositoryRef`, `spec.managedServiceRef`, `spec.secretStoreRef`, `spec.source.path`, `spec.syncInterval`, `spec.sync.{force,prune}` |

There is **no** per-product CRD (no `KeycloakRealm`, no
`HttpProxyBackend`, no `RundeckJob`). Everything is a generic
`SyncPolicy` pointed at a source path whose meaning is defined by the
active bundle's metadata.

## Metadata bundles

A **bundle** is a reusable package of declarest metadata for a
specific API product (Keycloak Admin API, HAProxy Data Plane, Rundeck,
…). Shipped as `bundle.yaml` + a metadata tree (+ optional OpenAPI) in
an OCI tarball.

```yaml
# bundle.yaml (authoring; see declarest/agents/reference/metadata-bundle.md)
apiVersion: declarest.io/v1alpha1
kind: MetadataBundle
name: keycloak-bundle
version: 1.0.0
description: Metadata bundle for Keycloak Admin REST API.
declarest:
  metadataRoot: metadata
  openapi: openapi.yaml
  compatibleDeclarest: ">=0.3.0"
  compatibleManagedService:
    product: keycloak
    versions: ">=26 <27"
```

A `ManagedService` consumes the bundle via either:

```yaml
spec:
  metadata:
    bundle: keycloak-bundle:1.0.0            # OCI shorthand
    # bundleFile: /path/to/keycloak-bundle-1.0.0.tar.gz
```

Bundles map logical paths (`/realms/master/clients/gitups`) to real
API endpoints, methods, transforms, identity templates, compare
rules, secret attributes, and externalized fields. **Packages that
declare they are declarest-reconcilable ship (or reference) exactly
one bundle**; the SRC loads it at runtime from the
`ManagedService.spec.metadata.bundle` reference.

## How a gitups package integrates with declarest

A workload package opts in by declaring `spec.declarestBundle` in
`package.yaml`:

```yaml
apiVersion: gitups/v1alpha1
kind: PackageDefinition
metadata:
  name: keycloak
  version: 0.0.1
spec:
  role: workload
  category: identity
  defaultInstall: olm
  declarestBundle:
    name: keycloak-bundle
    version: 1.0.0
    ref: ghcr.io/crmarques/declarest-bundles/keycloak-bundle:1.0.0
```

The bundle declaration is advisory to gitups core — gitups never
fetches it. It lets Provisions reference the bundle deterministically
when composing a `ManagedService` CR, and it gives `gitups check` a
stable way to verify cross-package compatibility (e.g., "this package
ships `keycloak-bundle:1.0.0`, so a `ManagedService` pointing at it
uses `metadata.bundle: keycloak-bundle:1.0.0`").

Resource payloads (the JSON/YAML files declarest syncs) live in the
env output repo at paths that match the bundle's logical layout,
e.g. `realms/master/resource.json`. Packages may ship resource
templates that seed a starter payload, or users may author them
directly.

## Wiring declarest in a Provision

Declarest itself is a regular package with `role: service-resource-controller`
and install methods `olm` + `raw`. It ships four resource templates —
one per declarest CRD — in `resources/`:

```
packages/declarest/
  install/{olm,raw}/descriptor.yaml
  resources/
    resource-repository/descriptor.yaml   # renders ResourceRepository CR
    managed-service/descriptor.yaml       # renders ManagedService CR
    secret-store/descriptor.yaml          # renders SecretStore CR
    sync-policy/descriptor.yaml           # renders SyncPolicy CR
  service-resource-controller/
    managed-resource/descriptor.yaml      # controller intent hook
```

A Provision composing declarest picks those resource templates
explicitly in the env repo, one per concrete scope:

```yaml
- name: gitops-controllers-{{.Env}}
  type: kubernetes-resources
  repoRef:
    name: gitops-controllers
  packages:
    - template: local/declarest
      resources:
        - template: resource-repository
          name: env-repo
          values:
            git.url: "https://example.com/gitops/services-keycloak-dev.git"
        - template: managed-service
          name: keycloak
          values:
            http.baseURL: "https://keycloak.svc.cluster.local:8443"
            metadata.bundle: keycloak-bundle:1.0.0
            auth.customHeaders.headerName: Authorization
            auth.customHeaders.prefix: Bearer
            auth.customHeaders.valueRef.name: keycloak-admin
            auth.customHeaders.valueRef.key: token
        - template: secret-store
          name: vault
          values:
            vault.address: "http://vault.vault.svc:8200"
            vault.auth.token.secretRef.name: vault-root
            vault.auth.token.secretRef.key: token
        - template: sync-policy
          name: keycloak-master-realm
          values:
            resourceRepositoryRef.name: env-repo
            managedServiceRef.name: keycloak
            secretStoreRef.name: vault
            source.path: /realms/master
```

The `vault` package ships its own `resources/secret-store/` template
that emits a `SecretStore` CR configured for vault — consumers may
import it via `defaultResources` or a capability binding instead of
authoring it inline.

## Secrets

Declarest stores secret placeholders in resource payloads and
resolves them at apply/diff time from the active `SecretStore`:

```json
{
  "clientSecret": "{{secret .}}"
}
```

- `{{secret .}}` binds to the JSON-pointer path where the placeholder
  appears (field name derived by declarest).
- `{{secret some-key}}` binds to an explicit key in the store.
- `SecretStore` backends: `file` (encrypted file on a PVC) or `vault`
  (HashiCorp Vault).

Gitups packages MUST NOT write real secret values into payload files
or into `FullProvision`. The vault package's `secret-store` resource
template exposes the vault address and token secret ref; real
credentials live in Kubernetes Secrets referenced from `SecretStore`.

## Bootstrap-sync via the declarest CLI

Waiting for the declarest operator to become Ready on a cold cluster
adds latency to the first reconcile and couples gitups apply to the
operator's readiness probe. To keep bootstrap fast and operator-
independent, the declarest package declares a **bootstrap-sync
intent** under `spec.cli.intents`:

```yaml
spec:
  role: service-resource-controller
  cli:
    binary: declarest
    # Default CLI-only invocation (managed-script intent etc.).
    args:
      - apply
      - --context={{.KubeContext}}
      - --file={{.ManifestPath}}
    # Per-intent bootstrap-sync invocations. Matched by resource
    # template name at resolve time.
    intents:
      sync-policy:
        args:
          - resource
          - apply
          - --kube-context={{.KubeContext}}
          - --sync-policy={{.ManifestPath}}/sync-policy.yaml
```

At resolve time,
[controllers.go](../../internal/resolve/controllers.go)
(`rewireSRCIntents`) walks every resource unit. When the unit's
`resourceTemplate` matches a key in the selected SRC's
`spec.cli.intents`, gitups tags the unit with
`Controller: {Kind: service-resource-controller, Intent: <name>}`.

At apply time, for every unit with an intent present in `Intents`:

1. `kubectl apply -k <unitDir>` — the rendered CR lands in the
   cluster so the operator can assume control on its own cadence.
2. Then `declarest <args>` — the binary runs one immediate
   reconciliation using the rendered CR as input.

The CLI invocation is expected to resolve any referenced CRs
(`resourceRepositoryRef`, `managedServiceRef`, `secretStoreRef` on a
`SyncPolicy`) from the live cluster; by step 2 they are already
applied because gitups applies earlier `applyWave` units before the
SyncPolicy.

CLI-only intents (today: `managed-script`) still live in
`spec.cli.args` and do not appear under `Intents` — gitups skips
kubectl apply and relies on the SRC CLI to apply its manifest.

## What gitups does NOT do

- **Does not define per-integration CRDs.** Retired: the old
  `http-proxy/v1alpha1` + `HttpProxyBackend` scheme — declarest does
  not use product-specific CRDs.
- **Does not project per-interface resources.** Retired: the
  `Provision.spec.repositories[].interfaceResources[]` synthesis
  pipeline. Users compose declarest's four CRs explicitly, one per
  scope, in a normal `kubernetes-resources` repo.
- **Does not fetch bundles at render time.** `spec.declarestBundle`
  on a package is advisory metadata; runtime fetch happens inside
  declarest when the SRC loads a `ManagedService`.
- **Does not manage bundle publication.** Bundles are shipped by the
  package (or its authors) as OCI artifacts published alongside the
  package release. Gitups-packages' release workflow may grow a
  per-package bundle publication hook, but gitups core stays out of
  that loop.

## Pointers

- [declarest/README.md](https://github.com/crmarques/declarest#readme)
  — project home.
- [declarest/docs/guide/core-concepts.md](https://github.com/crmarques/declarest/blob/main/docs/guide/core-concepts.md)
  — context, resources, metadata, secrets.
- [declarest/docs/reference/operator-crds.md](https://github.com/crmarques/declarest/blob/main/docs/reference/operator-crds.md)
  — CRD reference.
- [declarest/docs/guide/metadata-and-api-modeling.md](https://github.com/crmarques/declarest/blob/main/docs/guide/metadata-and-api-modeling.md)
  — bundle authoring.
- [capabilities.md](capabilities.md) — gitups capability-binding
  vocabulary (the feature that is still live; interface-projection is
  retired).
