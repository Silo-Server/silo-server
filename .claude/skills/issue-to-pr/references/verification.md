# Verification inside a worktree

Commands assume the repository root is the cwd (the new worktree, `$WT`).

## Why the obvious commands fail here

A `.claude/worktrees/*` checkout lives under the parent Silo workspace, whose
`go.work` only lists the primary `./silo-server`, not worktree checkouts. So:

- `go env GOWORK` resolves to the parent workspace from anywhere under `SiloServer/`.
- Plain `go build ./...` / `go test ./...` fail with
  *"directory prefix . does not contain modules listed in go.work"*.
- `go build ./...` also needs `web/dist` to exist (the embed directive).
- `golangci-lint` is not installed locally.

## Go

Prefix every Go command with `GOWORK=off`.

```bash
# Build the parts that matter without needing web/dist:
GOWORK=off go build ./internal/... ./cmd/...

# Run the tests for the package(s) you touched (fast, targeted):
GOWORK=off go test ./internal/<pkg>/...

# If you need a full ./... build, stub the embed target first:
mkdir -p web/dist && touch web/dist/.gitkeep
GOWORK=off go build ./...
```

Lint Go without installing golangci-lint — must be the **v2** module path (the
repo's `.golangci` config rejects v1):

```bash
GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./internal/...
```

**Known flake, not a real failure:** `internal/playback` GPU-probe tests
(`TestResolveHWAccel*`, the NVENC smoke test) can fail with `signal: killed`
under parallel load on macOS, but pass in isolation. If they're the only
failures, re-run them alone before treating them as real:

```bash
GOWORK=off go test ./internal/playback/ -run TestResolveHWAccel -count=1
```

## Frontend (only if `web/` changed)

```bash
cd web
pnpm install          # if deps changed or node_modules is absent
pnpm run lint
pnpm run format:check
pnpm run build         # catches type/build errors before the PR
cd ..
```

The repo requires `pnpm run lint` and `pnpm run format:check` to be clean before
a merge request.

## Repo-wide

```bash
make verify-local-paths   # must be clean; CLAUDE.md forbids absolute paths in docs/specs
```

## Migrations

If the change needs schema work, create a timestamped Goose migration — never
hand-number, never `goose fix`, never paired up/down files:

```bash
make migrate-create NAME=add_thing
make migrate-validate
make migrate-status
```
