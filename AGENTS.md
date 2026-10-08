# Agent Documentation

This file is the complete rule set for restor: what the project does, how it is built, and
the rules every change follows (architecture, errors, logging, code style, testing,
tooling, Git workflow and review). A reader of this repository alone must be able to
follow it; there is no external document to consult.

- Keywords: **MUST** = blocker if violated, **SHOULD** = fix unless there is a written
  reason, **MAY** = allowed option.
- When code and this file disagree, fix one of them in the same change.
- Known gaps between the code and these rules are listed under "Migration backlog"
  (section 17) and fixed in dedicated `refactor/` or `fix/` branches, not inside feature
  work.

---

## 1. Project Overview

restor orchestrates **restic** backups of several **projects** on one host.

- **Project:** a named set of `sources` (paths) plus the services that must be stopped while
  they are backed up. Services are managed by a **service manager**: `docker-compose`,
  `podman-compose`, `systemd` (system or user scope) or `noop` (files only, nothing to stop).
- **Per project:** lock → pre-backup hook → stop services → `restic backup --tag <project>`
  → start services → post-backup hook.
- **After all projects:** `restic forget --tag <project> <retention_policy>` per project,
  then one repository-wide `restic prune` and `restic unlock` (only with `auto_prune`).
- **Modes:** `sequential` (one project after another, sorted by name) or `parallel` (one
  goroutine per project).
- **Runtime:** a single CLI binary that runs **on the host** as a oneshot systemd service,
  triggered by a systemd timer (`example/restor.service`, `example/restor.timer`). It
  shells out to `restic`, `docker compose`, `podman compose` and `systemctl`; it has no
  server, no database and is not a container. Shipped as release archives and `.deb`/
  `.rpm` packages.

### Quick reference

| Area | Rule |
|---|---|
| Dependencies | Injected through the constructor as interfaces declared by the consumer |
| Wiring | One composition root (`internal/app`) names concrete types (`Create*` factories) |
| Global state | Forbidden (only `Err…` sentinels and linker-set build info) |
| Required deps | Validated in the constructor (`util.RequireAll`); a missing one fails startup |
| Optional deps | None; optional *settings* via functional options |
| Errors | Wrap with `%w` and context; sentinels + custom types; never swallow |
| Panic | Never; `main` turns errors into exit codes |
| Logging | `zerolog.Logger` injected, scoped with `component`; never log secrets |
| Size limits | Lines ≤ 100 chars, functions ≤ 40 lines/statements, gocyclo < 8, ≤ 5 params |
| Formatting | gofumpt + goimports (stdlib / third-party / project groups) |
| Tests | stdlib + testify + mockery, `-race`, no real commands or clock in unit tests |
| Checks | golangci-lint, deadcode, govulncheck, build, tests; same locally and in CI |
| Commits | Conventional Commits, imperative, **no AI signatures** |
| Branches | `type/description` + PR; the maintainer MAY push to `main` after `make check` |
| Releases | Tag on `main`, CI checks gate the release, goreleaser builds archives |
| Dependency updates | Renovate only; `govulncheck` in CI |

### Principles

- **One author:** code reads as if one person wrote it.
- **SOLID, the Go way:** one package = one responsibility; small interfaces; depend on
  interfaces, not concrete types; extend through interfaces, not type switches.
- **80/20 and YAGNI:** make the common case easy and the rare case possible. No abstraction
  before a second real use. "A little copying is better than a little dependency."
- **Explicit over clever:** a constant with a comment beats a config knob nobody sets;
  add knobs only when real use needs them.

---

## 2. Tools

```bash
make check                                                # ./scripts/pre-commit.sh, check-only
golangci-lint fmt --config .golangci.yml                  # gofumpt + goimports
golangci-lint run --config .golangci.yml                  # full linter set (section 12)
go run golang.org/x/tools/cmd/deadcode@v0.51.0 -test ./... # unused exported code
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...     # known vulnerabilities
go test -race ./...                                       # all unit tests, as in CI
go tool mockery                                           # regenerate mocks (.mockery.yaml)
make build                                                # ./restor with version info (ldflags)
./restor --config config.yaml [--debug]                   # run one backup cycle
./restor version                                          # build info (also --version, -v)
```

- CI: `.github/workflows/checks.yml` (push to main, PRs, reused by the release).
- Release: push a `vX.Y.Z` tag on `main`; `release.yml` runs the checks, then goreleaser
  (`.goreleaser.yaml`) builds archives (linux/darwin, amd64/arm64/armv7) that contain the
  binary, `README.md`, `LICENSE` and the templates from `example/`, and `.deb`/`.rpm`
  packages (linux only, nfpm) with the files from `packaging/`. No container image.
