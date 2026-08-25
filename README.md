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
make install        # builds into ~/.local/bin/rwt, version stamped from git
```

Use `make` rather than a bare `go build`: Go records the commit in the build
info but not the tag it sits on, so a plain build reports a bare SHA. The
Makefile derives the version with `git describe --tags --dirty --always` and
passes it at link time, which is why `rwt version` reads `v0.4.1-2-gabc1234`
instead of `abc1234def56`.

`make build` compiles into the working directory instead, `PREFIX=/usr/local
make install` changes where it lands, and `make version` prints what a build
would stamp without building. Building from a source archive with no git works
too: pass `make install VERSION=v1.2.3`, or pass nothing and the binary falls
back to the revision Go stamped into its build info.

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
  "demo": "auto",
  "hooks": "full"
}
```

## Commands

```
rwt new   <name> --from <develop|bugfixes|master> [--type <prefix>] [--demo <mode>] [--idea] [--force-managed-env] [--here]
rwt setup <name|.> [--only <eco>] [--demo <mode>]   # (re)warm uv/cargo/pnpm in a worktree (. = repo root)
rwt ls [--live]        # list worktrees + instance capability (--live: slot/port/running)
rwt rm    <name> [--keep-branch] [--force] [--purge-memory]
rwt rm    --merged [--yes] [--keep-branch] [--force]   # sweep merged worktrees
rwt refresh [--demo <mode>]   # fetch + ff-only every long-lived base, warm cold ones
rwt clean [name|.] [--dry-run] [--cache]   # reclaim per-worktree cargo target dirs
rwt go    <name>       # print `cd <path>` into a worktree (eval it)
rwt check [name|.] [--stage pre-commit|pre-push] [--full] [--dry-run]   # run the CI gates your change needs
rwt hooks install|uninstall|status|off|on   # wire those gates into git
rwt config             # show umbrella path + dev flags
rwt config path <dir>  # set the rotki umbrella location
rwt config <flag> on|off    # toggle a dev flag
rwt config demo <off|auto|minor|patch>   # set the default VITE_DEMO_MODE
rwt config hooks <full|standard>         # how far the pre-push gate goes
rwt doctor             # preflight tools / umbrella + cargo cache report
rwt version            # print the rwt version (also `rwt --version`)
rwt completion install [bash|zsh|fish]   # install/update shell completion
```

`new` creates `../<prefix>-<name>` off `upstream/<base>` (`develop`→`feat/…`,
`bugfixes`/`master`→`fix/…`), warms the envs, then — only if the checkout supports it —
enables dev:web instance mode by appending `INSTANCE_NAME`. It is idempotent:
re-run to resume after a failed step.

### Branch prefix (`--type`)

The prefix defaults to the `--from` base (`develop`→`feat`, `bugfixes` and
`master`→`fix`). Override it with `--type` (`-t`) to use any Conventional Commit
type, keeping `--from` as the base to branch off:

```sh
rwt new dark-mode                       # ../feat-dark-mode  on feat/dark-mode
rwt new login-crash --from bugfixes     # ../fix-login-crash on fix/login-crash
rwt new bump-deps --type chore          # ../chore-bump-deps on chore/bump-deps (off develop)
rwt new flaky-e2e  --type test --from bugfixes
```

Accepted types: `feat`, `fix`, `chore`, `refactor`, `docs`, `test`, `perf`,
`build`, `ci`, `style`, `revert`. `ls` / `setup` / `rm` resolve a worktree by
bare name across all of these.

### Branching off `master`

`--from master` is for the release window: the stretch between develop being
merged into master and the stable tag, where master is what the release is cut
from and a blocker has to be fixed there.

```sh
rwt new rc-crash --from master          # ../fix-rc-crash on fix/rc-crash
```

Outside that window master is just the released code, and develop or bugfixes is
the base you want. Nothing enforces the window (rwt cannot tell where in the
release cycle you are), so picking the right base is on you.

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
develop ships the next **minor**, bugfixes ships the next **patch**. master
during the release window is holding that same minor un-tagged, so it counts as
**minor** too.

rwt writes the key for you. Four modes:

| mode    | what lands in `.env.development.local`          |
| ------- | ----------------------------------------------- |
| `off`   | nothing — the line is removed (the default)      |
| `auto`  | `minor` on develop/master-based, `patch` on bugfixes-based |
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
itself rather than being scored, which during the release window is the only
thing that keeps master and develop apart: master then contains develop's tip,
and a full scoring tie falls back to candidate order, where develop and bugfixes
come before master.

## Local gates (`rwt check` / `rwt hooks`)

