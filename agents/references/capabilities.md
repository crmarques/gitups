# Capability Bindings

AGENTS.md §2 names **capability bindings** as the single open
extension point for wiring packages in a Provision:
`spec.provides[]` / `spec.requires[]` on a package, paired with one
`resourceTemplate` on each side. Shape is enforced by
[catalog.go](../../internal/catalog/catalog.go) and resolved by
[bindings.go](../../internal/resolve/bindings.go); see
[schemas.md](schemas.md#capability-bindings) for the field-level
contract.

This file holds the **vocabulary**: which capability names exist
today, which are planned, and how to add new ones. Keep it current
when a package adds, renames, or retires a provide/require entry.

Service-level reconciliation (what used to be called "service-config
interfaces") is **not** handled by gitups core capabilities — it is
handled externally by declarest. See [declarest.md](declarest.md) for
the model and the migration away from the retired
`implements[]` / `bundles[]` / `interfaceResources[]` machinery.

## Capability bindings

Capabilities are per-resource contracts: a provider package exposes a
capability through a resource template (e.g., gitea's `user` resource
represents a usable git-provider); a consumer package requests the
same capability through its own resource template (e.g., argocd's
`repo-secret` resource holds the provider's credentials).

| capability       | provided by | required by           | typical exports                                        |
| ---------------- | ----------- | --------------------- | ------------------------------------------------------ |
| `git-provider`   | gitea       | argocd, declarest     | `url`, `username`, `token` (secret)                    |
| `lb-ip-pool`     | metallb     | nginx-ingress, haproxy| `poolName`, `cidrs`                                    |
| `secret-store`   | vault       | (declarest, via explicit resource selection) | `address`, `tokenSecretRef` (secret) |

### Category → capability map

Use this as a shorthand when adding a new package: a package in a
listed category usually needs (or offers) the corresponding
capability. Add a row when a new category coins a capability name.

| `metadata.spec.category` | typical capability role    | capability name |
| ------------------------ | -------------------------- | --------------- |
| `network-lb`             | provider                   | `lb-ip-pool`    |
| `ingress`                | consumer (of `lb-ip-pool`) | `lb-ip-pool`    |
| `source-control`         | provider                   | `git-provider`  |
| `gitops-controller`      | consumer                   | `git-provider`  |
| `secrets`                | provider                   | `secret-store`  |
| `identity`               | provider (future)          | `oidc-provider` (unwired) |

Two rules keep this list small:

- **Coin a new capability only when a second consumer is imminent.**
  If a contract has exactly one provider and one consumer that ship
  together, name it but expect churn; if a third package joins, the
  name is earning its keep.
- **Stay per-resource, not per-package.** A capability binds one
  provider resource template to one consumer resource template. Two
  capabilities that share a provider but differ in the resource
  template (e.g., gitea's `user` → argocd's `repo-secret`, gitea's
  `repo` → argocd's `app-source`) stay separate — don't overload one
  name with conditional exports.

### `secret-store` note

`secret-store` is a normal capability binding but has one subtlety:
its consumer side is declarest's `resources/secret-store/` template
(which renders a declarest `SecretStore` CR) — not a per-reconciler
custom CR. See [declarest.md](declarest.md) for how the rendered
SecretStore is named and referenced from declarest `SyncPolicy` CRs.

## Naming rules

- **kebab-case**, ASCII only. Example: `git-provider`, not
  `gitProvider` or `git_provider`.
- **Noun-based contract**, not implementing tool. Prefer
  `secret-store` over `vault`; prefer `git-provider` over `gitea`.
- **One concept per name.** Split into two names before overloading
  semantics.
- **Capability names are unversioned.** If a capability's shape
  changes incompatibly, coin a new name.

## Adding a new capability

1. Pick a kebab-case noun that names the contract, not the tool.
2. Add a row to the capability table above with a one-line description
   of the expected exports.
3. On the provider package, under `spec.provides[]`, declare
   `capability`, `resourceTemplate`, and `exports[]`. The resource
   template must exist under `resources/<name>/descriptor.yaml`.
4. On each consumer package, under `spec.requires[]`, declare
   `capability`, `resourceTemplate`, and `consumes[]`. The consumer's
   `resourceTemplate` must exist too.
5. Wire an example binding in a Provision under `tests/e2e/` so the
   path is exercised by a concrete case.

No Go code change is required at any step.

## Compatibility

- A capability's exported keys and a consumer's consumed inputs form
  a stable contract. Adding a new export is safe; renaming or
  removing one breaks every consumer. Coin a new capability name
  instead.
- Do not reuse a capability name for a different contract, even
  after all users have migrated away. Retired names stay retired.