- Local release dry run: `goreleaser release --snapshot --clean --skip=publish` (output in
  `dist/`).
- Build info: `main.{Version,Commit,BuildDate,GoVersion}`, set via ldflags by the Makefile
  and goreleaser (`GOVERSION` comes from `release.yml`); printed by `version` and logged
  at startup.
- A real run needs `restic` and the service manager binaries on `PATH` (missing ones are
  warned about at startup); unit tests must not.
- Mocks: mockery v3 is a `tool` directive in `go.mod` (bump with
  `go get -tool github.com/vektra/mockery/v3@vX.Y.Z`); `go tool mockery` generates them
  from `.mockery.yaml` into `internal/<pkg>/mocks`. CI checks them for drift (not part of
  `make check`); regenerate after changing an interface.
- Tool versions are pinned (pinned `go run` commands, golangci-lint action `version:`,
  mockery in `go.mod`) and bumped deliberately, the same version locally and in CI.
  mockery is a `go tool` rather than a pinned `go run` because `go run` checks its ~20
  modules against sum.golang.org on every CI run, and a checksum-DB hiccup can fail the
  mock drift job.

---

## 3. Project Structure

```
cmd/main.go                main package: cobra commands, logger, signal context, shutdown
                           deadline, exit code, build info
internal/app/              composition root: config → Settings, CreateServiceManager,
                           wiring of all components, startup warnings
internal/config/           file (YAML/JSON/TOML) + env file + inline env, Defaults → expand
                           → Validate, ResolvedProjects
internal/backup/           orchestration: Manager (mode, per-project run, forget/prune/unlock);
                           declares Restic, Services, Locker, Runner
internal/restic/           restic CLI client: backup, forget, prune, unlock
internal/servicemanager/   Compose (docker/podman), Systemd, Noop; status polling
internal/lock/             flock(2) lock files per key, lock dir safety checks
internal/command/          the only os/exec user: runs a program with a fixed env and ctx
internal/util/             RequireAll/DependencyError, Clock; clocktest (fake clock)
internal/<pkg>/mocks/      mockery output (never hand-edited)
example/                   config.yaml (full reference, kept valid by a test), restor.env,
                           restor.service + restor.timer (systemd units)
packaging/                 .deb/.rpm only: system + user units (/usr/bin/restor) and the
                           install/remove scripts (nfpm in .goreleaser.yaml)
scripts/pre-commit.sh      local checks (make check)
```

Layout rules:
- `main` lives directly in `cmd/` and is built with `./cmd` (`cmd/<name>/` only if a
  second binary appears).
- `internal/` holds **all** project code; at most 3 levels below `internal/`.
- `pkg/` only for code meant for other modules, and only with user confirmation.
- Root files: `AGENTS.md`, `README.md`, `.golangci.yml`, `.goreleaser.yaml`,
  `.mockery.yaml`, `Makefile`, `renovate.json`, `scripts/pre-commit.sh`, `example/`,
  `packaging/`.

---

## 4. Dependency Direction

```
cmd/main               → internal/app, cobra, zerolog
internal/app           → config, backup, restic, servicemanager, lock, command, util
internal/backup        → util, zerolog
internal/restic        → util
internal/servicemanager → util
internal/lock          → util
internal/command       → zerolog
internal/config        → mapstructure, go-toml, yaml
internal/util          → stdlib only
```

Layers: `cmd → app (composition root) → backup (orchestration) → restic, servicemanager,
lock, command (external clients)`. Dependencies flow down only; no import cycles.

Rules:
- Only `internal/app` imports `config` and names concrete types. `backup`, `restic`,
  `servicemanager` and `lock` declare their own `Settings`/`Deps` structs; `app` maps the
  config onto them.
- `backup` talks to restic, the service managers, the locker and the hook runner only
  through interfaces it declares itself (`Restic`, `Services`, `Locker`, `Runner`).
- `restic` and `servicemanager` run their CLIs through a `Runner` interface they declare;
  `command.Runner` implements it. Only `command` imports `os/exec` (`app` uses
  `exec.LookPath` for the startup warning).
- External clients never import `config` or `backup`, and never each other.
- Nothing imports `cmd` or `internal/app`.
- Exported shared interface: `util.Clock` (used by `servicemanager`). Every
  other interface is declared by its consumer.

---

## 5. Shared State and Ownership

restor itself has no data store. What it writes, and who writes it:

