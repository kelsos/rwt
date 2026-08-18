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
  "flags": { "dev-tools": true, "logs": true, "persist": true },
  "demo": "auto"
}
```

## Commands

```
rwt new   <name> --from <develop|bugfixes> [--type <prefix>] [--demo <mode>] [--idea] [--force-managed-env] [--here]
rwt setup <name|.> [--only <eco>] [--demo <mode>]   # (re)warm uv/cargo/pnpm in a worktree (. = repo root)
rwt ls [--live]        # list worktrees + instance capability (--live: slot/port/running)
rwt rm    <name> [--keep-branch] [--force] [--purge-memory]
rwt rm    --merged [--yes] [--keep-branch] [--force]   # sweep merged worktrees
rwt refresh [--demo <mode>]   # fetch + ff-only every long-lived base, warm cold ones
rwt clean [name|.] [--dry-run] [--cache]   # reclaim per-worktree cargo target dirs
rwt go    <name>       # print `cd <path>` into a worktree (eval it)
rwt config             # show umbrella path + dev flags
rwt config path <dir>  # set the rotki umbrella location
rwt config <flag> on|off    # toggle a dev flag
rwt config demo <off|auto|minor|patch>   # set the default VITE_DEMO_MODE
rwt doctor             # preflight tools / umbrella + cargo cache report
rwt version            # print the rwt version (also `rwt --version`)
rwt completion install [bash|zsh|fish]   # install/update shell completion
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

`rwt ls --live` adds runtime state: it reads the app's port registry
(`$XDG_DATA_HOME/rotki-dev/.port-index.json`, or `$ROTKI_DEV_INSTANCES_DIR`),
maps each worktree's `INSTANCE_NAME` to its slot's dev port, and probes whether
that port is listening — so you can see which instances are actually up and on
which port. Read-only: `rwt` never writes the registry.

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

`refresh` re-asserts the flags on every present long-lived base unconditionally:
that's what keeps `VITE_PERSIST_STORE` in place so a post-refresh restart doesn't
log you out. The write is skipped when nothing would change, so it stays a no-op.

## Demo mode (`VITE_DEMO_MODE`)

`VITE_DEMO_MODE` makes the app report a released version instead of a dev one,
so version-gated UI (the update banner, release-gated features) shows up in a
dev build. The value picks which release to fake, and that depends on the base:
develop ships the next **minor**, bugfixes ships the next **patch**.

rwt writes the key for you. Four modes:

| mode    | what lands in `.env.development.local`          |
| ------- | ----------------------------------------------- |
| `off`   | nothing — the line is removed (the default)      |
| `auto`  | `minor` on develop-based, `patch` on bugfixes-based |
| `minor` | `VITE_DEMO_MODE=minor` regardless of base        |
| `patch` | `VITE_DEMO_MODE=patch` regardless of base        |

Set it once, or override a single run:

```sh
rwt config demo auto            # persisted; every new/setup/refresh derives it
rwt new checkout-flow --demo patch     # this worktree only
rwt setup . --demo off          # drop it again
```

`--demo` beats the configured mode for that run. `off` **removes** the line
rather than writing an empty value: the app tests
`import.meta.env.VITE_DEMO_MODE !== undefined`, so `VITE_DEMO_MODE=` would still
count as on. Like the dev flags, the key is outside both of the app's managed
key sets, so `dev:web` leaves an rwt-written line alone.

`auto` works out the base from history, not from the branch prefix: rotki has
plenty of `fix/*` branches cut from develop, so the prefix tells you what kind of
change it is, not what it was branched off. rwt scores each base by how many
commits HEAD would add to it and takes the lowest, breaking ties by how far the
fork point sits behind the base tip (which is what separates the two when
bugfixes is fully merged into develop). A checked-out long-lived base answers
itself; `master` has no release of its own to fake, so it gets nothing and says
so.

## Shared cargo cache

Every worktree points its cargo builds at one shared target dir per cargo
workspace under `~/.cache/rwt/target/` (honoring `$XDG_CACHE_HOME`), so
dependencies compile once and are reused everywhere. A fresh worktree only
compiles rotki's own crates; switching back to one you already built is a no-op
rather than a rebuild.

The wiring is a generated `.cargo/config.toml` at each workspace root, not an env
var, because the dev launch shells out to cargo itself
(`frontend/scripts/dev/services.ts` builds from the worktree root). A config file
is picked up by rwt's warm step *and* by the app's own cargo invocations,
including ones started from an IDE. The generated paths are added to the repo's
shared `info/exclude`, so they never show up in `git status`.

