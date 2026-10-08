# Agent Documentation

This file is the complete rule set for restor: what the project does, how it is built, and
the rules every change follows (architecture, errors, logging, code style, testing,
tooling, Git workflow and review). A reader of this repository alone must be able to
follow it; there is no external document to consult.

- Keywords: **MUST** = blocker if violated, **SHOULD** = fix unless there is a written
  reason, **MAY** = allowed option.
- When code and this file disagree, fix one of them in the same change.
- **Current state:** the code predates these rules. The gaps are listed under
  "Migration backlog" (section 17); new code follows the rules, and existing code is
  migrated in dedicated `refactor/` branches (with user confirmation), not inside feature
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
- **Modes:** `sequential` (one project after another) or `parallel` (one goroutine per
  project).
- **Runtime:** a single CLI binary that runs **on the host** as a oneshot systemd service,
  triggered by a systemd timer (`example/restor.service`, `example/restor.timer`). It
  shells out to `restic`, `docker compose`, `podman compose` and `systemctl`; it has no
  server, no database and is not a container. Shipped as release archives only.

### Quick reference

| Area | Rule |
|---|---|
| Dependencies | Injected through the constructor as interfaces declared by the consumer |
| Wiring | One composition root names concrete types (`Create*` factories) |
| Global state | Forbidden (only `Err…` sentinels and linker-set build info) |
| Required deps | Validated in the constructor; a missing one fails startup with a typed error |
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
make build                                                # ./restor with version info (ldflags)
./restor --config config.yaml [--debug]                   # run one backup cycle
./restor version                                          # build info (also --version)
```

- CI: `.github/workflows/checks.yml` (push to main, PRs, reused by the release).
- Release: push a `vX.Y.Z` tag on `main`; `release.yml` runs the checks, then goreleaser
  (`.goreleaser.yaml`) builds archives (linux/darwin, amd64/arm64/armv7) that contain the
  binary, `README.md`, `LICENSE` and the templates from `example/`. No container image.
- Build info is injected into `github.com/dombyte/restor/cmd.{Version,GitCommit,BuildDate}`
  (Makefile and goreleaser); see Migration backlog for the move to `main`.
- A real run needs `restic` and the service manager binaries on `PATH`; unit tests must not.
- There is no mockery setup yet (see Migration backlog). Once added, mockery v3 is a
  `tool` directive in `go.mod` (`go get -tool github.com/vektra/mockery/v3@vX.Y.Z`),
  mocks are generated with `go tool mockery` from `.mockery.yaml`, and CI checks them for
  drift (not part of `make check`).
- Tool versions are pinned (pinned `go run` commands, golangci-lint action `version:`,
  mockery in `go.mod`) and bumped deliberately, the same version locally and in CI.
  mockery is a `go tool` rather than a pinned `go run` because `go run` checks its ~20
  modules against sum.golang.org on every CI run, and a checksum-DB hiccup can fail the
  mock drift job.

---

## 3. Project Structure

```
main.go                  calls cmd.Execute()
cmd/                     cobra root + version command, flags, logger, signals; wiring
config/                  Config structs (viper/mapstructure), LoadConfig, env files, ToProjects
backup/                  Manager (mode, forget, prune), ProjectBackup (one project), Locker
  restic/                restic CLI wrapper: backup, forget, prune, unlock
  servicemanager/        ServiceManager interface, Factory; docker compose, podman compose,
                         systemd, noop implementations
example/                 config.yaml (full reference), restor.env (env file template),
                         restor.service + restor.timer (systemd units)