- **restic repository:** one repository (`global.restic_repo`) shared by all projects.
  Snapshots are separated by the tag `<project name>`. `backup` runs per project (in
  parallel mode concurrently; restic allows concurrent backups). `forget`, `prune` and
  `unlock` run **only in `Manager`, sequentially, after all backups**, because they need
  exclusive repository locks.
- **Lock files:** `lock.Locker` holds an `flock(2)` on `<lock dir>/<sha256 of key>.lock`
  (opened with `O_NOFOLLOW`; content: PID and key, for humans only). The kernel releases
  the lock when the holder exits, so there are no stale locks; the files are never
  removed (removing one would let two processes lock different inodes of the same path).
  Two `Lock` calls of one process conflict too. The lock dir is per user (`lock.DefaultDir`):
  `/run/restor` for root on Linux, `$XDG_RUNTIME_DIR/restor` for other users, else
  `restor-<uid>` in the temp dir; it is refused unless it is a real directory owned by the
  user and not writable by others (`ErrUnsafeDir`). The key comes from the service manager: the
  compose file path, the systemd unit names joined with commas, or `noop-<project>`. The
  locks stop two runs (two processes or
  two projects) from stopping and backing up the same compose file / units at once.
  Runs of different users do not see each other's locks (rare: root and a `docker` group
  member backing up the same compose file).
- **Services:** a project stops and restarts only the services it lists (`services`), or
  all services of its compose file / its `systemd_units` when the list is empty.

---

## 6. Lifecycle

restor is a **oneshot** process: one run = one backup cycle, then exit.

### Run
1. `cmd/main`: cobra parses flags; logger = console writer on stderr, `--debug` = debug
   level; `signal.NotifyContext` for SIGINT/SIGTERM; build info is logged.
2. `app.New`: `config.Load` (file + env file + inline env, expansion, validation) and
   wiring. Any error ends the run (exit 1).
3. `Manager.Run`: run all projects (sequential or parallel); when
   every project succeeded and `global.auto_prune` is set: forget per project with a
   `retention_policy`, then one prune + unlock (only if at least one project forgot).
4. Exit code: 0 when every project backed up; 1 when config failed, any project failed or
   the run was cancelled. Forget, prune, unlock and post-backup hook failures are logged
   but do **not** change the exit code; after a failed project they are skipped.

### Failure handling per project
- Lock held by a live process → project fails.
- Pre-backup hook fails → project fails; services are not touched.
- Listing the services fails → project fails.
- Stopping services fails → warning, the backup still runs (services may be running).
- Services not stopped within `stop_timeout` → warning, the backup still runs.
- `restic backup` fails → services are restarted, project fails.
- Restarting services fails → project fails; not running within `start_timeout` →
  warning.
- Other projects always continue; each failure is logged once by `Manager` with the
  project, and `main` logs the summary (`backup: projects failed: N of M`).

### Shutdown and supervision
- SIGINT/SIGTERM cancel the run context: running commands are killed (`exec.
  CommandContext`), sequential mode starts no further project, waits end early.
- A project that stopped its services restarts them with its own context
  (`context.WithoutCancel`, bounded by `start_timeout` + 3 min).
- `main` waits at most `shutdownTimeout` (5 min) after the signal, then exits 1.
  `example/restor.service` sets `TimeoutStopSec` above that so systemd does not kill
  restor while it restarts services. No second-signal "force" mode: the deadline is the
  force.
- There is no in-process retry and no restart: systemd records the failed run, and the
  next timer run is the retry (`Persistent=true` catches up missed runs).

### Rules (MUST)
- Every goroutine has an owner that starts it and waits for it to end (`Manager` owns the
  per-project goroutines in parallel mode via `sync.WaitGroup`).
- Loops that wait (service stop/start polling) use the injected `util.Clock`; tests use
  `util/clocktest`.
- `os.Exit` only in `main`; everything else returns errors.
- Service manager commands have timeouts: status queries 30 s, stop/start the configured
  timeout + 2 min. `restic` and hooks are bounded only by the run context (a backup can
  legitimately take hours).

---

## 7. Behaviour Reference

### Configuration
- One file (`--config`, required): YAML (`.yaml`/`.yml`), JSON or TOML by extension.
  Template with every option: `example/config.yaml`.
- Keys keep their case (environment variable names are case-sensitive). Config field names
  match case-insensitively; scalars are converted loosely (`"30"` → 30, `"a,b"` → list).
  Project names are case-insensitive and used in lowercase (also as the restic tag);
  names that differ only in case are an error.
