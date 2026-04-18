# OLM Rendering

Use when touching `renderer: olm` packages, OLM waits, or
`internal/render/olm.go`.

## Rules

- Prefer OLM when a pinned catalog entry exists.
- `spec.olm.startingCSV` is required and exact.
- A catalog entry is only acceptable when the CSV's install images are pinned
  too. If the exact CSV carries floating images such as `main` or `latest`, do
  not use it for e2e bootstrap; document the issue and choose the next renderer
  in the priority order for that scenario.
- `installPlanApproval` defaults to `Automatic`; use `Manual` only with a
  README reason.
- OLM packages emit `operator-group.yaml` and `subscription.yaml`; namespace
  should come from the shared overlay pattern.
- `operatorGroupScope` defaults to `OwnNamespace`. Use `AllNamespaces` only
  when the CSV supports it and cluster-wide visibility is intended.
- Per-instance overrides live under `resolvedValues.olm`.
- OLM itself is `renderer: raw`; bootstrap install descriptors cannot install
  themselves through OLM.
- Do not hand-author Subscription YAML in another renderer when `renderer: olm`
  can express it.

## Descriptor Block

```yaml
renderer: olm
olm:
  package: metallb-operator
  channel: stable
  source: operatorhubio-catalog
  sourceNamespace: olm
  startingCSV: metallb-operator.v0.14.9
  installPlanApproval: Automatic
  operatorGroupScope: OwnNamespace
```

Allowed per-instance overrides:

```yaml
values:
  olm:
    channel: beta
    source: custom-catalog
    sourceNamespace: olm
    startingCSV: metallb-operator.v0.14.0
    installPlanApproval: Manual
    operatorGroupScope: OwnNamespace
```

`package` stays fixed in the descriptor; it identifies the operator.

## Rendered Files

```
packages/<packageInstance>/install/olm/
  namespace.yaml
  operator-group.yaml
  subscription.yaml
  kustomization.yaml
```

Operator CRs should usually be resource descriptors in env repos with
`dependsOn: [<service>/install]`.

## Waits

`apply --wait-crds` waits after each repo for OLM subscriptions rendered by
that repo. `gitups wait` polls all OLM subscriptions in the `FullProvision`.
Both wait for CSV phase `Succeeded`, fail on `Failed`, and use kubectl reads.

## Gotchas

- Private catalogs need their own `CatalogSource`, usually a raw package that
  OLM packages depend on.
- `AllNamespaces` can propagate CSVs broadly; check CSV install modes first.
- Argo CD operator output is small compared with the Helm chart because OLM
  reconciles the control plane after the Subscription is applied.

Pointers: [internal/render/olm.go](../../internal/render/olm.go),
[internal/cluster](../../internal/cluster), [package-authoring.md](package-authoring.md)