CI decides which jobs to run from a `rotki/action-job-checker` config embedded in
`.github/workflows/rotki_ci.yml`: a change under `frontend/` runs the frontend
job, a change under `rotkehlchen/` runs backend lint and then the backend suite.
`rwt check` runs the local half of that same decision, so you find out before the
push instead of after it.

```sh
rwt check                        # the PR diff, lint + typecheck
rwt check --stage pre-commit     # the index, fast checks only
rwt check --full                 # add unit tests narrowed to what changed
rwt check --dry-run              # print the plan and the groups, run nothing
```

`rwt hooks install` wires the same two plans into git as `pre-commit` and
`pre-push`.

### Tiers

Tiers run in order and the run stops after the first tier that fails, so the
cheap failure is the one you read.

| tier | when | what |
|---|---|---|
| fast | pre-commit and pre-push | `typos`, `lint-staged` (ESLint + Stylelint), `ruff`, `double-indent`, `cargo fmt --check` |
| standard | pre-push | `typecheck`, `knip`, `lint:style`, `check:linked-keys`, `test:proxy`, `mypy`, the diff-scoped logging lint, `cargo clippy` |
| heavy | pre-push with `--full` | `vitest` and `pytest` narrowed to the changed files, `cargo test` |

`knip` earns its place: it runs in CI and no local script invokes it, so an
export used only inside its own file passes every gate you can run by hand and
fails the PR. `pyright` and `pylint` are deliberately absent, since minutes over
the whole tree is not a hook.

The heavy tier never widens to a suite. A changed spec runs itself, a changed
source file runs its sibling spec (or the specs beside it), a changed Python
module runs its same-named test under `rotkehlchen/tests/`. Anything that maps to
nothing is reported as skipped and left to CI. Set the pre-push depth once with
`rwt config hooks full|standard`.

### Scope

`pre-commit` gates the index, because that is what the commit will contain.
`pre-push` gates the PR diff (the merge-base against `upstream/<base>`, using the
same base detection demo mode uses), so a re-push re-checks the whole PR rather
than only the commits added since last time.

### Detection, not assumption

A check is only planned when the worktree can actually run it. The script set
differs by base: `develop` and `master` have `knip`, `check:linked-keys`,
`lint:file` and `test:proxy`; `bugfixes` has none of them. rwt reads the
worktree's own `frontend/package.json`, its cargo layout, and its `.venv`, and
reports what it skipped and why rather than running a command that is not there:

```
skip knip: frontend/package.json has no "knip" script on this base
skip ruff: .venv/bin/ruff is not present in this worktree; install it with: rwt setup <worktree> --only uv
```

The checks gate on the tool being in the venv rather than letting `uv run`
resolve it, so a commit never turns into a package install and never fails over a
missing dependency instead of over your code. `rwt doctor` flags it too.

That skip used to be the normal state rather than an edge case, and it is worth
knowing why. The Python lint group was opt-in behind `setup --lint`, and `rwt
new` never asked for it, so ruff and mypy were missing from nearly every
worktree — and because a missing tool is *skipped* rather than failed, the Python
gates were quietly not running while `rwt check` still reported success. rwt now
syncs **every** dependency group rotki declares (`dev`, `lint`, `docs`,
`packaging`, `profiling`, `ci`) with `uv sync --frozen --all-groups`, so a warmed
worktree has the whole toolchain. A worktree warmed before that change just needs
its uv step re-run.

### Install

```sh
rwt hooks install     # take over core.hooksPath
rwt hooks status      # what is wired, and which worktrees are opted out
rwt hooks off         # make the hooks inert in this worktree
rwt hooks uninstall   # put back whatever core.hooksPath held before
```

Install is umbrella-wide because it has to be: `core.hooksPath` lives in the
repository-local config, and every linked worktree shares one config file. The
opt-out is therefore the per-worktree half: a marker inside that worktree's own
git dir, so it is never a file in your tree, needs no `.gitignore` entry, and
disappears when `rwt rm` removes the worktree.

Whatever `core.hooksPath` held before is recorded, and rwt execs it after its own
checks pass, so an existing mechanism keeps working underneath. Install refuses
to displace a hooksPath that points at a directory which exists unless you pass
`--force`. It is worth running `rwt hooks status` before installing: a
`core.hooksPath` aimed at a directory that does not exist makes git run no hooks
at all and say nothing about it, which is easy to end up with (running husky from
the wrong cwd does it) and impossible to notice.

The installed scripts are four-line shims that `exec rwt hooks run <stage>`, so
upgrading rwt upgrades the checks. Bypass with git's own `--no-verify`, or
`RWT_HOOKS=0` for the whole shell.

