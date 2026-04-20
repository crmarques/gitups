# AGENTS.md - Gitups

Read before any change. This is the stable contract between maintainers and
coding agents; task scope still comes from the user's current request.

## 1. What Gitups Is

Gitups is a Go CLI that turns a declarative user intent into a *composition*
of packages — the git-shaped directories they live in, the installs they
perform, and the wiring between them. One YAML (`Provision`) names the
repos, which packages land where, and how packages depend on or configure
one another. Gitups expands that intent into `FullProvision`, renders one
directory tree per logical repo, and can bootstrap a target cluster from
those rendered trees (kubectl for K8s manifests, SRC CLI for
service-level configuration). Pushing the rendered trees to a git
provider is first-class via `gitups push`, which reuses the user's
configured git credentials by default and accepts per-invocation
overrides for token, username, base URL, and repo-name flattening.

It is scoped to generation, publication (push), and bootstrap apply. It
is not a controller and does not replace Argo CD, Flux, Declarest, Helm,
Kustomize, or OLM — it orchestrates them.

## 2. Non-Negotiable Invariants

- **Two-stage flow.** `Provision` (user-authored) -> `FullProvision`
  (expanded, reviewable, placeholder-bearing) -> rendered repo dirs. Render
  reads only `FullProvision`, never `Provision`.
- **Lean `Provision`.** Keep only sources and selected templates there.
  Defaults and inferable fields belong in `package.yaml`.
- **Install vs resources.** A package is one service/application. It declares
  install methods under `install/<renderer>/descriptor.yaml` and optional
  custom resources under `resources/<resourceTemplate>/descriptor.yaml`.
  Generic repositories select installs; env repositories select or derive
  resources. Each selected unit expands to one `FullProvision` package carrying
  `unitType: install|resource`, plus `installMethod` or
  `resourceTemplate`/`resourceName`.
- **`FullProvision` is authoritative.** Re-expand is idempotent and preserves
  user edits. The placeholder sentinel is `__GITUPS_PLACEHOLDER__`
  (`v1.PlaceholderSentinel`); do not change it.
- **No target-cluster metadata in schemas.** `Provision` and `FullProvision`
  must work for multiple clusters. CLI flags supply runtime context:
  `--context` labels generated overlays; `--to` selects a kubectl context.
- **Renderer priority is OLM -> Kustomize -> Helm -> raw.** Apply it when
  choosing an install method and when choosing a renderer for resource
  descriptors: OLM when a catalog entry exists, Kustomize for CRs/overlays/env
  config, Helm when no operator exists, and raw only for bootstrap/escape-hatch
  manifests.
- **Repositories are user intent.** `Provision.spec.repositories[]` is the only
  source of output repo placement. Package descriptors do not declare
  `targetRepo`. `{{.Env}}` is substituted in repository names from
  `spec.envKey`, falling back to `metadata.name`. Env repos with `repoRef` and
  no explicit packages derive each referenced generic package's
  `defaultResources`.
- **Controllers are first-class.** Every package declares
  `role: kubernetes-resource-controller | service-resource-controller | workload`.
  A KRC (ArgoCD, Flux) reconciles git → cluster manifests and carries a
  `kubernetes-resource-controller/` domain directory. An SRC (Declarest,
  Crossplane) reconciles git → managed-service state and carries a
  `service-resource-controller/` domain directory plus `spec.cli`. Other
  packages are `workload`. `Provision.spec.controllers.{kubernetesResources,serviceResources}`
  assigns the KRC/SRC roles to already-selected package instances via
  `{repo, instance}` — same reference shape as `bindings[].provider`.
  Full design in [agents/plans/krc-src-first-class-controllers.md](agents/plans/krc-src-first-class-controllers.md).
- **Prefer KRC over SRC.** When a package's desired state can be
  expressed as K8s CRDs/CRs, route it through the KRC: kubectl +
  GitOps-controller reconciliation is the simpler, more auditable path
  and keeps gitups output pure K8s YAML. Reach for SRC only when the
  target has no stable CR (service-level admin APIs, external SaaS,
  configuration that needs a controller-specific bundle). Same spirit
  as the renderer priority above.
