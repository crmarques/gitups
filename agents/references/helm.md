# Helm Rendering

Use when touching `renderer: helm` packages or `internal/render/helm.go`.

## Rules

- Pin `spec.helm.version` exactly. No `latest`, ranges, or implicit upgrades.
- Use Helm only when no suitable operator/OLM path exists. Document the reason
  in the package README.
- Rendered `values.yaml` is both review artifact and the exact `-f` input to
  `helm template`.
- Every invocation includes `--include-crds`.
- Helm is required at `generate`/`status`, not `expand`.
- Tests stub `HelmRunner`; unit/golden tests must not call real Helm or chart
  registries.

## Invocation

```sh
helm template <instance> <chart> \
  --repo <repo> \
  --version <version> \
  --namespace <resolvedValues.namespace-or-default> \
  --include-crds \
  -f <rendered-values.yaml>
```

Minimum block:

```yaml
renderer: helm
helm:
  repo: https://argoproj.github.io/argo-helm
  chart: argo-cd
  version: 7.6.12
  valuesTemplate: helm/values.yaml.tmpl
```

## Templates

- Context: `.Values`, `.Instance`, `.Env`, `.Context`.
- Helpers: `toYaml`, `indent`, `nindent`, `default`.
- Keep templates to chart overrides. Shape values in `inputs[]` and
  `resolvedValues`, not with heavy template logic.
- Do not override namespace in the values template; `resolvedValues.namespace`
  should drive both namespace overlay and Helm `--namespace`.

## Gotchas

- Large `install.yaml` output is expected for charts with CRDs.
- `helm repo add` and `helm repo update` are not used; `--repo` is the only
  render-time network path.
- If a `chart.version` input exists, render uses the resolved input value for
  `helm template --version`; keep its default in sync with `spec.helm.version`,
  which remains the exact descriptor fallback.

Pointers: [internal/render/helm.go](../../internal/render/helm.go),
[internal/render/overlays.go](../../internal/render/overlays.go),
[package-authoring.md](package-authoring.md)