**Placement is load-bearing.** Cargo discovers config by walking up from the
*current working directory*, never from `--manifest-path`. A config under
`colibri/` is invisible to a build launched from the worktree root, which is how
both rwt and the app build, so the config always goes at the workspace root and
rwt's own warm steps `cd` into it.

The layout is detected per worktree, because it differs by base:

| layout | shared dir | bases |
| --- | --- | --- |
| root `Cargo.toml` workspace (`colibri` + `crates/*` members) | `target/rotki` | current `develop` |
| separate `colibri/` and `crates/` workspaces | `target/colibri`, `target/crates` | bases predating the merge |
| `colibri/` only | `target/colibri` | older bases (`bugfixes`, `master`) |

Rebasing a worktree across that boundary re-wires it and removes the config rwt
wrote for the old layout, so a worktree never compiles into two caches at once.
A hand-written `.cargo/config.toml` is never touched (rwt reports it and skips
wiring that workspace rather than overwriting it).

The detection is transitional: the root workspace becomes the baseline on every
live base after the next rotki release, and the two fallback rows above go with
it.

`new`, `setup`, `refresh` and `clean` wire it automatically. If `sccache` is on
`PATH` it is also set as the rustc wrapper, catching misses a target dir cannot
(rustc upgrades, changed rustflags); its absence costs cache hits, not
correctness.

The one cost is contention: cargo takes an exclusive lock per target dir, so two
worktrees building the *same* workspace at the same time serialise. That is a
wait during compilation, not a failure.

### Keeping the dev launcher off `cargo run`

rotki's dev launcher runs `<worktree>/target/debug/<name>` when it exists and
falls back to `cargo run` when it does not. Redirecting the target dir empties
that path, so the fallback would fire on every launch: a visible "Compiling" at
`pnpm run dev`, and an extra cargo process wedged between starling and the
service it supervises.

So after each warm build rwt symlinks that path at the worktree's own artifact in
the shared cache:

```
develop/target/debug/colibri -> ~/.cache/rwt/target/rotki/debug/deps/colibri-288ac144823fe9e3
```

It links to the artifact under `deps/` rather than to `debug/colibri`, because
that top-level path is a single hardlink slot every worktree shares and it
belongs to whichever one built last. The `deps/` hash is derived from the
worktree's manifest path, so it stays *this* worktree's artifact, and cargo
rewrites it in place: the symlink never needs refreshing and can never serve a
stale binary. Cargo only writes the slot when a build produces output, so rwt
clears it before building — cargo re-links a missing slot even when nothing
recompiles, which is what makes the artifact identifiable afterwards.

If the artifact cannot be identified the link is skipped rather than guessed at,
and the launcher takes its `cargo run` fallback: slower, still correct.

### Rebuilding one ecosystem (`setup --only`)

After touching Rust, `--only` narrows a setup to just that ecosystem instead of
re-running pnpm and uv alongside it:

```sh
rwt setup . --only cargo       # from inside the worktree
rwt setup login-crash --only colibri
rwt setup . --only pnpm,uv
```

Use this rather than running `cargo build` yourself: the step keeps the
uplift-slot clearing and the artifact symlink described above, so the dev
launcher keeps finding `target/debug/<name>`. A hand-run build leaves that path
empty and quietly costs you the fallback.

Selectors are ecosystem tags, not step names: `cargo`, `rust`, `colibri`,
`starling` and `crates` all mean the same thing, which is what makes one command
correct on both cargo layouts (the step is named `rotki` on the root workspace
and `colibri`/`crates` on the split one). An unknown selector, or one this
worktree has no step for, is an error rather than a run that builds nothing. A
narrowed run skips the dev-flag write a full setup does.

One habit worth keeping on the root-workspace layout: build with `-p colibri -p
starling`, the way rwt and `pnpm dev:web` both do. Selecting a subset changes
cargo's feature unification, which re-fingerprints the shared deps — harmless,
but it costs a rebuild each time you alternate, and now that the target dir is
shared, everyone pays it.

```sh
rwt clean --dry-run    # what the per-worktree target dirs are still holding
rwt clean              # wire every worktree, then remove the dirs it supersedes
rwt clean login-crash  # limit it to one worktree
rwt clean --cache      # also drop the shared dirs (full rebuild everywhere)
```

