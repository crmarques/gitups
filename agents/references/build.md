# Build

Use when changing the Makefile, building a local CLI, or validating Go changes.

## Rules

- Build with `make build`. It writes `./bin/gitups` using the repo contract for
  output path and flags.
- Run the local CLI as `./bin/gitups <command>` or `make run ARGS='...'`.
- Keep `bin/` ignored. Do not add root-level `./gitups` binaries or examples
  that encourage them.
- Use `make check` before handing off Go or build-system changes. It runs
  format, vet, and unit tests.

## Commands

```sh
make build
./bin/gitups <command>
make check
```

Direct builds are only for debugging and must keep the same output path:

```sh
go build -trimpath -o ./bin/gitups ./cmd/gitups
```

Pointers: [Makefile](../../Makefile), [cmd/gitups/main.go](../../cmd/gitups/main.go)
