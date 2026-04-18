# Package Authoring

Use when creating or reviewing packages in the sibling `gitups-packages/`
catalog.

## Rules

- Directory name must match root `package.yaml` `metadata.name`.
- Keep `metadata.version: 0.0.1` unless maintainers explicitly start release
  versioning.
- One top-level package represents one service/application. Do not split
  `argocd-operator`, `argocd-instance`, or `metallb-config` style packages
  when the service package can own install methods and resources.
- Prefer OLM -> Kustomize -> Helm -> raw for installs. Offer multiple install
  methods only when the user genuinely has a choice; pick `defaultInstall`
  with the same preference order.
- Put user-tunable settings in descriptor `inputs[]`. Use
  `placeholder: true` plus `placeholderReason` when the user must fill a
  value.
- For explicit image references, expose the image repository and tag or digest
  as separate inputs and compose the final reference in the template. Do not
  default any image tag to `latest`; when upstream ships a mutable latest tag,
  resolve it to an immutable digest before adding it to the catalog.
- Keep templates thin. Prefer resolved values over optional template logic.
- Package descriptors do not declare output repositories. Repository placement
  belongs only in `Provision.spec.repositories[]`.
- Package README files should list non-obvious inputs, placeholders, install
  methods, resource templates, and any reason for not using the preferred
  install method.
- When a package fulfills a capability, declare `spec.provides[]`; when it
  consumes one, declare `spec.requires[]`. The declared `resourceTemplate`
  must exist under `resources/`.
- **Operator-CRD-first for provide/require resource templates.** If the
  package's operator exposes a CR for the capability's object (argocd
  consumes a labeled `Secret` for repo connections; gitea-operator has a
  user/repo CRD when pinned), the resource template MUST emit that CR via
  `renderer: raw` + overlays (or kustomize). Script-style is the fallback
  for packages without a stable operator CRD at pin time.
- **Script fallback is gitups-wrapped.** Set `spec.provisioningStyle:
  script` + pin `spec.scriptImage` (never `:latest`) and ship
  `scripts/provision.sh`. Gitups owns the Job shape, the ConfigMap
  layout, and the `/etc/gitups/values.json` mount — package authors only
  write the script body. Use `GITUPS_VALUES_FILE`, `GITUPS_INSTANCE`,
  and `GITUPS_NAMESPACE` inside the script.
- Declare `spec.readiness[]` on descriptors whose on-cluster object must
  become `Available`/`Ready` before dependent units apply.
  `name`/`namespace` may template `.Values`; gitups never reads the
  cluster during render.

## Layout

```text
<name>/
  package.yaml
  README.md
  install/
    <renderer>/                 # renderer is olm|kustomize|helm|raw
      descriptor.yaml
      values.yaml.tmpl          # for helm
      base/                     # for kustomize
      raw/                      # for raw (files + *.yaml.tmpl)
      overlays/
      scripts/
  resources/
    <resourceTemplate>/
      descriptor.yaml
      raw/
      overlays/
      scripts/
```

Install assets resolve relative to `install/<renderer>/`. Resource assets
resolve relative to `resources/<resourceTemplate>/`.

Renderer behaviour:

- `olm` emits `operator-group.yaml` and `subscription.yaml`.
- `helm` renders `values.yaml` then `install.yaml` via `helm template`.
- `kustomize` runs `kustomize build <base>` into `install.yaml`; optional
  values templates are written as review aids.
- `raw` copies files from the descriptor's `raw/` dir; `.tmpl` files are
  rendered and the suffix is stripped.
- `overlays/*.tmpl` render after the main renderer and become sibling YAML
  files in the unit output dir.

## Patterns

Root package metadata:

```yaml
apiVersion: gitups/v1alpha1
kind: PackageDefinition
metadata: {name: metallb, version: 0.0.1}
spec:
  role: workload
  category: network-lb
  defaultInstall: olm
  defaultResources:
    - template: config
      name: default
```

Install descriptor:

```yaml
apiVersion: gitups/v1alpha1
kind: PackageDescriptor
metadata: {name: helm}
spec:
  renderer: helm
  inputs:
    - name: namespace
      type: string
      default: metallb-system
  helm:
    repo: https://metallb.github.io/metallb
    chart: metallb
    version: 0.14.8
    valuesTemplate: values.yaml.tmpl
```

Resource descriptor:

```yaml
apiVersion: gitups/v1alpha1
kind: PackageDescriptor
metadata: {name: config}
spec:
  renderer: raw
  inputs:
    - name: addressPools
      type: list
      default:
        - name: default
          cidrs: [__GITUPS_PLACEHOLDER__]
      placeholderReason: "LB CIDR range is site-specific"
  dependsOn: [metallb/install]
```

Namespace overlay:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Values.namespace }}
```

Capability provider with operator-CRD resource template:

```yaml
# package.yaml
spec:
  provides:
    - capability: git-provider
      resourceTemplate: user
      exports:
        - {name: url,      from: input:rootURL}
        - {name: username, from: binding:username}
        - {name: token,    from: placeholder, secret: true}
```

Script-fallback provider (no operator CRD at pin time):

```yaml
# resources/user/descriptor.yaml
spec:
  renderer: raw
  provisioningStyle: script
  scriptImage: alpine:3.20.3       # pinned; never :latest
  inputs:
    - {name: namespace, type: string, default: gitea}
    - {name: url,       type: string, required: true}
    - {name: username,  type: string, required: true}
    - {name: token,     type: string, required: true, sensitive: true}
  dependsOn: [gitea/install]
  readiness:
    - kind: Deployment
      namespace: '{{ .Values.namespace }}'
      name: gitea
      condition: Available
```

```sh
# resources/user/scripts/provision.sh (package-authored; gitups wraps in a Job)
#!/bin/sh
set -eu
VALUES="$GITUPS_VALUES_FILE"
# ... use jq / curl against the service API
```

## Gotchas

- `dependsOn` uses install/resource aliases such as `olm/install`,
  `metallb/install/helm`, `argocd/resources/instance`, or
  `argocd/resources/applications/root`.
- `Provision.spec.repositories[*].packages[*].role` overrides package role for
  every resolved unit of that package ref.
- Template context is `.Values`, `.Instance`, `.PackageInstance`, `.UnitType`,
  `.ResourceTemplate`, `.ResourceName`, `.Env`, and `.Context`.
- Hooks may write only into `--out`.
- Helm `valuesTemplate` is relative to the install/resource descriptor dir.
- A raw descriptor may rely only on overlays and omit `raw/`.

Pointers: [schemas.md](schemas.md), [repo-layout.md](repo-layout.md),
[olm.md](olm.md), [helm.md](helm.md),
[internal/catalog/catalog.go](../../internal/catalog/catalog.go)