`clean` wires before removing by design: deleting a target dir from an unwired
worktree would just trade disk for a cold rebuild. `rwt doctor` reports the
shared cache size, any unwired workspaces, and how much disk the superseded
per-worktree target dirs still hold.

It removes only what cargo put in a target dir — its markers, its profile dirs,
and the per-triple dirs a cross-compile leaves behind — and keeps two things that
share the directory:

- `target/backend`, the frozen python core the e2e run builds (`pyinstaller
  --distpath target/backend`), which cargo never wrote and which is slow to
  rebuild.
- the launcher symlinks above, which cost no disk and still resolve afterwards.

A `target` directory with no cargo markers in it is left alone entirely: the name
is common enough that acting on it alone would eventually delete something that
was never cargo's. `--dry-run` shares the same walk as the real run, so its
number is a preview rather than an estimate.

## Bulk cleanup (`rwt rm --merged`)

After a few PRs land, `rwt rm --merged` removes every non-long-lived worktree
whose branch is already merged into an upstream base — the worktree analogue of
`git branch --merged`. It fetches `upstream` first (so the check isn't stale),
lists the candidates, and asks before removing (`--yes` skips the prompt). Each
removal reuses the normal teardown: dirty/unpushed guard (override with
`--force`), dev:web instance clean, worktree + branch deletion.

```sh
rwt refresh && rwt rm --merged       # warm bases, then clear landed worktrees
```

## Jumping into a worktree (`rwt go`)

A binary can't change its parent shell's cwd, so `rwt go <name>` prints a
`cd <path>` line for you to eval (the same trick as `rwt new --here`):

```sh
eval "$(rwt go login-crash)"     # bare name; the prefix is resolved for you
```

Wrap it once in your shell rc so `rwt go x` cds directly, and the installed
completion will suggest worktree names:

```sh
rwt() { if [ "$1" = go ]; then cd "$(command rwt go "${@:2}" | sed 's/^cd //')"; else command rwt "$@"; fi; }
```

## Shell completion

```sh
rwt completion install        # detects your shell from $SHELL
rwt completion install zsh    # or name it explicitly (bash|zsh|fish)
```

Writes a per-user completion script (no root): zsh into a writable dir already
on your `$fpath` — falling back to `~/.zsh/completions` with a one-line `fpath`
hint — bash into `~/.local/share/bash-completion/completions`, fish into
`$XDG_CONFIG_HOME/fish/completions`. Re-run after upgrading rwt to refresh it.
(`rwt completion <shell>` still just prints the script to stdout, à la Cobra.)

Worktree names complete for `setup` / `rm` / `clean` / `go`; `rwt config`
completes its setting names and then the values that setting takes (`on|off`
for a flag, `off|auto|minor|patch` for `demo`, directories for `path`); and the
`--only` / `--demo` flags complete their values. The installed script is a thin
loader that asks the binary, so a rebuilt rwt updates completions without
re-running the install.

## Status

Feature-complete for its intended scope: worktree lifecycle (`new` / `setup` /
`ls` / `rm` / `refresh`), the shared cargo cache (`clean`), `config`, `doctor`,
shell completion, and the conveniences `--type`, `--demo`, `rwt go`,
`ls --live`, and `rm --merged`.

Deliberately not planned (considered and dropped): `rwt pr` (just use `gh`), the
`rm` process-kill backstop, branch-guard hook install (would need an upstream
Husky extension point), and `CLAUDE.local.md`/`WORKTREE.md` stamping. IntelliJ
project-close on `rm` is also out — the `idea` launcher has no `close` verb, and
on Linux open editor handles don't block worktree removal anyway.

## Environment overrides

- `RWT_UMBRELLA` — path to the `rotki/rotki` umbrella. Takes precedence over the
  configured path; there is no built-in default (see **Configuration**).
- `RWT_CARGO_CACHE` — root of the shared cargo target dirs. Takes precedence over
  `$XDG_CACHE_HOME/rwt/target` and `~/.cache/rwt/target`.

## Development

Hooks live in `.githooks/`. Enable them once per clone:

```sh
git config core.hooksPath .githooks
```

The `pre-commit` hook blocks a commit unless `gofmt`, `go vet`, `go test ./...`
and `go build` all pass.

## License

MIT © Konstantinos Paparas. See [LICENSE](LICENSE).