scripts/pre-commit.sh    local checks (make check)
```

Target layout, reached through the migration backlog:

```
cmd/main.go                main package: flags, logger, signal context, exit code only
internal/app/              composition root: Create* factories, run, exit code
internal/config/           YAML + env files, Defaults() → Validate() without side effects
internal/backup/           orchestration: manager (mode, forget/prune), project run, locker
internal/restic/           restic CLI client
internal/servicemanager/   service manager contract; compose (docker/podman), systemd, noop
internal/<pkg>/mocks/      mockery output
```

Layout rules:
- `main` lives directly in `cmd/` and is built with `./cmd` (`cmd/<name>/` only if a
  second binary appears).
- `internal/` holds **all** project code; at most 3 levels below `internal/`.
- `pkg/` only for code meant for other modules, and only with user confirmation.
- Root files: `AGENTS.md`, `README.md`, `.golangci.yml`, `.goreleaser.yaml`, `Makefile`,
  `renovate.json`, `scripts/pre-commit.sh`, `example/`.

---

## 4. Dependency Direction

Current imports:

```
main                  → cmd
cmd                   → backup, config, cobra, zerolog
backup                → backup/restic, backup/servicemanager, config, zerolog
backup/restic         → zerolog
backup/servicemanager → zerolog
config                → viper, yaml, zerolog
```

Layers: `cmd / composition root → backup (orchestration) → restic, servicemanager
(external clients)`. Dependencies flow down only; no import cycles.

Rules:
- `restic` and `servicemanager` are external-client wrappers: stdlib, `os/exec`, an injected
  logger (and later a `Clock`) only. They never import `config` or `backup`, and never
  each other.
- `backup` talks to restic and to service managers only through interfaces it declares
  itself; the concrete types are chosen by the composition root.
- Nothing imports `main`/`cmd`/`internal/app`.
- Target: only the composition root imports `config`; `backup`, `restic` and
  `servicemanager` declare their own `Settings` structs, mapped from config by the root.
  This keeps the packages independent of the config file format.
- Exported shared interface: `servicemanager.ServiceManager` (one contract implemented by
  every manager). Every other interface is declared by its consumer.

---

## 5. Shared State and Ownership

restor itself has no data store. What it writes, and who writes it:

- **restic repository:** one repository (`global.restic_repo`) shared by all projects.
  Snapshots are separated by the tag `<project name>`. `backup` runs per project (in
  parallel mode concurrently; restic allows concurrent backups). `forget`, `prune` and
  `unlock` run **only in `Manager`, sequentially, after all backups**, because they need
  exclusive repository locks.
- **Lock files:** `Locker` owns `/tmp/restor-locks/<key>.lock` (content: PID and Unix time).
  The key comes from the service manager: the compose file path, the systemd unit names,
  or `noop`. A lock is valid while its PID is alive; stale locks are removed at startup and
  when checked. The locks stop two runs (two processes or two projects) from stopping and
  backing up the same compose file / units at the same time.
- **Services:** a project stops and restarts only the services it lists (`services`), or
  all services of its compose file / its `systemd_units` when the list is empty.

---

## 6. Lifecycle

restor is a **oneshot** process: one run = one backup cycle, then exit.

### Run
1. cobra parses flags; logger = console writer on stderr, `--debug` = debug level.
2. `config.LoadConfig` (file + env file + inline env) and `ToProjects` (defaults and
   per-project validation). Any error ends the run (exit 1).
3. `Manager.Run`: clean up stale locks, run all projects (sequential or parallel), then
   forget per project and one prune + unlock (only with `global.auto_prune` and at least
   one `retention_policy`).
4. Exit code: 0 when every project backed up; 1 when config failed or any project failed.
   Forget, prune, unlock and post-backup hook failures are logged but do **not** change the
   exit code.

### Failure handling per project
- Pre-backup hook fails → project fails; services are not touched.
- Stopping services fails → logged, the backup still runs (services may be running).
- Services not stopped within `stop_timeout` → warning, the backup still runs.
- `restic backup` fails → services are restarted, project fails.
- Restarting services fails → project fails.
- Other projects always continue; errors are collected and reported at the end.

### Shutdown and supervision
- SIGINT/SIGTERM cancel the run context. Sequential mode stops before the next project;
  waits (service stop polling) end early. **Current behaviour:** the external commands are
  not bound to the context, so a running `restic`/compose/`systemctl` call finishes first
  (see Migration backlog).
- There is no in-process retry and no restart: systemd records the failed run, and the
  next timer run is the retry (`Persistent=true` catches up missed runs).

### Rules (MUST)
- Every goroutine has an owner that starts it and waits for it to end (`Manager` owns the
  per-project goroutines in parallel mode; use `sync.WaitGroup`/`errgroup`, not
  sleep-polling).
- Loops that wait (service stop/start polling) use an injected `Clock` so tests can drive
  time; capture `now` once per iteration and derive everything from it.
- `main` builds the signal context with `signal.NotifyContext` (SIGINT, SIGTERM). After
  cancellation, a project that already stopped its services restarts them with a
  separate, bounded context. The whole shutdown has one hard deadline; after it, `main`
  exits 1. No second-signal "force" mode: the deadline is the force.
- `log.Fatal`/`os.Exit` only in `main`; everything else returns errors.
- Every external command has a timeout (context or explicit deadline).

Target shape of `main`:

```go
func main() { os.Exit(run()) }

