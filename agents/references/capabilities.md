# Capability & Interface Vocabulary

AGENTS.md §2 names two open extension points that let new wiring ship
without gitups core changes:

- **Capability bindings** — `spec.provides[]` / `spec.requires[]` on a
  package, paired with one `resourceTemplate` on each side. Shape is
  enforced by [catalog.go](../../internal/catalog/catalog.go) and resolved
  by [bindings.go](../../internal/resolve/bindings.go); see
  [schemas.md](schemas.md#capability-bindings) for the field-level contract.
- **Service-config interfaces** — `spec.implements[]` (any role) and
  `spec.bundles[]` (SRC only). Shape is enforced by
  [load.go](../../internal/load/load.go) (`validateImplements`,
  `validateBundles`); see [schemas.md:149-156](schemas.md#L149-L156).

This file holds the **vocabulary**: which names exist today, which are
planned, and how to add new ones. Keep it current when a package adds,
renames, or retires a provide/require/implements/bundles entry.

## Capability bindings

Capabilities are per-resource contracts: a provider package exposes a
capability through a resource template (e.g., gitea's `user` resource
represents a usable git-provider); a consumer package requests the same
capability through its own resource template (e.g., argocd's `repo-secret`
resource holds the provider's credentials).

| capability       | provided by | required by           | typical exports                                |
| ---------------- | ----------- | --------------------- | ---------------------------------------------- |
| `git-provider`   | gitea       | argocd, declarest     | `url`, `username`, `token` (secret)            |
| `lb-ip-pool`     | metallb     | nginx-ingress, haproxy | `poolName`, `cidrs`                           |
| `secret-store`   | *(planned — vault)* | *(planned — declarest)* | `address`, `token` (secret), `mountPath` |

Planned entries are not yet wired; do not cite them as existing contracts.
Track their progress under
[agents/plans/](../plans/) if they gain formal plans.

### Category → capability map

Use this as a shorthand when adding a new package: a package in a listed
category usually needs (or offers) the corresponding capability. Add a
row when a new category coins a capability name.

| `metadata.spec.category` | typical capability role    | capability name |
| ------------------------ | -------------------------- | --------------- |
| `network-lb`             | provider                   | `lb-ip-pool`    |
| `ingress`                | consumer (of `lb-ip-pool`) | `lb-ip-pool`    |
| `source-control`         | provider                   | `git-provider`  |
| `gitops-controller`      | consumer                   | `git-provider`  |
| `secrets`                | provider (planned)         | `secret-store`  |
| `gitops-controller`      | consumer (planned)         | `secret-store`  |
| `identity`               | provider (future)          | `oidc-provider` (unwired) |

Two rules keep this list small:

- **Coin a new capability only when a second consumer is imminent.** If a
  contract has exactly one provider and one consumer that ship together,
  name it but expect churn; if a third package joins, the name is earning
  its keep.
- **Stay per-resource, not per-package.** A capability binds one provider
  resource template to one consumer resource template. Two capabilities
  that share a provider but differ in the resource template (e.g., gitea's
  `user` → argocd's `repo-secret`, gitea's `repo` → argocd's `app-source`)
  stay separate — don't overload one name with conditional exports.

## Service-config interfaces

Interfaces are per-package contracts reconciled by an SRC at runtime.
The consumer package declares `implements[]`; the SRC declares
`bundles[]` for every interface it can reconcile. Expand fails if any
`implements` entry has no matching `bundles` entry on the selected SRC.

| interface     | version    | kinds              | implemented by | bundled by        |
| ------------- | ---------- | ------------------ | -------------- | ----------------- |
| `http-proxy`  | `v1alpha1` | `HttpProxyBackend` | haproxy        | declarest         |

`http-proxy/v1alpha1` is wired end-to-end: haproxy's `package.yaml`
carries `spec.implements: [{interface: http-proxy, version: v1alpha1,
resourceTemplate: backend}]`; declarest's `spec.bundles[]` lists the
same interface; declarest's `service-resource-controller/managed-resource/`
directory opts the SRC in to passthrough reconciliation. At expand
time, every `Provision.spec.repositories[].interfaceResources[]` entry
targeting that pair synthesises one `HttpProxyBackend` CR in the
matching service-resources repo, rendered from haproxy's
`resources/backend/` template and routed through declarest at apply.

## Naming rules

Apply to both capability names and interface names unless noted.

- **kebab-case**, ASCII only. Example: `git-provider`, not `gitProvider`
  or `git_provider`.
- **Noun-based contract**, not implementing tool. Prefer `secret-store`
  over `vault`; prefer `git-provider` over `gitea`.
- **One concept per name.** Split into two names before overloading
  semantics.
- **Interfaces carry a version.** Capability names do not — if a
  capability's shape changes incompatibly, coin a new name. (Interfaces
  version via `spec.implements[].version` / `spec.bundles[].version`.)
- **Gitups-owned interface constants.** Hard-coded kinds like
  `InterfaceHTTPProxy` live next to the API in
  [types.go:75-82](../../api/v1alpha1/types.go#L75-L82). Coining a new
  interface that gitups core must introspect (routing, validation)
  requires adding a constant there; coining one that only package
  authors/SRC authors agree on does not.

## Adding a new capability

1. Pick a kebab-case noun that names the contract, not the tool.
2. Add a row to the capability table above with a one-line description
   of the expected exports. Mark `*(planned)*` until at least one
   provider ships.
3. On the provider package, under `spec.provides[]`, declare
   `capability`, `resourceTemplate`, and `exports[]`. The resource
   template must exist under `resources/<name>/descriptor.yaml`.
4. On each consumer package, under `spec.requires[]`, declare
   `capability`, `resourceTemplate`, and `consumes[]`. The consumer's
   `resourceTemplate` must exist too.
5. Wire an example binding in a Provision under `examples/` or
   `tests/e2e/` so the path is exercised by golden/e2e tests.
6. Update this file's table to drop the `*(planned)*` marker.

No Go code change is required at any step.

## Adding a new service-config interface

1. Agree the interface name + versioned kinds with the SRC author
   out-of-band.
2. If gitups core needs to know the name (routing, cross-validation
   beyond shape), add `Interface<Name>` and `Kind<Resource>` constants
   in [types.go:75-82](../../api/v1alpha1/types.go#L75-L82). Otherwise
   skip this step.
3. On the provider package (e.g. haproxy), add a `spec.implements[]`
   entry with `interface`, `version`, and an optional
   `resourceTemplate` naming the `resources/<name>/` directory whose
   descriptor renders the CR body. The resource template acts as the
   CR body shape; gitups core does not hold per-interface rendering
   code.
4. Add `spec.bundles[]` entries to the SRC package, pointing at the
   bundle image/artifact that reconciles the interface.
5. Ensure the SRC package has a
   `service-resource-controller/managed-resource/descriptor.yaml` so
   apply-time routing recognises the intent; gitups's projector tags
   every synthesised CR with this intent.
6. Add a row to the interfaces table above.

### How Provisions consume the interface

Users author consumption per-env inside the matching service-resources
repo declaration:

```yaml
- name: services-haproxy-{{.Env}}
  type: service-resources
  serviceRef:
    repo: support-services
    instance: haproxy
  interfaceResources:
    - name: gitea
      interface: http-proxy
      version: v1alpha1
      consumer:
        repo: support-services
        instance: gitea
      values:
        serviceName: gitea-http
        serviceNamespace: gitea
        servicePort: 3000
        host: gitea.dsv.local
```

`resolveInterfaceResources`
([internal/resolve/interface_resources.go](../../internal/resolve/interface_resources.go))
walks every such entry, confirms the provider's `implements[]` covers
`{interface, version}`, confirms the selected SRC's `bundles[]` does
too, resolves the provider's `resources/<resourceTemplate>/`
descriptor (defaulting to `<interface>-<version>` when the
`resourceTemplate` field is absent on both sides), and emits one
`ResolvedPackage` per entry tagged with the SRC as its controller.

## Compatibility

- A capability's exported keys and a consumer's consumed inputs form a
  stable contract. Adding a new export is safe; renaming or removing
  one breaks every consumer. Coin a new capability name instead.
- Interface versions are explicit (`v1alpha1`, `v1`). A new version is
  a new contract; SRC bundles must list every version they reconcile.
- Do not reuse a capability or interface name for a different contract,
  even after all users have migrated away. Retired names stay retired.
