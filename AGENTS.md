# AGENTS.md

## Overview

`bkp` takes backups without intermediate copies. A source process (`tar`,
`mysqldump`, `dd` off a snapshot, …) streams through optional `compress`,
`crypt` and `shape` stages straight into a destination (a local directory,
FTP), and the destination rotates archives by a plan of yearly, monthly,
weekly and daily copies. One run takes a list of YAML configs; a config
yields one or more profiles, a profile writes one archive.

This is a port of the Raku implementation in `../raku-bkp`, and that tree is
the spec. The config format, class names and their attributes, archive names
`<prefix>-YYYY-MM-DD-<period>.<suffix>`, the rotation, and the CLI
(`bkp <config>...`, `list`, `download`, `list-profiles`) stay compatible: the
binary replaces the old one over the same `/usr/local/etc/bkp/*.yaml` and the
archives already stored, and an archive the old version wrote stays
restorable. Every deliberate departure from the Raku behaviour is recorded in
`doc/compat.md` with its reason.

The deliverable is one static Linux binary for amd64 and arm64, copied to a
server and run from cron. Nothing else gets installed besides the external
tools a config names.

## Commands

```bash
mise install                        # Go and lint tools at pinned versions
go build ./cmd/bkp
scripts/test.sh [./pkg...] [flags]  # build, then tests under -race
scripts/test.sh ./internal/dst -run TestRotate   # one package, one test
scripts/lint.sh [paths...]          # linters on changed files, findings only
scripts/build.sh                    # static release binaries into dist/
```

## Layout

- `cmd/bkp/` — `main` and CLI dispatch only, no logic.
- `internal/config/` — YAML loading, profiles, `script`/`template` expansion.
- `internal/src/`, `internal/compress/`, `internal/crypt/`, `internal/shape/`,
  `internal/dst/` — one file per class, named after it (`tar.go`, `ftp.go`).
  Each package maps class names to constructors in one explicit table; no
  `init()` registration.
- `internal/dst/ftptest/` — in-process FTP server for tests, imported only
  from `_test.go` files.
- `internal/pipeline/` — starting, chaining, waiting for and killing stage
  processes.
- `internal/run/` — running configs and profiles: attempts, cleanup, `post`,
  exit status.
- `testdata/` next to the tests that use it.
- `doc/compat.md` — departures from `../raku-bkp`.

## Go conventions

- The Go version is pinned in `go.mod` and `mise.toml`; use the current
  standard library (`slices`, `maps`, `errors.Join`, `log/slog`, `min`/`max`,
  range over int) rather than hand-rolled equivalents.
- Code is `gofmt`ed and `golangci-lint` clean. A `//nolint` carries the
  linter name and the reason on the same line.
- Standard library first. The third-party modules approved so far are
  `go.yaml.in/yaml/v3` and `github.com/jlaffaye/ftp`. Any other one goes
  into the plan with what it replaces and waits for the operator's approval;
  once approved, it is added to the `depguard` allow list in
  `.golangci.yml`. The CLI is built on `flag`.
- `CGO_ENABLED=0` always. Signals and process groups go through `syscall`.
- Errors are returned, not panicked, and wrapped with `fmt.Errorf("…: %w")`
  carrying the profile and stage they come from. Messages are lowercase,
  without trailing punctuation. An error is either logged or returned, never
  both. `panic` is for a broken invariant only.
- No package-level mutable state besides the class tables.
- Interfaces are small and defined where they are consumed; constructors
  return concrete types.
- `context.Context` is the first parameter of anything that starts a process,
  touches the network or can block.
- Go naming: no `Get` prefix, no stutter (`src.Tar`, not `src.SrcTar`),
  receivers one or two letters, initialisms in one case (`URL`, `ID`).
- Config structs carry `yaml` tags and decode with known-field checking, so a
  typo fails the config instead of being ignored. Required attributes and
  value constraints (paths that must exist, gzip level, cipher name) are
  checked at load, before any process starts.

## Processes and pipes

This is the core of the program; most real defects will be here.

- A stage is an external process started with `exec.CommandContext` and an
  argv slice. `bash -o pipefail -c` runs only what the config states as a
  shell string: `src.script` and `post`.
- Adjacent stages are joined by a file descriptor: the stdout of one is the
  `*os.File` stdin of the next, so the data never passes through `bkp`.
  Go code sits in the data path only where a stage is implemented in Go, and
  then it streams with a bounded buffer.
- No temporary files and no archive held in memory or on disk on its way to
  the destination.
- The parent closes its copies of pipe ends after `Start`; a pipe end left
  open in the parent means the reader never sees EOF and the run hangs.
- Each stage runs in a process group of its own, killed when the stage
  exits. On a stage failure, a timeout or `SIGTERM`/`SIGINT` to `bkp`,
  every stage's group is killed and every process is waited for: no
  zombies, no orphans still writing.