- Environment for restic, service managers and hooks = process env + `env_file`
  (`.env`, `.yaml`/`.yml`, `.json`; other extensions are tried in that order) + inline
  `environments` (highest priority). `$VAR`, `${VAR}` and `${VAR:-default}` in env values,
  global settings and project fields (paths, units, hooks, options, retention) are expanded
  once at load time from the **process** env (not from the env file); `$$` is a literal
  `$`. `.env` files: `KEY=value` per line (`export ` prefix allowed, a line without `=` is
  an error), `"…"` and `'…'` values are taken as is (single-quoted ones are not expanded),
  an unquoted value ends at ` #`.
- Validation (all problems at once, with field paths): `mode` is `sequential` (default) or
  `parallel`; per project `service_manager` and `sources` are required; `compose_file` for
  the compose managers, `systemd_units` for systemd; `systemd_scope` is `system` (default)
  or `user`; `stop_timeout`/`start_timeout` (seconds) are ≥ 0 and > 0 when services are
  stopped. Unknown keys are ignored.
- `stop_services: false` backs up without stopping anything. Hooks: global
  `pre_backup_cmd`/`post_backup_cmd` are the default of every project, overridden per
  project.
- `example/config.yaml` stays complete, commented and valid (`TestLoad_ExampleConfig`);
  every new option is added there in the same change.
- Secrets (restic password, S3 keys) come from the env file or the process env, never from
  `example/`; the real `config.yaml` and `env.yaml` are gitignored.

### Hooks
- Split on whitespace and executed directly (no shell): no quoting, pipes or redirects.
  Use a script for anything more.
- Run with the merged environment and the run context.