func run() int {
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    a, err := app.New(cfg, log) // wires everything, starts nothing
    if err != nil {
        log.Error().Err(err).Msg("startup failed")
        return 1
    }
    if err := a.Run(ctx); err != nil { // one backup cycle; bounded cleanup on cancel
        log.Error().Err(err).Msg("backup run failed")
        return 1
    }
    return 0
}
```

---

## 7. Behaviour Reference

### Configuration
- One file (`--config`, required), read by viper: YAML, JSON or TOML by extension. Template
  with every option: `example/config.yaml`.
- Environment for restic, service managers and hooks = process env + `env_file`
  (`.env`, `.yaml`/`.yml`, `.json`) + inline `environments` (highest priority). `${VAR}` in
  inline env values and in all project fields (paths, repo, units, hooks, options) is
  expanded once at load time from the **process** env (not from the env file).
- Required per project: `service_manager`, `sources`, `stop_timeout`, `start_timeout`
  (seconds); `compose_file` for the compose managers, `systemd_units` for systemd;
  `systemd_scope` is `system` (default) or `user`.
- `stop_services: false` backs up without stopping anything. Hooks: global
  `pre_backup_cmd`/`post_backup_cmd`, overridden per project.
- Secrets (restic password, S3 keys) come from the env file or the process env, never from
  `example/`; the real `config.yaml` and `env.yaml` are gitignored.

### Configuration rules (target)
- Order: `Defaults()` → file → env → `Validate()`. Validation runs once at startup and
  returns **all** problems with the field path (`projects.web.stop_timeout: must be > 0`).
- An invalid env override is an error, never silently ignored.
- Rules owned by another package (e.g. which service managers exist) MAY be injected into
  validation by the composition root, so config stays free of domain imports.
- `example/config.yaml` stays complete and commented; every new option is added there in
  the same change.

### Hooks
- Split on whitespace and executed directly (no shell): no quoting, pipes or redirects;
  `${VAR}` is only expanded at config load (see above). Use a script for anything more.
- Run with the project environment and the run context.

### Service managers
- Compose: `docker compose -f <file>` / `podman compose -f <file>`; services from
  `config --services` when not listed; stop with `stop -t <stop_timeout>`, start with
  `up -d <services>`.
- systemd: `systemctl [--user] stop|start <units>`, waits for the units to reach the
  expected state within the timeout.
- noop: every operation succeeds and does nothing.
- "Is it still running?" is detected from `ps`/status text (`Up`, `Running`,
  `active (running)`).

### restic
- `restic -r <repo> backup [backup_options] --tag <project> <sources>`; the snapshot ID is
  parsed from `snapshot <id> saved` in the output (logged; missing ID is not an error).
- `forget [forget_options] --tag <project> <retention_policy split on spaces>`.
- `prune` uses `global.prune_options` as global restic options.

---

## 8. Design Decisions

- **Runs on the host, not in a container:** restor has to stop/start the host's compose
  projects and systemd units and read their data paths directly; a container would need
  the Docker/Podman socket, systemd access and every source path mounted. Therefore there
  is no `Dockerfile`, no `.dockerignore` and no image: releases ship archives only (no
  `dockers_v2` in goreleaser, no ghcr login in `release.yml`).
- **Oneshot + systemd timer instead of a daemon:** scheduling, logging (journal), failure
  state and catch-up of missed runs come from systemd; restor stays a plain CLI. A failed
  run exits 1 and the next timer run is the retry; there is no container runtime restart
  and no in-process restart.
- **Shell out to the CLIs instead of using libraries/APIs:** restic has no stable Go API,
  and the compose/systemctl CLIs behave the same for Docker, Podman and systemd scopes.
- **Forget and prune after all backups, sequentially:** they take exclusive repository locks
  and would fail or block concurrent backups; one repository-wide prune is cheaper than one
  per project.
- **Restart services even when the backup failed:** a failed backup must not leave
  production services down.
- **Tag = project name:** keeps each project's retention separate in a shared repository.
  Renaming a project starts a new snapshot series (the old one is no longer forgotten).

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
type Snapshotter interface {
    Backup(ctx context.Context, tag string, sources []string) (string, error)
}
```