## Per-worktree cargo target dirs

Every worktree builds into its own `<worktree>/target`, and every cargo workspace
inside that worktree shares it. Rebuilding a worktree you already built stays a
no-op; a fresh one compiles its dependencies once, which measures at ~50s and
1.4 GB for `colibri` + `starling`.

**This replaces a shared cache, and the replacement is a bug fix.** Until
2026-08 every worktree was pointed at one shared target dir per workspace under
`~/.cache/rwt/target/`, on the theory that cargo's metadata hash kept their
artifacts apart. It does not, and the failure was silent: worktrees ran each
other's binaries. [Why the shared cache was wrong](#why-the-shared-cache-was-wrong)
has the measurement.

Across an umbrella this costs less disk than the shared cache did, not more: a
shared target dir accumulates every worktree's artifacts without deduplicating
them, so it had grown larger than the sum of the isolated dirs that replaced it.
Cargo's exclusive per-target-dir lock, which made two worktrees building at the
same time serialise, goes away with it.

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

The layout is still detected per worktree, because it differs by base:

| layout | bases |
| --- | --- |
| root `Cargo.toml` workspace (`colibri` + `crates/*` members) | current `develop` |
| separate `colibri/` and `crates/` workspaces | bases predating the merge |
| `colibri/` only | older bases (`bugfixes`, `master`) |

All of them build into `<worktree>/target`. Pointing the split layout's two
workspaces at one dir is what keeps the dev launcher's shortcut working there,
and is safe in a way sharing across worktrees never was: different packages, and
a worktree is only ever on one layout at a time.

Rebasing a worktree across that boundary re-wires it and removes the config rwt
wrote for the old layout. A hand-written `.cargo/config.toml` is never touched
(rwt reports it and skips wiring that workspace rather than overwriting it).

The detection is transitional: the root workspace becomes the baseline on every
live base after the next rotki release, and the two fallback rows above go with
it.

`new`, `setup`, `refresh` and `clean` wire it automatically. If `sccache` is on
`PATH` it is also set as the rustc wrapper. It is a trim rather than a
mechanism: with paths normalised it reaches ~92% on rustc invocations across
worktrees, which converts to about 10% of wall clock, because a cold build is
dominated by link steps that sccache cannot cache at all.

Getting even that requires telling sccache the paths are equivalent, which rwt
does by managing `basedirs` in `~/.config/sccache/config` (honoring
`$SCCACHE_CONF` and `$XDG_CONFIG_HOME`). It lists **every worktree root** and is
regenerated whenever the worktree set changes, since a stale entry is not inert:
`basedirs` resolves by longest matching prefix, so a leftover can shadow the
entry that should have matched. A common parent directory does not work at all —
stripping it still leaves `develop/colibri/src/…` against `master/colibri/src/…`.
Measured on two worktrees at the same commit: common parent **0%** Rust cache
hits, per-worktree roots **92%**.

sccache reads that file once at server start, so rwt stops the server whenever it
rewrites it; the next build starts one that has read it. A config file you wrote
yourself is never overwritten — rwt reports it and leaves it alone, since it may
carry cache backends or limits rwt knows nothing about.

> The variable is `SCCACHE_BASEDIRS`, **plural**. The singular is silently
> ignored, and the only symptom is `Base directories (none)` in
> `sccache --show-stats`. `rwt doctor` reports which worktrees are covered.

> **Do not export `CARGO_INCREMENTAL=1`.** With a rustc wrapper configured,
> sccache checks that variable rather than the rustc flag and refuses to run at
> all, failing every cargo build with `incremental compilation is prohibited` —
> an error naming neither rwt nor sccache. Left unset, cargo still compiles
> workspace members incrementally and sccache simply declines to cache those.
> `rwt doctor` warns if it finds it set.

### Keeping the dev launcher off `cargo run`

rotki's dev launcher runs `<worktree>/target/debug/<name>` when it exists and
falls back to `cargo run` when it does not. Building into `<worktree>/target`
puts cargo's own output at exactly that path, so the shortcut works with no
bookkeeping at all.

This used to take real machinery: clearing the shared dir's hardlink "uplift
slot" before each build, matching inodes against `deps/` afterwards to work out
which artifact was this worktree's, symlinking the result, and a staleness check
that ran `cargo clean -p` when the shared dir handed back an artifact older than
its own sources. All of it existed to compensate for the shared target dir, and
all of it is gone.

### Why the shared cache was wrong

The shared design claimed the `deps/` hash was derived from the worktree's
manifest path, so two worktrees could never collide. **That is false**, and it
was measured rather than reasoned about:

```
develop/target/debug/colibri -> .../deps/colibri-029385afae5985ed
master/target/debug/colibri  -> .../deps/colibri-029385afae5985ed   # same file
```

Those two worktrees had genuinely different colibri sources, and the shared
cache held exactly one colibri artifact and one `.fingerprint` entry. Cargo
records depfile paths **relative** to the workspace root (`colibri/src/main.rs`,
not an absolute path), so two worktrees sharing a target dir are
indistinguishable to it: one fingerprint namespace, one artifact, freshness
decided on mtimes within it. Whichever worktree built last owned the binary, and
every other worktree's symlink followed it.

That is also why a build could fail against a symbol that exists only in a
neighbour's tree. It was not the symlink misbehaving; it was cargo reusing a
neighbour's compilation unit.

On the umbrella that produced the measurement, **every** worktree resolved to a
single `colibri` and a single `starling`. This was the normal state, not an edge
case.

`rwt doctor` still reports it, now as a regression guard that should stay silent:

```
[FAIL] 2 binaries shared across worktrees
       colibri: develop, master
       starling: develop, master
```

It reads the symlinks rather than the config, so it catches a worktree left on
the old wiring as well as anyone who reintroduces a shared `CARGO_TARGET_DIR` by
hand. To migrate a worktree it names, run `rwt clean`.

### Migrating off the shared cache

`rwt clean` does it, per worktree or across the umbrella:

- rewires each workspace onto `<worktree>/target`
- drops the symlinks pointing into the old shared cache, which is what stops a
  worktree from running its neighbour's binary
- reclaims `colibri/target` and `crates/target`, which nothing builds into now

`<worktree>/target` is left alone by default: it is the live build directory, and
reclaiming it trades disk for a cold rebuild (~50s). `--deep` does it anyway,
when the disk matters more. It skips any worktree with a running dev session,
checked by reading `/proc/<pid>/exe` rather than matching process names —
starling respawns colibri, and Linux unlinks a running binary without complaint,
so the supervisor would sit there exec'ing something that is no longer on disk.

Once every worktree is migrated, the old shared cache is pure garbage and is
where the reclaimable disk actually is. `rwt clean --cache` removes it.

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

`clean` wires before removing by design: the wiring is what redirects a worktree
onto its own target dir and drops its links into the old shared cache, so doing
it second would leave a reclaimed worktree still pointing at a neighbour's
binary. `rwt doctor` reports total target-dir size, any unwired workspaces, how
much disk the superseded dirs hold, and whatever is left in the old shared cache.

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
`ls` / `rm` / `refresh`), cargo target dirs (`clean`), the local gates
(`check` / `hooks`), `config`, `doctor`, shell completion, and the conveniences
`--type`, `--demo`, `rwt go`, `ls --live`, and `rm --merged`.

Deliberately not planned (considered and dropped): `rwt pr` (just use `gh`), the
`rm` process-kill backstop, and `CLAUDE.local.md`/`WORKTREE.md` stamping. The
gates stop short of e2e, the full test suites, `pyright`/`pylint`, and the docs
build; those stay in CI, where the runners are not your laptop. IntelliJ
project-close on `rm` is also out — the `idea` launcher has no `close` verb, and
on Linux open editor handles don't block worktree removal anyway.

## Environment overrides

- `RWT_UMBRELLA` — path to the `rotki/rotki` umbrella. Takes precedence over the
  configured path; there is no built-in default (see **Configuration**).
- `RWT_CARGO_CACHE` — root of the *old* shared cargo cache, which now only
  `rwt clean --cache` and `rwt doctor` look at. Takes precedence over
  `$XDG_CACHE_HOME/rwt/target` and `~/.cache/rwt/target`.
- `RWT_HOOKS=0` makes the installed git hooks a no-op for this shell, without
  reaching for `--no-verify` on every command.

## Development

Hooks live in `.githooks/`. Enable them once per clone:

```sh
git config core.hooksPath .githooks
```

The `pre-commit` hook blocks a commit unless `gofmt`, `go vet`, `go test ./...`
and `go build` all pass. `make test` runs the same set by hand.

Releasing is a tag: `git tag -a vX.Y.Z -m ...` then `make install`, and the
binary reports the new version. The stamped variable is
`internal/cli.version`; `-X` against a name that does not exist is silently
ignored, so a rename there would quietly revert every build to a bare SHA.
`TestResolveVersionPrefersTheStampedValue` exists to catch that.

## License

MIT © Konstantinos Paparas. See [LICENSE](LICENSE).