- Every stage's exit status is checked, not just the last one's. An archive
  counts as written only when all stages exited 0; a partial archive is never
  left looking like a good one.
- Stage stderr is copied to stderr when `BKP_LOG` is set; either way its last
  lines are kept and go into the error when the stage fails.
- Whatever a source or destination sets up — a mount, a snapshot — is
  released after every attempt: success, failure and signal alike.
- Passwords and keys never reach the log. Where the tool can take a secret
  from the environment, a file descriptor or stdin, it gets it there rather
  than in argv, which every user on the host can read in `ps`.
- Rotation and archive naming take the current time as a parameter, never
  `time.Now()` inside, and use local dates as the Raku version does.

## Output

Progress lines go to stdout, and only when `BKP_LOG` is set, in the Raku
format `<time> bkp: [<pid>] <message>`. Error messages — a config that does
not load, a failed attempt, `BACKUP <config>:<profile> FAILED!`, a failed
`post` hook — go to stderr unconditionally.

`bkp <config>...` works through every config and every profile even when some
fail, and exits non-zero when any config failed to load, any profile ran out
of attempts or any `post` hook failed. A backup that was not taken never ends
in exit status 0. `bkp list` exits non-zero when its config does not load; the metrics
collector in `../infrastructure` (role `bkp`) relies on that and parses the
exact output of `bkp --bytes list <config>`.

## Tests

- Every package with logic has tests. Pure logic (rotation, next archive
  name, profile expansion, suffixes) is covered table-driven, edge cases
  included: an empty plan entry, a second run on the same day, a period's
  first archive, an archive name that does not parse.
- Config loading is tested against YAML in `testdata/`, including the
  examples from `../raku-bkp/README.md`.
- Process tests use fake tools: small scripts in `testdata/` put first in
  `PATH`, or the test binary re-executed as a helper process. Never the real
  `mysqldump`, a remote FTP server, the network or root. A test that checks
  compatibility with a real local tool (`openssl`, `gzip`) skips when the
  tool is missing.
- Destinations are tested against `t.TempDir()`.
- `scripts/test.sh` passes before a step is presented.

## Tasks

The port is planned as task files in `doc/tasks/`, done in number order. A
task file is the spec of its work: read it and the `../raku-bkp` sources it
names before planning its steps. The commit that completes a task deletes its
file. Decisions that depart from the Raku behaviour are in `doc/compat.md`;
a task that makes a new one adds it there.

## Workflow

The operator does not write Go and relies on the reviews below more than on
reading the diff, so the review steps are never skipped.

1. Gather what you need from this repo and `../raku-bkp`, ask about anything
   unclear, and show the plan broken into steps. A step is one self-contained
   commit: it builds, its tests pass, and it can be reverted on its own. Start
   editing only once the plan is confirmed.
2. Execute one step at a time. Leave changes unstaged in the working tree.
3. Run `scripts/test.sh` and `scripts/lint.sh`; fix what they turn up.
4. Launch `go-reviewer` and `convention-guard` in parallel. Fix every
   blocking and should-fix finding, then run step 3 again. A finding you
   disagree with goes to the operator with your reason, not silently dropped.
5. Present the step: `git diff --stat`, what changed and why, the reviewers'
   verdict (what was fixed, what nitpicks remain), and one line for each Go
   idiom in the diff that a Perl or Raku reader would not recognise. Then
   wait. Corrections from the review go into the working tree, still
   unstaged.
6. Once the operator confirms the step, commit it and move to the next. Keep
   commit messages terse — for a small change a subject line alone is enough.
7. Push only when the operator asks.

## Review

Findings are graded the same way by every reviewer:

- **blocking** — wrong behaviour, a hang, a leaked process, file descriptor
  or snapshot, a data race, a lost or silently truncated archive, a secret in
  argv or the log where the tool offers another way, an incompatibility with
  `../raku-bkp` not recorded in `doc/compat.md`, logic without a test.
- **should-fix** — an error without the context to act on it, a missing edge
  case in tests, a non-idiomatic construct with a clear idiomatic
  replacement, a needless dependency or abstraction, a convention above
  broken.
- **nitpick** — naming and small readability points.

The Go-specific checklist lives in `.claude/agents/go-reviewer.md`, the
convention checklist in `.claude/agents/convention-guard.md`.

## Delegation

Hand these to a subagent without asking, and take back only its report:

- `go-reviewer` — every step that touches `.go` files, before presenting it.
- `convention-guard` — the working tree, before presenting a step.
- `Explore` — locating something in this repo or in `../raku-bkp` when the
  path is not known.

A subagent starts with no context of the task: give it the packages or files,
what the step is meant to do, and the Raku module it ports — never a pronoun
back into this conversation. Edits, planning, the review conversation and
commits stay in the main session.