- **Capabilities are open extension points.** Packages declare
  `provides[]` / `requires[]` using free-form capability name strings
  paired with one `resourceTemplate` on each side. Gitups core
  enforces *shape* (non-empty fields, unique names, consistent
  provider/consumer wiring) but does not hold a closed list of names
  — new capabilities can be coined by convention as new packages
  ship, without touching gitups core code.
- **Service-level reconciliation is declarest.** When a package
  exposes a REST API that should be kept in sync from Git, it ships a
  declarest metadata bundle and declares
  `spec.declarestBundle: {name, version, ref?}` in `package.yaml`.
  Declarest (SRC) is the single canonical reconciler; it uses four
  generic CRDs (`ResourceRepository`, `ManagedService`,
  `SecretStore`, `SyncPolicy`) — **no** per-integration CRDs. Full
  model, including how a Provision composes declarest's four CR
  templates, is in
  [agents/references/declarest.md](agents/references/declarest.md).
  The retired `spec.implements[]` / `spec.bundles[]` /
  `Provision.spec.repositories[].interfaceResources[]` machinery and
  the `service-resources` repo type are gone.
- **Bootstrap-sync SRC intents.** An SRC may declare
  `spec.cli.intents[<resourceTemplate>].args` in `package.yaml`.
  Resource units whose `resourceTemplate` matches a key are tagged
  at resolve time as SRC-owned with that intent; at apply time
  gitups runs `kubectl apply` first (so the operator can later
  assume control) and then invokes the SRC CLI with the intent's
  args to drive one immediate reconciliation. The older CLI-only
  invocation (`spec.cli.args`) is still used for the
  `managed-script` intent. Declarest ships a `sync-policy` intent
  by default.
- **Hook ABI is stable.** Call hooks as
  `<script> --phase <phase> --values <json-path> --out <render-dir>`. Hooks
  may write only inside `--out`.
- **Generation is deterministic and hermetic.** Same inputs produce identical
  bytes. No timestamps, random IDs, or network calls during render except
  pinned `helm template --repo` chart pulls.
- **Pin everything.** Chart versions, `startingCSV`, catalog sources, image
  tags, and upstream URLs must be exact. Never use `latest` or floating ranges.
- **Cluster access is explicit.** `apply` is the only mutating cluster command.
  `wait` may read cluster state via kubectl. `init`, `expand`, `check`,
  `generate`, `push`, and `status` are cluster-agnostic.
- **Push is the only outbound mutating command.** `push` talks to a git
  provider (GitHub, GitLab, Gitea, …) to create missing repos and
  publish rendered trees. It uses the user's existing git credential
  helpers by default; CLI flags (`--token`, `--user`) override per
  invocation. `--base-url` is required (e.g. `https://github.com/myorg`
  or `https://gitea.example.com/myteam`) so there is no implicit
  provider host. `--flatten` replaces `/` with `-` in rendered repo
  names for providers that do not support subgroups. Secrets are read
  from flags/env only — never read from or written to `Provision`,
  `FullProvision`, or rendered repo trees.

## 3. Workspace Layout

```
gitops-workspace/
  gitups/                  # this repo: CLI, specs, agent docs
    AGENTS.md              # authoritative agent contract
    CLAUDE.md              # pointer for Claude-style agents
    agents/
      references/          # long-lived domain rules
      plans/               # user-requested markdown plans; read only when cited
    bin/                   # ignored; make build writes bin/gitups
    tmp/                   # ignored scratch
    cmd/ internal/ api/ ...
  gitups-packages/         # sibling package catalog, not a subdir
    packages/               # every package lives here; released independently
      <package-name>/
        package.yaml
        install/
          <renderer>/       # olm|kustomize|helm|raw
            descriptor.yaml
            raw/ | overlays/ | scripts/
        resources/
          <resourceTemplate>/
            descriptor.yaml
            raw/ | overlays/ | scripts/
    schemas/ tools/ docs/   # shared infra; see gitups-packages/README.md
```

Filesystem package sources are resolved relative to the provision file, usually
with a path such as `../../../gitups-packages/packages`. The `packages/`
segment is required — releases are driven per-package by git tags of the form
`pkg/<name>/v<version>` and published under
`ghcr.io/<owner>/gitups-packages/<name>:<version>`.

