# rwt — rotki worktree tool

A small Go CLI that spawns/tears down git worktrees for parallel-agent /
parallel-PR work on the **rotki app repo** (`rotki/rotki/` umbrella), and warms
each worktree's uv / cargo / pnpm environments.

Rotki-specific by design: remotes, branch conventions and the dev:web port
layout are baked in. The one thing rwt does **not** assume is where your
`rotki/rotki` umbrella lives — you configure that once (see **Configuration**).
Per-user state (umbrella path + dev flags) lives in a small config file. It is a
**thin shim**: the app (`frontend/scripts/dev-instance/`) owns dev:web slot
allocation, the managed-env block, and the mkdir-locked `.port-index.json`
registry. `rwt` only appends one `INSTANCE_NAME=<name>` line and never writes the
registry.

## Install

```sh
go build -o ~/.local/bin/rwt ./cmd/rwt
```

## Configuration

rwt assumes no location for the rotki umbrella. Set it once before any
worktree command:

```sh
rwt config path ~/development/repos/rotki/rotki
```

Until it is set, umbrella-touching commands (`new`, `setup`, `ls`, `rm`,
`refresh`) refuse with a hint. Resolution order is `RWT_UMBRELLA` env > config
file > nothing. State is stored in `~/.config/rwt/config.json` (honoring
`$XDG_CONFIG_HOME`):

```json
{
  "umbrella": "/home/you/development/repos/rotki/rotki",
  "flags": { "dev-tools": true, "logs": true, "persist": true }
}
```

## Commands

```
rwt new   <name> --from <develop|bugfixes> [--type <prefix>] [--idea] [--force-managed-env] [--here]
rwt setup <name|.>     # (re)warm uv/cargo/pnpm in an existing worktree
rwt ls                 # list worktrees + instance capability
rwt rm    <name> [--keep-branch] [--force] [--purge-memory]
rwt refresh            # fetch + ff-only every long-lived base, warm cold ones
rwt config             # show umbrella path + dev flags
rwt config path <dir>  # set the rotki umbrella location
rwt config <flag> on|off    # toggle a dev flag
rwt doctor             # preflight sccache / tools / umbrella
```

`new` creates `../<prefix>-<name>` off `upstream/<base>` (`develop`→`feat/…`,
`bugfixes`→`fix/…`), warms the envs, then — only if the checkout supports it —
enables dev:web instance mode by appending `INSTANCE_NAME`. It is idempotent:
re-run to resume after a failed step.

### Branch prefix (`--type`)

The prefix defaults to the `--from` base (`develop`→`feat`, `bugfixes`→`fix`).
Override it with `--type` (`-t`) to use any Conventional Commit type, keeping
`--from` as the base to branch off:

```sh
rwt new dark-mode                       # ../feat-dark-mode  on feat/dark-mode
rwt new login-crash --from bugfixes     # ../fix-login-crash on fix/login-crash
rwt new bump-deps --type chore          # ../chore-bump-deps on chore/bump-deps (off develop)
rwt new flaky-e2e  --type test --from bugfixes
```

Accepted types: `feat`, `fix`, `chore`, `refactor`, `docs`, `test`, `perf`,
`build`, `ci`, `style`, `revert`. `ls` / `setup` / `rm` resolve a worktree by
bare name across all of these.

## Capability detection

The dev:web multi-instance feature lives on `develop`, not `bugfixes`. `rwt`
detects it by file-stat (`frontend/scripts/dev-instance/index.ts`), not by
branch name, and refuses to write `INSTANCE_NAME` into a checkout that would
silently ignore it (no isolation). `--force-managed-env` overrides.

## Dev flags

A small set of dev-comfort env vars can be toggled once and applied to every
worktree automatically:

| alias       | env key              | what it does                              |
| ----------- | -------------------- | ----------------------------------------- |
| `dev-tools` | `ENABLE_DEV_TOOLS`   | in-app Vue/dev tooling                    |
| `logs`      | `VITE_DEV_LOGS`      | verbose local dev logs                    |
| `persist`   | `VITE_PERSIST_STORE` | persist store across restarts (stay logged in) |

```sh
rwt config              # list flags and on/off state
rwt config logs off     # toggle one (persisted)
```

Flags live in the same `~/.config/rwt/config.json`; an absent file means every
flag is on. Enabled flags are upserted into a worktree's
`.env.development.local` on `new` / `setup` / `refresh`; disabled flags have
their line removed. These keys sit **outside** the app's `MANAGED_ENV_KEYS`, so
`dev:web` preserves them verbatim.

`refresh` re-asserts the flags on every present long-lived base unconditionally —
that's what keeps `VITE_PERSIST_STORE` in place so a post-refresh restart doesn't
log you out. The write is skipped when nothing would change, so it stays a no-op.

## Status

v0 (shim) + most of v1 (lifecycle + doctor) implemented. Deferred: branch-guard
hook install, `CLAUDE.local.md`/`WORKTREE.md` stamping, `rwt pr`.

## Environment overrides

- `RWT_UMBRELLA` — path to the `rotki/rotki` umbrella. Takes precedence over the
  configured path; there is no built-in default (see **Configuration**).

## Development

Hooks live in `.githooks/`. Enable them once per clone:

```sh
git config core.hooksPath .githooks
```

The `pre-commit` hook blocks a commit unless `gofmt`, `go vet`, `go test ./...`
and `go build` all pass.

## License

MIT © Konstantinos Paparas. See [LICENSE](LICENSE).
