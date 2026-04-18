# Output Repo Layout

Use when changing `internal/render/render.go`, repository resolution, pruning,
or generated kustomization files.

## Rules

- Workspace is `<output-dir>/<name>/`.
- `provision.yaml` and `full-provision.yaml` live beside rendered repo dirs.
- Each substituted `Provision.spec.repositories[].name` becomes one top-level
  repo dir if it has resolved units.
- Repository placement is decided only by `Provision.spec.repositories[]`.
  Package descriptors must not declare `targetRepo`.
- Generic repos (`type: k8s-gitops-generic`) render package installs from
  `repositories[].packages[]`.
- Env repos (`type: k8s-gitops-env`) render resources. With `repoRef` and no
  explicit packages, they derive each referenced generic package's
  `defaultResources`.
- `renderedPaths.dir` is `packages/<packageInstance>/install/<installMethod>`
  for installs and
  `packages/<packageInstance>/resources/<resourceTemplate>/<resourceName>` for
  resources.
- `spec.repository.layout: split` is the only supported layout.
- Per-repo and intermediate `kustomization.yaml` files list child dirs.
- Leaf unit `kustomization.yaml` files list YAML files except `values.yaml` and
  `kustomization.yaml`.
- Workspace CLI rendering sets `SuppressFullProvision=true` and
  `PreserveExtras=true`. `--prune` removes orphan repo dirs, not metadata
  files.

## Tree

```text
<output-dir>/
  <name>/
    provision.yaml
    full-provision.yaml
    <repo>/
      README.md
      kustomization.yaml
      packages/<packageInstance>/
        kustomization.yaml
        install/
          kustomization.yaml
          <method>/
            namespace.yaml
            install.yaml
            subscription.yaml
            operator-group.yaml
            values.yaml
            kustomization.yaml
        resources/
          kustomization.yaml
          <resourceTemplate>/
            kustomization.yaml
            <resourceName>/
              kustomization.yaml
              *.yaml
```

## Repository Names

`{{.Env}}` is the only recognized token and is replaced at expand time.
It binds to `Provision.spec.envKey` when set, falling back to
`Provision.metadata.name`:

| Repository name                 | `envKey=dev` renders as       | No `envKey`, `metadata.name=dsv` |
| ------------------------------- | ----------------------------- | -------------------------------- |
| `basic-infra`                   | `basic-infra`                 | `basic-infra`                    |
| `basic-infra-{{.Env}}`          | `basic-infra-dev`             | `basic-infra-dsv`                |
| `gitops-controllers-{{.Env}}`   | `gitops-controllers-dev`      | `gitops-controllers-dsv`         |

## Gotchas

- Large `install.yaml` files are normal for big Helm charts and raw CRD
  bundles. A sudden large shrink usually means CRDs were dropped.
- If env is `dsv` and a repository is literally named `dsv`, output is
  `<root>/dsv/dsv/`. That is accepted.
- Top-level README content is generated from `FullProvision` and package
  metadata; do not put durable authoring guidance there.
- `repoRef.commit` is persisted as provenance only today; Gitups does not fetch
  remote repository content from it.

Pointers: [internal/render/render.go](../../internal/render/render.go),
[internal/resolve/resolve.go](../../internal/resolve/resolve.go),
[schemas.md](schemas.md)
