# CLI UX

Use when adding commands, changing flags, or changing stdout/stderr behavior.

## Rules

- Workspace is always `<output-dir>/<name>/`; default output dir is
  `./gitups-output-dir`. The CLI owns `provision.yaml`, `full-provision.yaml`,
  and rendered repo siblings there.
- `<name>` is positional. Do not add ad hoc `-f path/to/file.yaml` flags.
- Command flow stays explicit: `init` -> `expand` -> edit `FullProvision`
  (or `fill` for `--set`-driven edits) -> `check` -> `plan` (preview,
  optional) -> `generate` -> `push` (optional, to a git provider) ->
  `apply`. `status` reports drift; `wait` polls OLM.
- `metadata.name` in YAML must equal the workspace `<name>`.
- Placeholders are OK on `expand`, `check`, and `status`; fatal on `generate`
  and `apply` unless `--allow-placeholders` is set.
- `apply` is the only mutating cluster command. `wait` is read-only but still
  shells to kubectl. `init`, `expand`, `check`, `generate`, `push`, and
  `status` do not contact a cluster. `push` is cluster-agnostic but does
  talk to a git provider over HTTPS.
- Summaries go to stderr. Return errors from `RunE`; root `SilenceUsage` and
  `SilenceErrors` ensure errors print once.

## Commands

| Command | Behavior |
| --- | --- |
| `init <name> [-d dir] [--force]` | Writes scaffold `provision.yaml`; refuses overwrite unless forced. |
| `expand <name> [-d dir] [--force]` | Resolves catalog into `full-provision.yaml`; preserves user fills unless forced. |
| `fill <name> [-d dir] --set <instance>.<dotted.path>=<value>` | Rewrites `full-provision.yaml`, dropping supplied values into matching package `resolvedValues`. Repeat `--set`. No array-index support (edit nested arrays directly). |
| `check <name> [-d dir]` | Validates schema, dry-expands catalog/resolve, and validates existing `FullProvision`. |
| `plan <name> [-d dir] [--full]` | Prints the ordered apply plan (bootstrap subset by default, or the full tree with `--full`) without touching the cluster. |
| `generate <name> [-d dir] [--context c] [--allow-placeholders] [--prune] [--skip-determinism-check]` | Renders repo dirs from `FullProvision`; re-renders to a scratch dir and fails on byte drift unless `--skip-determinism-check` is set. `--context` is a label for templates, not cluster access. |
| `push <name> --provider <p> --base-url <url> [-d dir] [--flatten] [--token t] [--user u] [--branch main] [--commit-message m] [--visibility private] [--create-missing] [--force] [--dry-run]` | Publishes each rendered repo dir to the named provider (github, gitlab, gitea). Defaults to the user's configured git credentials; flags override. |
| `apply <name> --to <ctx> [-d dir] [--dry-run] [--allow-placeholders] [--wait-crds] [--wait-timeout 10m]` | Runs `kubectl --context <ctx> apply -k` for each rendered repo in dependency order. Prints a one-line mode banner + ordered plan preview; emits compatibility warnings when the target cluster version falls outside a planned package's declared `spec.compatibility.kubernetes` range; surfaces CSV-owner pod log tails on OLM timeout/failure. |
| `wait <name> --to <ctx> [-d dir] [--timeout 10m]` | Polls OLM subscriptions until CSVs reach `Succeeded`; fails on `Failed` or timeout. Surfaces pod log tails on failure. |
| `status <name> [-d dir] [--diff] [--diff-lines N]` | Renders to scratch and diffs against the workspace; exits non-zero on drift. With `--diff`, prints a truncated unified diff per modified file. |

## Gotchas

- Source paths resolve relative to `provision.yaml`, not `$PWD`.
- Repo names must be DNS-1123-safe (lowercase alphanumeric, `-`, `.`,
  and the literal `{{.Env}}` token; length ≤253 after substitution).
  `check` rejects slashes, uppercase, underscores, or leading/trailing
  hyphen/dot — a KRC's managed-repo Application uses the repo name as a
  K8s resource name, and a slash turns into an apply-time failure
  halfway through the run.
- `generate`'s determinism check catches chart-side non-determinism
  (auto-generated TLS certs, random IDs, timestamps) inline by
  re-rendering to a scratch dir and byte-comparing. Bypass with
  `--skip-determinism-check`; fix the underlying chart input first.
- `waitForCRDsEstablished` short-circuits on fresh clusters (no CRDs
  yet) so `apply` no longer emits "no matching resources found" on an
  empty target.
- `fill` parses `<instance>.<dotted.path>=<value>`. Values are always
  written as strings; for nested arrays or typed primitives, edit
  `full-provision.yaml` directly.
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
- `push` credential resolution order: `--token` flag -> `GITUPS_PUSH_TOKEN`
  env -> provider env (`GITHUB_TOKEN`, `GITLAB_TOKEN`, `GITEA_TOKEN`).
  If a token is resolved, gitups injects it into the HTTPS push URL and
  uses it for provider REST calls. With no token, git falls back to the
  user's configured credential helper and `--create-missing` errors
  out (no way to call the provider API without credentials).
- `push --base-url` parses as `<scheme>://<host>[:<port>]/<owner-or-group>[/subgroup…]`.
  The owner/group path is combined with each rendered repo name to form
  the remote path. `--flatten` replaces `/` in the rendered repo name
  with `-` so providers without subgroup support (GitHub, Gitea) stay
  happy when repo names carry a slash.

Pointers: [cmd/gitups/main.go](../../cmd/gitups/main.go),
[repo-layout.md](repo-layout.md), [schemas.md](schemas.md), [olm.md](olm.md)
