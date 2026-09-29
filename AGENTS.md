# Agent Documentation

## Standard

This project follows the **Go Project Standard v3.0**
(local reference: `/home/dom/Dokumente/Git/go-project-standard.md`; will be replaced by a URL).
That document holds the rules for every Go project: dependency injection, composition root,
errors, logging, lifecycle, code style, testing, tooling, Git workflow and review criteria.

This file holds only what is specific to restor. It wins for project-specific questions;
a deviation from a MUST rule of the standard is only valid if it is listed under
"Deviations" below with its reason. When code and this file disagree, fix one of them in
the same change.

**Current state:** the code predates standard v3. The gaps are listed under
"Migration backlog"; new code follows the standard, and existing code is migrated in
dedicated `refactor/` branches (with user confirmation), not inside feature work.

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

---

## 2. Tools

```bash
make check                                                # ./scripts/pre-commit.sh, check-only
golangci-lint fmt --config .golangci.yml                  # gofumpt + goimports
golangci-lint run --config .golangci.yml                  # full linter set from the standard
go run golang.org/x/tools/cmd/deadcode@v0.50.0 -test ./... # unused exported code
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
- There is no mockery setup yet (see Migration backlog); once added, mocks are generated
  with `go run github.com/vektra/mockery/v2@v2.53.7` from `.mockery.yaml` and checked for
  drift in CI.

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

Target layout (standard 8.3), reached through the migration backlog:

```
cmd/restor/main.go         flags, logger, signal context, exit code only
internal/app/              composition root: Create* factories, run, exit code
internal/config/           YAML + env files, Defaults() → Validate() without side effects
internal/backup/           orchestration: manager (mode, forget/prune), project run, locker
internal/restic/           restic CLI client
internal/servicemanager/   service manager contract; compose (docker/podman), systemd, noop
internal/<pkg>/mocks/      mockery output
```

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

Rules:
- `restic` and `servicemanager` are external-client wrappers: stdlib, `os/exec`, an injected
  logger (and later a `Clock`) only. They never import `config` or `backup`, and never
  each other.
- `backup` talks to restic and to service managers only through interfaces it declares
  itself; the concrete types are chosen by the composition root.
- Nothing imports `main`/`cmd`/`internal/app`.
- Target (standard 3.2): only the composition root imports `config`; `backup`, `restic` and
  `servicemanager` declare their own `Settings` structs, mapped from config by the root.

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
  the Docker/Podman socket, systemd access and every source path mounted.
- **Oneshot + systemd timer instead of a daemon:** scheduling, logging (journal), failure
  state and catch-up of missed runs come from systemd; restor stays a plain CLI.
- **Shell out to the CLIs instead of using libraries/APIs:** restic has no stable Go API,
  and the compose/systemctl CLIs behave the same for Docker, Podman and systemd scopes.
- **Forget and prune after all backups, sequentially:** they take exclusive repository locks
  and would fail or block concurrent backups; one repository-wide prune is cheaper than one
  per project.
- **Restart services even when the backup failed:** a failed backup must not leave
  production services down.
- **Tag = project name:** keeps each project's retention separate in a shared repository.
  Renaming a project starts a new snapshot series (the old one is no longer forgotten).

---

## 9. Deviations from the Standard

Accepted deviations (meant to stay):

| Rule | Deviation | Reason |
|---|---|---|
| 10: `Dockerfile`, `Dockerfile.goreleaser`, `.dockerignore`, multi-arch images on ghcr.io | None of these; releases ship archives only (no `dockers_v2`, no ghcr login in `release.yml`) | restor runs on the host (see Design Decisions); an image would not be usable |
| 4.3: container runtime restarts the process | systemd timer runs it again | restor is a oneshot job, not a long-running service |

Everything else in the code that does not match the standard is a backlog item below, not
an accepted deviation.

---

## 10. Migration Backlog

Known gaps between the current code and standard v3, in suggested order. Each item is its
own `refactor/…` or `fix/…` branch; update this list when an item is done.

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
   the composition root, add `.mockery.yaml`, mocks and the mock-drift step in CI.
7. **Required dependencies:** `NewRestic`, `NewDockerCompose`, … take an optional variadic
   logger and fall back to the global `log.Logger`; make the logger required and scope it
   with `component`. Remove all uses of the global `zerolog/log`.
8. **Globals in `cmd`:** `rootCmd`, `versionCmd`, flag variables and `init()` → build the
   commands in a function. Move build info to `main` as `Version`, `Commit`, `BuildDate`,
   `GoVersion` with `//nolint:gochecknoglobals`, then update the ldflags in `Makefile`
   and `.goreleaser.yaml` (and add the `GOVERSION` step to `release.yml` as in the
   reference repos). Log the build info at startup.
9. **Lifecycle per standard 4.2:** `signal.NotifyContext`, `run() int` with exit code
   instead of `cobra.CheckErr`/`os.Exit` in `PersistentPreRun`; one hard deadline for the
   cleanup after a signal.
10. **Errors:** package-prefixed, wrapped messages; sentinels/typed errors for "already
    locked" and validation; no swallowed errors (`os.Remove` of stale locks, deferred
    `Unlock`); log once at the handling boundary (restic and `ProjectBackup` currently both
    log and return).
11. **Config per standard 5:** validation of all fields at load time with field paths
    (today some checks run in `ToProjects`, some in `servicemanager`); `Defaults()` step;
    the leftover `DOCKER_BACKUP_` env prefix: remove or rename to `RESTOR_` (breaking, `!`);
    decide whether JSON/TOML support stays (then add it to Deviations) or config becomes
    YAML only (breaking, `!`).
12. **Injected `Clock`:** `waitForStopped`/`waitForRunning` poll with `time.Now` and
    `time.After`; the locker writes `time.Now()`.
13. **Status detection:** "is running" matches output text; use `docker compose ps --format
    json` / `systemctl is-active` instead.
14. **Layout:** move to `cmd/restor` + `internal/…` (section 3 target), with the composition
    root in `internal/app` and per-package `Settings` structs.

---

## 11. Adding a Service Manager

1. New file in `servicemanager` implementing `ServiceManager` (services, stop, start,
   status, lock key); the lock key must be unique per managed unit set.
2. Config: add the type constant, its fields in `ProjectConfig`, validation, and a
   commented example in `example/config.yaml`.
3. Composition root / factory: add the `case`. Nothing else may reference the new type.
4. Document the commands it runs in section 7.
5. Tests with a fake command runner only (no real Docker, Podman or systemd).
6. README: user-facing documentation.

---

## 12. Commit Scopes

Conventional Commits per the standard; no AI signatures. Scopes used here:

`config`, `backup`, `restic`, `compose`, `systemd`, `lock`, `hooks`, `cli`, `release`, `ci`,
`deps`

Examples:

```
feat(compose): support compose profiles
fix(lock): key noop projects by project name
refactor(backup): inject restic and service manager through interfaces
feat(config)!: rename env prefix to RESTOR_
```