## 4. Instruction Files

- `AGENTS.md` holds invariants and repo-wide working rules.
- `agents/references/<domain>.md` holds durable domain rules. Read the files
  for the domains you touch before coding.
- `agents/references/README.md` is the index; update it when reference files
  are added, renamed, or retired.
- When the user asks for help thinking through or defining a plan, save the
  resulting plan as a Markdown file under `agents/plans/` with a concise
  kebab-case filename tied to the request.
- Never read `agents/plans/*` unless the user cites a specific path in the
  current request.

Update a reference when the user gives durable guidance: "always", "never",
"prefer X", a design decision with a reason, or a corrected past mistake. Do
not store task state or milestone notes there. Rules should say what to do,
why, and when they apply.

## 5. Agent Workflow

Before coding:

1. Read this file, especially section 2.
2. Read matching reference files.
3. Use the user's request as scope; ask if scope is ambiguous.
4. Stop and ask if the request conflicts with an invariant. Do not silently
   soften, work around, or proceed past an invariant. Name the specific
   clause(s) in §2 or the reference files that the request would violate,
   explain the conflict concretely, and ask the user whether to (a) revise
   the request to stay within the contract, or (b) amend the clause and
   proceed. If the user chooses (b), update this file or the relevant
   reference file in the same change that implements the request, and note
   the amendment in the PR/commit message so future agents see the new
   rule.

While coding:

- Prefer existing files, patterns, helpers, and ownership boundaries.
- Schema breaks are allowed while the API is `gitups/v1alpha1`; update the
  schemas reference and all fixtures/tests in the same change. Bump
  `apiVersion` when the API stabilises.
- Keep render output deterministic: sort maps/slices where order can vary, pin
  inputs, render to temp dirs, then swap into place.
- Use repo-local `tmp/` for scratch only.
- Build with `make build`; run `./bin/gitups`. Do not leave root-level
  binaries.
- Check required external binaries before shelling to `helm`, `kustomize`, or
  `kubectl`, and return clear errors.
- Stub `HelmRunner`, `KustomizeRunner`, and OLM/cluster behavior in tests.
  Unit and golden tests must not hit real registries or clusters.

After coding:

- Update relevant references if the task created durable guidance.
- Edit this file only for maintainer-approved invariant or workflow changes.
- Flag any reference/code mismatch you corrected.
- For a successful standard request handoff, output only one recommended commit
  subject line using Conventional Commits:
  `<type>(<scope>): <description>`. Use a scope when obvious
  (package/module/folder). Keep it short, with a maximum of 72 characters. Do
  not append summaries, file lists, verification details, or commit questions.
  If request processing is blocked or required verification cannot complete,
  report the blocker instead.
- Allowed commit types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`,
  `build`, `ci`, `chore`, `revert`.
- Examples: `docs(agents): shorten standard handoff`; `Blocked: go test -race
  ./... could not complete`.

## 6. Tooling Conventions

- Language: Go.
- YAML: 2-space indent, no tabs, `apiVersion` and `kind` at the top.
- Filenames: kebab-case YAML/package dirs; snake_case Go files.
- External tools: render may call `helm` and `kustomize`; `apply`/`wait` may
  call `kubectl`; `push` shells to `git` and makes provider REST calls
  (GitHub, GitLab, Gitea).
- Validation: use focused unit tests for narrow changes; use golden tests under
  `testdata/golden/` for rendered output changes; use `make check` for Go or
  build changes.
- Commits: one logical change, explain why, never use `--no-verify` unless the
  user asks.

## 7. Anti-Goals

- Not a Kubernetes controller; it does not watch or reconcile in-cluster.
- Not a Helm/Kustomize/OLM replacement; it orchestrates them.
- Not a secrets manager; secrets surface as placeholders. `gitups apply
  --generate-secrets` may fill placeholders whose input declared a
  `generator` block, writing the result back into FullProvision in
  place. Generation happens at apply time only — never during render —
  so render stays deterministic and hermetic.
- Not a general-purpose templating engine; Go templates stay limited to small
  values and overlay wrappers.

Flag requests that push Gitups toward these anti-goals before implementing.