### 9.2 Composition root (MUST)
- Exactly one place wires the application (`cmd` today, `internal/app` in the target
  layout). It is the only code that names concrete types and creates them through
  `Create*` factory functions.

```go
func CreateServiceManager(s ProjectSettings, log zerolog.Logger) (backup.Services, error) {
    switch s.ServiceManager {
    case "docker-compose":
        return servicemanager.NewDockerCompose(s.Compose, log)
    case "systemd":
        return servicemanager.NewSystemd(s.Systemd, log)
    case "noop":
        return servicemanager.NewNoop(), nil
    default:
        return nil, fmt.Errorf("app: service manager %q: %w", s.ServiceManager, ErrUnknownType)
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
var ErrMissingDependency = errors.New("backup: missing dependency")

func NewProjectBackup(d Deps) (*ProjectBackup, error) {
    missing := []string{}
    if d.Restic == nil {
        missing = append(missing, "Restic")
    }
    if d.Services == nil {
        missing = append(missing, "Services")
    }
    if d.Clock == nil {
        missing = append(missing, "Clock")
    }
    if len(missing) > 0 {
        return nil, fmt.Errorf("%w: %s", ErrMissingDependency, strings.Join(missing, ", "))
    }
    return &ProjectBackup{d: d}, nil
}
```

Move this check into a small shared helper once a second constructor needs it.

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
  callers branch on; **custom types** (`*ValidationError{Field, Err}`) when callers need
  data. Both work with `errors.Is`/`errors.As`.
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
  project done, snapshot ID), `warn` for recovered problems (services not stopped in time,
  forget/prune failure), `error` for failures that need attention.
- Never log secrets: no restic password, S3 keys, env file contents or repository URLs
  with credentials. Be careful when logging environments or full command lines.

---

## 11. Security

- Secrets: env vars (preferred) or the env file; both gitignored; never logged.
- Input validation at the boundary (config load), with field paths.
- External commands are executed directly with argument lists (`exec.Command`, no shell);
  never build a shell string from config values.
- Timeouts on every external call (context with deadline).
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
| Files | lowercase, underscores | `docker_compose.go`, `docker_compose_test.go` |
| Exported | PascalCase | `NewRestic`, `ProjectBackup` |
| Unexported | camelCase | `waitForStopped`, `lockPath` |
| Receivers | one letter, consistent per type | `m *Manager`, `p *ProjectBackup` |
| Acronyms | stdlib style | `ID`, `URL`, `PID`, `snapshotID` |
| Errors | `Err…` sentinels, `…Error` types | `ErrLocked`, `ValidationError` |
| Functions | verb + noun | `StopServices`, `ParseSnapshotID` |

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
| `.goreleaser.yaml` | `go mod verify` only (never rewrites), `-trimpath`, reproducible (`CommitDate`), `goamd64: v1`, grouped changelog, archives only (section 8) |
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
1. New file in `servicemanager` implementing `ServiceManager` (services, stop, start,
   status, lock key); the lock key must be unique per managed unit set.
2. Config: add the type constant, its fields in `ProjectConfig`, validation, and a
   commented example in `example/config.yaml`.
