# CLI UX

Use when adding commands, changing flags, or changing stdout/stderr behavior.

## Rules

- Workspace is always `<output-dir>/<name>/`; default output dir is
  `./gitups-output-dir`. The CLI owns `provision.yaml`, `full-provision.yaml`,
  and rendered repo siblings there.
- `<name>` is positional. Do not add ad hoc `-f path/to/file.yaml` flags.
- Command flow stays explicit: `init` -> `expand` -> edit `FullProvision` ->
  `check` -> `generate` -> `apply`. `status` reports drift; `wait` polls OLM.
- `metadata.name` in YAML must equal the workspace `<name>`.
- Placeholders are OK on `expand`, `check`, and `status`; fatal on `generate`
  and `apply` unless `--allow-placeholders` is set.
- `apply` is the only mutating cluster command. `wait` is read-only but still
  shells to kubectl. `init`, `expand`, `check`, `generate`, and `status` do not
  contact a cluster.
- Summaries go to stderr. Return errors from `RunE`; root `SilenceUsage` and
  `SilenceErrors` ensure errors print once.

## Commands

| Command | Behavior |
| --- | --- |
| `init <name> [-d dir] [--force]` | Writes scaffold `provision.yaml`; refuses overwrite unless forced. |
| `expand <name> [-d dir] [--force]` | Resolves catalog into `full-provision.yaml`; preserves user fills unless forced. |
| `check <name> [-d dir]` | Validates schema, dry-expands catalog/resolve, and validates existing `FullProvision`. |
| `generate <name> [-d dir] [--context c] [--allow-placeholders] [--prune]` | Renders repo dirs from `FullProvision`; `--context` is a label for templates, not cluster access. |
| `apply <name> --to <ctx> [-d dir] [--dry-run] [--allow-placeholders] [--wait-crds] [--wait-timeout 10m]` | Runs `kubectl --context <ctx> apply -k` for each rendered repo in dependency order. |
| `wait <name> --to <ctx> [-d dir] [--timeout 10m]` | Polls OLM subscriptions until CSVs reach `Succeeded`; fails on `Failed` or timeout. |
| `status <name> [-d dir]` | Renders to scratch and diffs against the workspace; exits non-zero on drift. |

## Gotchas

- Source paths resolve relative to `provision.yaml`, not `$PWD`.
- `spec.extends.source` on an env Provision is also relative to the env
  `provision.yaml`. `expand` and `check` call `load.ProvisionResolved` which
  merges the base into memory; the on-disk env file is never rewritten.
- Each workspace is still one env: `gitups expand basic-infra-dev` targets
  `basic-infra-dev/provision.yaml` whether or not it `extends` another.
- `generate` uses `SuppressFullProvision=true` and `PreserveExtras=true` so it
  replaces rendered repo dirs without clobbering workspace metadata files.
- `--prune` removes orphan top-level repo dirs only; metadata files stay.
- `generate` and `status` require `helm` and `kustomize`. `apply` and `wait`
  require `kubectl`.
- `apply` uses server-side apply with `--force-conflicts` and retries once
  after CRD establishment unless `--dry-run` is set.
- `--context` is a render-time template value. `--to` is the kubectl target.
- `--to <ctx>` is strict: it uses exactly the kubectl context string supplied
  by the user.

Pointers: [cmd/gitups/main.go](../../cmd/gitups/main.go),
[repo-layout.md](repo-layout.md), [schemas.md](schemas.md), [olm.md](olm.md)