### Service managers
- Compose: `<docker|podman> compose -f <file> …`; services from `config --services` when
  not listed; stop with `stop -t <stop_timeout> <services>` (ignores "no containers to
  stop"), start with `up -d <services>`. Running = `ps --format json` reports the service
  with `State` `running` (Docker JSON lines or a JSON array; podman-compose via the
  `com.docker.compose.service` label).
- systemd: `systemctl [--user] stop|start <units>` (stop ignores "not running"/"not
  loaded"); state from `systemctl is-active <units>`. Stopped = no unit `active`,
  `activating`, `deactivating`, `reloading` or `refreshing`; started = all `active`.
  `services` filters `systemd_units`; if none match, all units are used.
- noop: no services; every operation succeeds.
- Stop and start poll every 500 ms until the expected state or the timeout.

### restic
- The repository is passed as `RESTIC_REPOSITORY` (not `-r`), so it never shows up in
  arguments or logs; with an empty `restic_repo` a `RESTIC_REPOSITORY` from the env is used.
- `restic backup [backup_options] --tag <project> <sources>`; the snapshot ID is parsed
  from `snapshot <id> saved` (logged; a missing ID is a warning, not an error).
- `restic forget [forget_options] --tag <project> <retention_policy split on whitespace>`.
- `restic [prune_options] prune` and `restic [prune_options] unlock`.
- Every list option is passed as one argument per item: write `["-o", "s3.connections=10"]`
  or `["--option=s3.connections=10"]`, not `["-o s3.connections=10"]`.

---

## 8. Design Decisions

- **Runs on the host, not in a container:** restor has to stop/start the host's compose
  projects and systemd units and read their data paths directly; a container would need
  the Docker/Podman socket, systemd access and every source path mounted. Therefore there
  is no `Dockerfile`, no `.dockerignore` and no image: releases ship archives and
  `.deb`/`.rpm` packages only (no `dockers_v2` in goreleaser, no ghcr login in
  `release.yml`).
- **Packages as release assets, not a repository:** the `.deb`/`.rpm` files are attached
  to the GitHub release; hosting an apt/yum repository would need an external service or
  goreleaser Pro. Packages install to `/usr/bin` (so `packaging/` has its own unit; the
  `example/` unit targets manual installs in `/usr/local/bin`), never write into
  `/etc/restor` beyond creating it 0700, do not enable the timer (a run would fail before
  the config is written), and only *recommend* `restic` (often installed upstream).
- **Oneshot + systemd timer instead of a daemon:** scheduling, logging (journal), failure
  state and catch-up of missed runs come from systemd; restor stays a plain CLI. A failed
  run exits 1 and the next timer run is the retry; there is no container runtime restart
  and no in-process restart.
- **Shell out to the CLIs instead of using libraries/APIs:** restic has no stable Go API,
  and the compose/systemctl CLIs behave the same for Docker, Podman and systemd scopes.
- **YAML, JSON and TOML config:** kept for existing users. The file is decoded directly
  (no viper) because viper lowercases map keys, which broke environment variable names.
- **Forget and prune after all backups, sequentially:** they take exclusive repository locks
  and would fail or block concurrent backups; one repository-wide prune is cheaper than one
  per project. They are skipped when a project failed, so a broken run never thins out
  the snapshot history.
- **Restart services even when the backup failed or the run was cancelled:** a failed
  backup must not leave production services down.
- **Tag = lowercase project name:** keeps each project's retention separate in a shared
  repository; lowercase keeps the tags of snapshots made by earlier versions (viper
  lowercased the names). Renaming a project starts a new snapshot series (the old one is
  no longer forgotten).
- **Sequential order = sorted by name:** the config's `projects` map has no order once
  decoded; sorting makes runs reproducible. Prefix names (`10-db`, `20-web`) to control it.
- **Repository in the environment:** `RESTIC_REPOSITORY` instead of `-r` keeps
  credentials in repository URLs out of debug logs and command errors (errors carry only
  the program name and the last 20 output lines).

Design decisions that are not obvious from the code go here, not into long code comments.

---

## 9. Architecture Rules

### 9.1 Dependency injection (MUST)
- Every dependency a type uses (restic client, service manager, locker, clock, logger,
  command runner) is passed in through its constructor. Nothing is created inside methods.
- Dependencies are **interfaces declared by the consumer**, next to the code that uses
  them, and kept small (only the methods the consumer calls).
- Exception: a package MAY export its own interface when several consumers share exactly
  the same contract; section 4 names these.

```go
// Declared in package backup, next to the code that calls it.
type Locker interface {
    Lock(key string) (release func() error, err error)
}
```

### 9.2 Composition root (MUST)
- Exactly one place wires the application: `internal/app`. It is the only code that
  names concrete types and creates them through `Create*` factory functions.

```go
func CreateServiceManager(p config.Project, d servicemanager.Deps) (backup.Services, error) {
    switch p.ServiceManager {
    case config.ManagerDockerCompose:
        return servicemanager.NewCompose(
            servicemanager.ComposeSettings{Binary: "docker", File: p.ComposeFile}, d)
    // … podman-compose, systemd, noop
    default:
        return nil, fmt.Errorf("%w: %q", ErrUnknownType, p.ServiceManager)
    }
}
```

### 9.3 Required dependencies (MUST)
- All dependencies are **required**. The constructor validates them once and returns a
  typed error naming the missing field. A nil dependency is a wiring bug and fails at
  startup, never as a nil-pointer panic later.
- No optional components hidden behind `if x != nil` checks. A feature that can be
  switched off is a separate component (e.g. the `noop` service manager).
- Missing **data** (no snapshot ID in the output) is normal and returns an explicit,
  typed answer, not a panic and not a generic error.

```go
var ErrMissingDependency = errors.New("servicemanager: missing dependency")

func NewCompose(s ComposeSettings, d Deps) (*Compose, error) {
    if err := util.RequireAll(ErrMissingDependency,
        util.Requirement{Name: "Runner", OK: d.Runner != nil},
        util.Requirement{Name: "Clock", OK: d.Clock != nil},
        util.Requirement{Name: "File", OK: s.File != ""},
    ); err != nil {
        return nil, err // *util.DependencyError naming the field, wrapping the sentinel
    }
    …
}
```

### 9.4 Functional options (MAY)
For optional **settings** with a sensible default, when there are three or more of them.
Never for dependencies (see 9.3).

### 9.5 Forbidden patterns (MUST NOT)

```go
var logger = zerolog.New(os.Stdout)   // package-level logger (also: global zerolog/log)
var instance *Service                 // singleton
var cache = NewCache()                // package-level state
var lookup = map[string]int{...}      // package-level lookup table: build it in a constructor
func (s *Service) Do() { c := NewClient() } // creating a dependency inside a method
```

Allowed package-level variables: `Err…` sentinels, and build info set by the linker
(`Version`, `Commit`, `BuildDate`, `GoVersion` in `main`, marked
`//nolint:gochecknoglobals // written by the linker at build time`).

---

## 10. Errors and Logging

### Errors (MUST)
- **Wrap with `%w`** and context, prefixed with the package:
  `fmt.Errorf("restic: backup %s: %w", project, err)`.
- **Sentinels** (`var ErrLocked = errors.New("lock: already locked")`) for conditions
  callers branch on; **custom types** (`*config.FieldError{Path, Problem}`,
  `*command.Error`, `*util.DependencyError`) when callers need data. Both work with
  `errors.Is`/`errors.As`.
- **Never swallow** an error: handle it, return it, or log it with the reason it is safe to
  continue. `_ = f()` needs a `//nolint` with explanation.
- **Log once**, at the boundary that handles the error; lower layers return, they do not
  log and return.
- Error strings: lowercase, no trailing punctuation.
- **No panic.** Startup errors are returned to `main`, which exits 1.

### Logging
- `zerolog.Logger` is created in `main`/the composition root and injected; every component
  gets `log.With().Str("component", name).Logger()` (plus `project` where it applies).
- Log with context fields, not formatted strings:
  `.Str("project", name).Err(err).Msg("backup failed")`.
- Levels: `debug` for command lines and per-step detail, `info` for lifecycle (run start,
  project done, snapshot ID), `warn` for recovered problems (services not stopped/started
  in time, stop command failed, missing binary), `error` for failures
  that need attention (project failed, forget/prune/unlock failed, post-backup hook
  failed).
- Never log secrets: no restic password, S3 keys, env file contents or repository URLs
  with credentials. Be careful when logging environments or full command lines.

---

## 11. Security

- Secrets: env vars (preferred) or the env file; both gitignored; never logged.
- Input validation at the boundary (config load), with field paths.
- External commands are executed directly with argument lists (`internal/command`,
  `exec.CommandContext`, no shell); never build a shell string from config values.
- Timeouts on service manager calls; restic and hooks are bound to the run context
  (section 6).
- Dependencies: Renovate (section 14) and `govulncheck` in CI and in the pre-commit
  script. GitHub's vulnerability alerts stay enabled; Renovate's `vulnerabilityAlerts`
  reads them (no Dependabot).

---

## 12. Code Style

### Formatting and limits (MUST, enforced by golangci-lint)
- gofumpt + goimports, import groups: stdlib, third-party, project (local prefix
  `github.com/dombyte/restor`).
- Line length ≤ 100 (tab width 4), function length ≤ 40 lines and ≤ 40 statements,
  cyclomatic complexity < 8, ≤ 5 parameters (use a struct beyond that).
- No magic numbers outside constants.
- Linters (`.golangci.yml`, golangci-lint v2): revive (exported, package-comments,
  function-length 40/40, argument-limit 5, error-strings, var-naming, indent-error-flow,
  unreachable-code, redefines-builtin-id), staticcheck, govet, unused, errcheck (type
  assertions + blank), errorlint, gosec, gocyclo (min-complexity 7), dupl (100), mnd, lll
  (100), misspell, unconvert, perfsprint, whitespace, importas, goprintffuncname,
  ineffassign, wastedassign, durationcheck, makezero, tparallel, copyloopvar, intrange,
  bodyclose, rowserrcheck, sqlclosecheck, gochecknoglobals, forbidigo (`panic`),
  nolintlint (explanation + specific). Tests are excluded from revive, dupl, errcheck,
  gosec, lll, gochecknoglobals, forbidigo, mnd.

### Naming

| Element | Convention | Example |
|---|---|---|
| Packages | lowercase, singular, compound allowed, never plural | `servicemanager` |
| Files | lowercase, underscores | `compose.go`, `compose_test.go` |
| Exported | PascalCase | `NewCompose`, `ProjectSettings` |
| Unexported | camelCase | `waitUntil`, `parseComposePs` |
| Receivers | one letter, consistent per type | `m *Manager`, `c *Compose` |
| Acronyms | stdlib style | `ID`, `URL`, `PID`, `snapshotID` |
| Errors | `Err…` sentinels, `…Error` types | `ErrLocked`, `FieldError` |
| Functions | verb + noun | `stopServices`, `parseSnapshotID` |

### Documentation
- Package doc and doc comments on exported types: MUST. On exported functions: SHOULD.
- Doc comments start with the name and are brief; full sentences allowed.
- Inline comments explain **why**, not what (unless the what is non-obvious).

---

## 13. Testing

- stdlib `testing` + testify (`require` for preconditions, `assert` for checks) + mockery.
- Every interface has a mockery mock, generated into `<pkg>/mocks` from `.mockery.yaml`;
  mocks are never hand-edited and CI fails if they drift.
- Table-driven tests with named cases; `t.Parallel()` where there is no shared state.
- Unit tests use no real commands (`restic`, `docker`, `podman`, `systemctl`), no network,
  no real clock and no sleeps: a fake command runner, a fake `Clock`, mocks, `t.TempDir()`
  for lock files. Tests that need real tools are integration tests behind
  `//go:build integration` and are not part of `go test ./...`.
- Tests run with `-race` locally and in CI.
- New code comes with tests; bug fixes come with a regression test (failing first).
- Coverage (SHOULD): ≥ 70 % per package with logic, ≥ 80 % for packages with ≥ 5 functions
  of complexity ≥ 5; pure wiring (`cmd`, `app`) is covered by a startup test instead.

---

## 14. CI, Releases and Dependency Updates

| File | Must do |
|---|---|
| `.golangci.yml` | v2 config, linters from section 12, formatters gofumpt + goimports |
| `scripts/pre-commit.sh` | check-only (never rewrites or stages): fmt diff, lint, deadcode, govulncheck, build, `go test -race` |
| `Makefile` | `build` (ldflags build info), `check` (runs the script), `clean`, `run` |
| `.github/workflows/checks.yml` | on push to main, PRs and `workflow_call`: tidy check, build, race tests, deadcode, govulncheck, mock drift, golangci-lint; a `gate` job skips them on a push to main that is a PR's merge commit (15.5); `concurrency` cancels superseded PR runs only |
| `.github/workflows/release.yml` | on `vX.Y.Z` / `vX.Y.Z-*` tags: a `guard` job verifies the exact tag format and that the tag is on `main`, then reuses `checks.yml` (granted `pull-requests: read` for the gate); passes `GOVERSION` to goreleaser; `concurrency` runs one release per tag, never cancelled |
| `.goreleaser.yaml` | `go mod verify` only (never rewrites), `-trimpath`, reproducible (`CommitDate`), `goamd64: v1`, grouped changelog, archives + nfpm `.deb`/`.rpm` (`mtime` = `CommitDate`), no image (section 8) |
| `.gitignore` | never commit local config (`config.yaml`, `env.yaml`), secrets or build output |
| `renovate.json` | fitted to the project (below), semantic commits, grouped by manager |

### Releases
- SemVer tags `vX.Y.Z` without leading zeros, pre-releases `vX.Y.Z-alphaN`, `-betaN`,
  `-rcN` (or `-rc.N`); tags only on commits on `main`. Other tags fail the guard job.
- The changelog is built from commit subjects (section 15.2); pre-releases compare against
  the last stable tag.
- Build info (`Version`, `Commit`, `BuildDate`, `GoVersion`) is set via ldflags and shown
  by the `version` subcommand / `--version` flag and in the startup log.

### Renovate
Renovate is the only dependency updater (no Dependabot). Its config is written for this
project:
- `enabledManagers` lists exactly the ecosystems restor has: `gomod`, `github-actions`,
  plus `custom.regex` for the regex managers below.
- Every pinned version in the repo is tracked. Versions no built-in manager sees get a
  `customManagers` regex: pinned `go run` commands in workflows, scripts and this file,
  and the golangci-lint `version:` of the CI action.
- Updates that need manual work (Go module majors = new module path, golangci-lint majors
  = config migration) are disabled with a `description` saying why.
- Text that only describes a pinning pattern must not match a regex manager (write
  "pinned `go run` commands", not a literal tool-at-version string).
- A change that adds a new pinned version extends `renovate.json` in the same change.

---

## 15. Git Workflow

### 15.1 Branches
- Changes go through a PR from a `type/description` branch (`feat/…`, `fix/…`, `docs/…`,
  `refactor/…`, `chore/…`). The maintainer MAY commit or merge directly into `main`
  (ruleset bypass, 15.5) after `make check` passes; CI then runs on the pushed commit.
- Merge: regular merge by default; squash or rebase only with user confirmation.
- Only rewrite history that is not on any remote.

### 15.2 Commits (MUST)
[Conventional Commits](https://www.conventionalcommits.org):

```
<type>[(<scope>)][!]: <imperative, lowercase subject>
```

| Type | Use for | Changelog group |
|---|---|---|
| `feat` | new or changed user-visible behaviour | Features |
| `fix` | bug fixes, incl. security hardening | Bug fixes |
| `perf` | performance improvements | Performance |
| `refactor` | restructuring without behaviour change | Refactoring |
| `docs` | README, AGENTS.md, comment-only changes | Documentation |
| `chore`, `build`, `ci`, `test`, `style` | tooling, CI, tests, formatting | Maintenance |
| `<type>(deps)` | dependency and toolchain bumps | Dependencies |

- `!` before the colon marks a breaking change for users upgrading (config keys, env
  prefix, CLI flags, exit codes); a `BREAKING CHANGE:` footer alone is not enough (the
  changelog reads only the subject).
- Subject: imperative, lowercase first word, no trailing period, ≤ 72 characters.
- Merge commits keep git's default message.
- **No AI signatures**: no `Co-Authored-By:` AI trailer, no "generated by" line, in commits,
  PR descriptions or release notes.

Scopes used in restor:

`config`, `backup`, `restic`, `compose`, `systemd`, `lock`, `hooks`, `cli`, `release`, `ci`,
`deps`

```
feat(compose): support compose profiles
fix(lock): key noop projects by project name
refactor(backup): inject restic and service manager through interfaces
feat(config)!: rename env prefix to RESTOR_
```

### 15.3 Review
- The required CI checks MUST be green before a PR is merged. Approvals are set in the
  ruleset: 0 while the maintainer is the only reviewer, otherwise 1; the author MAY merge
  after approval.

### 15.4 Batching
Several small changes MAY share one PR, so they cost one review and few CI runs while each
change keeps its own branch and merge commit:

1. Create one batch branch from `main` (e.g. `refactor/migration-backlog`).
2. Each change gets its own local `type/description` branch cut from the batch branch. Run
   `make check` before merging it back with `git merge --no-ff`. These branches are never
   pushed.
3. Push the batch branch once, when every change is merged; open one PR and merge it
   (regular merge) once CI is green.

### 15.5 Branch protection (MUST)
`main` is protected by a repository ruleset (not classic branch protection):

- Rules: block deletion and force pushes, require signed commits, require a pull request,
  require the `checks.yml` job names as status checks with "branches must be up to date"
  on.
- Bypass list: only the Repository admin role, mode "always". Bots (Renovate) and other
  contributors are never on the bypass list.
- `checks.yml` runs on every PR, every direct push to `main` and every release tag, but not
  again on the merge commit of a PR: a `gate` job asks the API
  (`repos/{repo}/commits/{sha}/pulls`) whether the pushed commit is a PR's
  `merge_commit_sha`; if so the other jobs are skipped, and if the API call fails they
  run. This is safe only because "up to date" is on.

---

## 16. Review Criteria and Checklists

**Blocker (reject):**
- Does not build, tests fail, lint or format findings, gosec findings
- Global state, panic, concrete types where an interface belongs, dependency created inside
  a method
- Swallowed errors
- New code without tests, drifted mocks
- AI signatures in commits or PRs; non-conventional commit subjects
- Secrets in code, config, `example/` or logs

**Must fix:**
- Violated size limits (enforced by lint anyway)
- Missing package/type docs
- Breaking change without `!`
- A rule in this file that no longer matches the code

**Should fix:**
- Duplication, deep nesting (> 4 levels), unclear names, missing function docs, coverage
  below the target in section 13

### New feature
```
[ ] Branch feat/…; Conventional Commit subjects, no AI signatures
[ ] Dependencies injected as consumer-declared interfaces; wired only in the composition root
[ ] Required deps validated in the constructor; no optional components
[ ] No global state, no panic, errors wrapped with %w and context
[ ] Logger injected and scoped with component
[ ] Goroutines owned and awaited; Clock injected for waiting loops; commands bound to ctx
[ ] Size limits: lines ≤ 100, functions ≤ 40, gocyclo < 8, ≤ 5 params
[ ] Package/type docs; AGENTS.md updated (structure, dependency direction, behaviour, decisions)
[ ] example/config.yaml and README updated for new options
[ ] mockery mocks regenerated; tests incl. -race; no real commands or clock in unit tests
[ ] make check passes
```

### Bug fix
```
[ ] Reproduce with a failing test first
[ ] Fix the root cause, not the symptom
[ ] Error wrapped with context; logged once at the handling boundary
[ ] Regression test passes with -race
[ ] Branch fix/…; commit fix(scope): …
[ ] make check passes
```

### Adding a service manager
1. New file in `servicemanager` with a `…Settings` struct and a constructor that takes
   `Deps` (Runner, Clock) and validates them; it implements `backup.Services`: `LockKey`
   (unique per managed unit set), `Services`, and `Stop`/`Start` that wait with
   `waitUntil` and return false when the timeout passes.
2. Config: add the `Manager…` constant, its fields in `ProjectConfig`/`Project`,
   validation in `validator.serviceManager` (and `stopsServices` if it stops services), and
   a commented example in `example/config.yaml`.
3. `internal/app`: add the `case` in `CreateServiceManager` and its binary in
   `warnMissingBinaries`. Nothing else may reference the new type.
4. Document the commands it runs in section 7.
5. Tests with the mocked runner and `clocktest` only (no real tools).
6. README: user-facing documentation.

---

## 17. Migration Backlog

Known gaps between the code and the rules above, in suggested order. Each item is its own
`refactor/…` or `fix/…` branch; update this list when an item is done.

None at the moment: the migration to these rules is complete.