3. Composition root / factory: add the `case`. Nothing else may reference the new type.
4. Document the commands it runs in section 7.
5. Tests with a fake command runner only (no real Docker, Podman or systemd).
6. README: user-facing documentation.

---

## 17. Migration Backlog

Known gaps between the current code and the rules above, in suggested order. Each item is
its own `refactor/…` or `fix/…` branch; update this list when an item is done.

1. **Make `make check` green:** `golangci-lint fmt` (1 file), 89 lint findings (mostly
   `lll`, `gocyclo`, `gochecknoglobals`, `perfsprint`, `mnd`), and deadcode
   (`servicemanager.GetBinaryPath`, `CheckRequiredBinary`).
2. **Bug – sequential order:** `projects` is a map, so `ToProjects` returns them in random
   order although `sequential` promises config order. Make the order deterministic (sorted
   by name, or a list in config) and document it.
3. **Bug – shared `noop` lock:** every `noop` project uses the lock key `noop`, so in
   parallel mode only one of them can run; key noop projects by project name.
4. **Context for external commands:** `restic`, compose and `systemctl` use `exec.Command`
   without the context. Use `exec.CommandContext` with timeouts, and restart stopped
   services with a separate, bounded context after cancellation.
5. **Tests:** there are none. Add table-driven tests for config (env merge, defaults,
   validation), snapshot ID parsing, the project run sequence (with mocked restic and
   service manager) and the locker (`t.TempDir()`).
6. **Dependency injection:** `backup` creates restic wrappers, service managers
   (`servicemanager.NewFactory()` inside `ProjectBackup.Run`) and the locker itself. Declare
   small consumer interfaces in `backup`, build the concrete types in `Create*` factories in
   the composition root, add mockery (`go get -tool github.com/vektra/mockery/v3@…`),
   `.mockery.yaml`, the mocks, and the "Mocks up to date" step in `checks.yml`
   (`go tool mockery` + `git diff --exit-code -- '*/mocks/*'`).
7. **Required dependencies:** `NewRestic`, `NewDockerCompose`, … take an optional variadic
   logger and fall back to the global `log.Logger`; make the logger required and scope it
   with `component`. Remove all uses of the global `zerolog/log`.
8. **Globals in `cmd`:** `rootCmd`, `versionCmd`, flag variables and `init()` → build the
   commands in a function. Move build info to `main` as `Version`, `Commit`, `BuildDate`,
   `GoVersion` with `//nolint:gochecknoglobals`, then update the ldflags: `Makefile`
   (`GOVERSION := $(shell go version | awk '{print $$3}')`, `-X main.…`, build `./cmd`)
   and `.goreleaser.yaml` (`main: ./cmd`, `-X main.GoVersion={{ envOrDefault "GOVERSION"
   "unknown" }}`; `release.yml` already exports `GOVERSION`). Log the build info at
   startup.
9. **Lifecycle (section 6):** `signal.NotifyContext`, `run() int` with exit code instead
   of `cobra.CheckErr`/`os.Exit` in `PersistentPreRun`; one hard deadline for the cleanup
   after a signal.
10. **Errors:** package-prefixed, wrapped messages; sentinels/typed errors for "already
    locked" and validation; no swallowed errors (`os.Remove` of stale locks, deferred
    `Unlock`); log once at the handling boundary (restic and `ProjectBackup` currently both
    log and return).
11. **Config (section 7):** validation of all fields at load time with field paths
    (today some checks run in `ToProjects`, some in `servicemanager`); `Defaults()` step;
    the leftover `DOCKER_BACKUP_` env prefix: remove or rename to `RESTOR_` (breaking, `!`);
    decide whether JSON/TOML support stays (then record it in Design Decisions) or config
    becomes YAML only (breaking, `!`).
12. **Injected `Clock`:** `waitForStopped`/`waitForRunning` poll with `time.Now` and
    `time.After`; the locker writes `time.Now()`.
13. **Status detection:** "is running" matches output text; use `docker compose ps --format
    json` / `systemctl is-active` instead.
14. **Layout:** move to `cmd/` + `internal/…` (section 3 target), with the composition
    root in `internal/app` and per-package `Settings` structs.
