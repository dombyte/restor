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

restor orchestrates **restic** backups of several **jobs** on one host.

- **Job:** a named set of `sources` (paths) plus, depending on its `type`, the services that
  must be stopped while they are backed up: `docker-compose`, `podman-compose`, `systemd`
  (system or user scope), or no type (files only, nothing to stop).
- **Per job:** lock → pre hook → stop services → `restic backup --tag=<job>` → start
  services → post hook (also after a failed backup) → on_failure hook (when it failed).
- **Order:** a job starts once the jobs it runs `after` (or that name it in `before`) have
  finished; at most `concurrency` jobs run at once (1 = one after another, 0 = all), and
  jobs that share a compose file or systemd unit never run together.
- **After all jobs:** `restic forget --group-by=host --tag=<job> <keep>` per job, then one
  repository-wide `restic prune` and `restic unlock` (only with `repository.prune`); the
  run-level post hook, then the run-level on_failure hook when anything failed.
- **Runtime:** a single CLI binary that runs **on the host** as a oneshot systemd service
  (`restor run`), triggered by a systemd timer (`example/restor.service`,
  `example/restor.timer`). It shells out to `restic`, `docker compose`, `podman compose`
  and `systemctl`; it has no server, no database and is not a container. Shipped as
  release archives and `.deb`/`.rpm` packages.

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
make integration                                          # integration tests, restic on PATH
make integration-oldest                                   # same with the oldest supported restic
golangci-lint fmt --config .golangci.yml                  # gofumpt + goimports
golangci-lint run --config .golangci.yml                  # full linter set (section 12)
go tool deadcode -test ./... # unused exported code
go tool govulncheck ./...     # known vulnerabilities
go test -race ./...                                       # all unit tests, as in CI
go tool mockery                                           # regenerate mocks (.mockery.yaml)
make build                                                # ./restor with version info (ldflags)
./restor run --config config.yaml [--debug] [job...]     # run one backup cycle
./restor validate --config config.yaml                    # check config, print job order
./restor restic --config config.yaml snapshots            # restic with repo + env file
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
- Mocks: mockery v3 is a Go tool (see below); `go tool mockery` generates them
  from `.mockery.yaml` into `internal/<pkg>/mocks`. CI checks them for drift (not part of
  `make check`); regenerate after changing an interface.
- Tool versions are pinned (`tool` directives in `go.mod`, the `version:` of the
  golangci-lint and goreleaser actions) and bumped deliberately, the same version locally and in CI. Go tools
  (mockery, deadcode, govulncheck) are `tool` directives, run with `go tool <name>` and
  bumped with `go get -tool <module>@vX.Y.Z`, rather than pinned `go run` commands: those
  check their modules against sum.golang.org on every CI run, and a checksum-DB hiccup
  can fail CI. golangci-lint is not one: it would pull ~300 modules into restor's module
  graph and upgrade the app's own dependencies; its maintainers support only prebuilt
  binaries.
- Go itself is the `go` directive in `go.mod`: CI installs it (`go-version-file`), and
  `scripts/pre-commit.sh` sets `GOTOOLCHAIN` to it, so `make check` uses the same release
  even on a distro Go. A distro build's version (e.g. `go1.26.8-X:nodwarf5`) makes
  govulncheck skip the standard library without a warning. Raise the directive when a Go
  patch release fixes a standard-library vulnerability.

---

## 3. Project Structure

```
cmd/main.go                main package: cobra commands (run, validate, snapshots, restore,
                           restic, version), logger, signal context, shutdown deadline,
                           exit code, build info
internal/app/              composition root: config Plan → Settings, CreateServiceManager,
                           restic flags, wiring, restic version check, startup warnings,
                           Describe (validate), snapshots, restore and the restic
                           passthrough
internal/config/           YAML file (version 2) decoded strictly, ${VAR} expansion, paths,
                           repository env file, defaults → validated read-only Plan
internal/backup/           orchestration: Manager (schedule, per-job run, hooks,
                           forget/prune/unlock); declares Restic, Services, Locker, Runner
internal/restic/           restic CLI client: typed flags, backup, forget, prune, unlock,
                           version
internal/servicemanager/   Compose (docker/podman), Systemd, Noop; status polling
internal/lock/             flock(2) lock files per key, lock dir safety checks
internal/command/          the only os/exec user: runs a program with a fixed env and ctx
                           (captured or attached to the terminal)
internal/util/             RequireAll/DependencyError, Clock; clocktest (fake clock)
internal/<pkg>/mocks/      mockery output (never hand-edited)
example/                   config.yaml (full reference, kept valid by a test), restor.env,
                           hooks/ (hook scripts + hooks.conf, shipped as examples),
                           restor.service + restor.timer (systemd units)
packaging/                 .deb/.rpm only: system + user units (/usr/bin/restor) and the
                           install/remove scripts (nfpm in .goreleaser.yaml)
scripts/pre-commit.sh      local checks (make check)
scripts/install-restic.sh  downloads a restic release, checksum verified (CI, make
                           integration-oldest)
```

Layout rules:
- `main` lives directly in `cmd/` and is built with `./cmd` (`cmd/<name>/` only if a
  second binary appears).
- `internal/` holds **all** project code; at most 3 levels below `internal/`.
- `pkg/` only for code meant for other modules, and only with user confirmation.
- Root files: `AGENTS.md`, `README.md`, `.golangci.yml`, `.goreleaser.yaml`,
  `.mockery.yaml`, `Makefile`, `renovate.json`, `scripts/pre-commit.sh`,
  `scripts/install-restic.sh`, `example/`, `packaging/`.

---

## 4. Dependency Direction

```
cmd/main               → internal/app, cobra, zerolog
internal/app           → config, backup, restic, servicemanager, lock, command, util
internal/backup        → util, zerolog
internal/restic        → util, zerolog
internal/servicemanager → util
internal/lock          → util
internal/command       → zerolog
internal/config        → yaml
internal/util          → stdlib only
```

Layers: `cmd → app (composition root) → backup (orchestration) → restic, servicemanager,
lock, command (external clients)`. Dependencies flow down only; no import cycles.

Rules:
- Only `internal/app` imports `config` and names concrete types. `backup`, `restic`,
  `servicemanager` and `lock` declare their own `Settings`/`Deps` structs; `app` maps the
  config's `Plan` onto them. Only `restic` knows restic's flags: `app` turns the typed
  job and repository settings into arguments with `restic.BackupFlags`, `restic.Keep`,
  `restic.Repository` and `restic.PruneFlags`; `backup` passes them on unread.
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

- **restic repository:** one repository (`repository.url`) shared by all jobs. Snapshots
  are separated by the tag `<job name>`. `backup` runs per job (several at once with
  `concurrency` ≠ 1; restic allows concurrent backups). `forget`, `prune` and `unlock` run
  **only in `Manager`, sequentially, after all backups**, because they need exclusive
  repository locks.
- **Lock files:** `lock.Locker` holds an `flock(2)` on `<lock dir>/<sha256 of key>.lock`
  (opened with `O_NOFOLLOW`; content: PID and key, for humans only). The kernel releases
  the lock when the holder exits, so there are no stale locks; the files are never
  removed (removing one would let two processes lock different inodes of the same path).
  Two `Lock` calls of one process conflict too. The lock dir is per user (`lock.DefaultDir`):
  `/run/restor` for root on Linux, `$XDG_RUNTIME_DIR/restor` for other users, else
  `restor-<uid>` in the temp dir; it is refused unless it is a real directory owned by the
  user and not writable by others (`ErrUnsafeDir`). The keys come from the service manager
  (`LockKeys`): the compose file path, one key per systemd unit, or `files-<job>`; a job
  takes all of its keys before it starts and fails if one is held. The locks stop two
  restor processes from stopping and backing up the same compose file or unit at once,
  also when two jobs share only some of their units. Within one run the schedule never
  starts two jobs that share a key, so they wait for each other instead of failing.
  Runs of different users do not see each other's locks (rare: root and a `docker` group
  member backing up the same compose file).
- **Services:** a job stops and restarts only those of its services (`services`, or all
  services of its compose file / its `units` when the list is empty) that are running when
  its backup starts (none running is a warning: a wrong compose project looks the same).
  Services that were already stopped, and one-shot jobs that have exited, stay as they
  were, unless the job sets `start_all`: then, after the restart, the job also starts the
  services it did not stop (`Services.All` minus the stopped ones; `start` leaves running
  ones as they are), also with `stop: false`, but not after a cancel.

## 6. Lifecycle

restor is a **oneshot** process: one `restor run` = one backup cycle, then exit.

### Run
1. `cmd/main`: cobra parses the command and flags (`--config` defaults to
   `app.DefaultConfig`, see CLI); logger = console writer on stderr, `--debug` = debug
   level (`validate`, `snapshots` and `restic` log warnings only); `signal.NotifyContext` for SIGINT/SIGTERM;
   build info is logged. `restor` without a command is an error, so the call of version
   1 (`restor --config x`) cannot pass as a backup.
2. `app.New`: `config.Load` (file, expansion, paths, env file, defaults, validation),
   `Plan.Select` for the named jobs, and wiring. Any error ends the run (exit 1); an
   invalid config is logged one line per problem.
3. `App.Run`: a config without jobs fails here (`app.ErrNoJobs`; it is valid for the
   commands that only need the repository); `restic version` must succeed and be at least the oldest supported release
   (`restic.Supported`), before any service is stopped; a release newer than the newest
   tested one is logged as a warning and runs.
4. `Manager.Run`: run the jobs as the schedule allows; when the run was not cancelled:
   forget each job that has a complete snapshot and a `keep`, then, with
   `repository.prune` and only when every job has a complete snapshot, one prune + unlock
   (only if at least one job forgot). Then the run-level post hook, and the run-level
   on_failure hook when a job, forget, prune or unlock failed.
5. Exit code: 0 when every job backed up; 1 when config failed, there are no jobs,
   restic is missing or too old, any job failed or the run was cancelled. Forget, prune, unlock and hook failures
   are logged but do **not** change the exit code; after a failed or incomplete job only
   its forget, prune and unlock are skipped (warnings), and a cancelled run does no
   maintenance. `restor restic` exits with restic's exit code.

### Failure handling per job
- Lock held by a live process → job fails.
- Pre hook fails → job fails; services are not touched, the post hook does not run.
- Finding the running services fails → job fails.
- Stopping services fails → warning, the backup still runs (services may be running).
- Services not stopped within `stop_timeout` → warning, the backup still runs.
- `restic backup` fails or exceeds `backup_timeout` → services are restarted, job fails.
  Exit code 3 (snapshot saved, some source files unreadable) or no snapshot ID in the
  output → warning, the job counts as backed up but is **incomplete**: its forget and the
  prune are skipped. A snapshot that `--skip-if-unchanged` skipped (a summary without ID)
  counts as **complete**: the latest snapshot is still current.
- Restarting services fails → job fails; not running within `start_timeout` → warning.
- `start_all`: listing or starting the services that were not stopped fails, or they are
  not running within `start_timeout` → warning only (a service that was down must not
  block forget and prune on every run).
- A failed job runs its on_failure hook after the post hook.
- Other jobs always continue, also those that run after the failed one; each failure is
  logged once by `Manager` with the job, and `main` logs the summary
  (`backup: jobs failed: N of M`).

### Shutdown and supervision
- SIGINT/SIGTERM cancel the run context: running commands get SIGTERM and are killed 10 s
  later (`exec.Cmd.Cancel`/`WaitDelay`, so restic can remove its repository lock). Each
  command runs in its own process group: SIGTERM goes to the whole group (a hook's child
  processes too), and what is left of it gets SIGKILL when the command returns; no
  further job starts, waits end early. (`restor restic` stays in the terminal's process
  group, so Ctrl-C reaches restic directly.)
- A job that stopped its services restarts them with its own context
  (`context.WithoutCancel`, bounded by `start_timeout` + 3 min), then runs its post hook,
  which may undo the pre hook, for at most 1 min after the cancel (`graceContext`).
- `main` waits at most `Manager.ShutdownTimeout` after the signal, then exits 1: 1 min
  plus, for the slowest job, `start_timeout` + 3 min when it stops services and 1 min
  when it has a post hook (only the jobs running at the signal do that, at the same time,
  so the slowest counts, not the sum; logged as `shutdown_timeout` at the start of a run).
  The systemd units set `TimeoutStopSec=900` so systemd does not kill restor while it
  restarts services; that covers a `start_timeout` up to 10 min, a larger one needs a
  larger `TimeoutStopSec`. No second-signal "force" mode: the deadline is the force.
- There is no in-process retry and no restart: systemd records the failed run, and the
  next timer run is the retry (`Persistent=true` catches up missed runs).

### Rules (MUST)
- Every goroutine has an owner that starts it and waits for it to end (`Manager.runJobs`
  starts one goroutine per job and receives each one's end on a channel before it
  returns).
- Loops that wait (service stop/start polling) use the injected `util.Clock`; tests use
  `util/clocktest`.
- `os.Exit` only in `main`; everything else returns errors.
- Service manager commands have timeouts: status queries 30 s, stop/start the configured
  timeout + 2 min. `restic` and hooks are bounded by the run context, a backup also by
  its optional `backup_timeout` (a backup can legitimately take hours).

---

## 7. Behaviour Reference

### Configuration
- One YAML file (`.yaml`/`.yml`) with `version: 2`; other extensions are an error.
  Template with every option: `example/config.yaml`. Top level: `version`,
  `concurrency`, `repository`, `post`, `on_failure`, `defaults`, `jobs`.
- Decoding is strict: a key that matches no field is an error with its path and line
  (`config.unknownKeys` walks the YAML nodes against the struct tags); the keys of version
  1 (`projects`, `mode`, `global`, `service_manager`, `retention_policy`, …) name their
  replacement. Types are strict too: durations are strings with a unit (`30s`; a bare
  number is an error), lists must be lists; a type error names the field (looked up by
  the position of its value) and the line. Every path names jobs in lowercase.
- YAML anchors work: aliases are followed and merge keys (`<<`, one mapping or a list)
  are checked against the type they are merged into; top-level keys starting with `x-`
  are ignored (a place for anchors, as in Compose files).
- `$VAR`, `${VAR}` and `${VAR:-default}` in scalar values (not keys) are expanded once at
  load time from the **process** environment; `$$` is a literal `$`. A plain scalar is
  re-typed after expansion, so `${LIMIT}` can fill a number.
- Relative `repository.env_file`, `compose_file`, `sources` and `exclude_file` are
  resolved against the directory of the config file (systemd runs restor in `/`); hooks
  are not (they are commands).
- `defaults` holds every job key except `type`, `sources`, `compose_file`, `units`,
  `scope`, `services`, `before` and `after` (`config.Defaults`, inlined into
  `config.JobConfig`). A job's own value replaces the default, lists included (an empty
  list clears the default); `keep` is replaced as a whole. Unset: `stop` true,
  `start_all` false, `stop_timeout` 1m, `start_timeout` 2m, `concurrency` 1.
- Job names are case-insensitive and used in lowercase (also as the restic tag and in
  `before`/`after`); names that differ only in case are an error. `before: [x]` on job a
  is `after: [a]` on x; the Plan holds only the merged, sorted `After` lists.
- **Environment:** restic gets the process env + `repository.env_file` +
  `RESTIC_REPOSITORY` (from `repository.url`, if set). The env file is `.env` only
  (`KEY=value` per line, `export ` prefix allowed, a line without `=` is an error,
  `"…"`/`'…'` values taken as is, an unquoted value ends at ` #`) and its values are
  **never expanded**. Service managers and hooks get only the process env (hooks plus
  their `RESTOR_*` variables).
- Validation (all problems at once, with field paths): `version` is 2; `concurrency` ≥ 0;
  `jobs` may be empty; job names and tags are not empty, do not start with `-` and
  contain no `,` or whitespace (they are restic tags, and restic splits `--tag` on commas);
  `type` is `docker-compose`, `podman-compose`, `systemd` or absent; `compose_file` for
  the compose types, `units` for systemd, `scope` is `system` or `user`, `services` of a
  systemd job are among its `units`; keys of another type are errors (`units` on a compose
  job, `services`/`stop`/`start_all`/timeouts on a files-only job); `sources` required, no empty
  entries; `before`/`after` name other existing jobs and form no cycle (the first cycle is
  reported as `a → b → a`); durations ≥ 0, `stop_timeout` > 0 when services are
  stopped and `start_timeout` > 0 when they are stopped or `start_all` is set; `keep` counts ≥ 0 and `within…` restic durations (`1y2m3d4h`); sizes
  (`exclude_larger_than`) like `500M`; `max_unused` a percentage, size or `unlimited`;
  `backend_options` keys like `s3.connections` with non-empty values; limits ≥ 0; env file
  names and values without NUL bytes; no extra `tags` that are the name of a job (forget
  `--tag=<job>` would select those snapshots too); `keep.tags` only next to another rule
  (alone, restic forgets every other snapshot, also the newest) and not one of the job's
  own `tags` (every snapshot has it, so forget would remove none); no path starting with
  `~` (YAML does not expand it). Startup warns about a missing repository
  (`repository.url`, `RESTIC_REPOSITORY`, `RESTIC_REPOSITORY_FILE`), an env file that
  group or others can access (mode & 077), a hook whose program is a relative path
  (`./x.sh`), and binaries missing from `PATH`.
- `example/config.yaml` stays complete, commented and valid (`TestLoad_ExampleConfig`);
  every new option is added there in the same change.
- Secrets (restic password, S3 keys) come from the env file or the process env, never from
  `example/`; the real `config.yaml` and env files are gitignored.

### CLI
- `restor run [job...]`: one backup cycle of all or the named jobs (`Plan.Select`;
  unknown names are an error). Ordering against jobs that are not selected is dropped.
- `restor validate`: loads and wires everything, then prints the jobs in the order they
  start with concurrency 1 (`Manager.Order`), with type, after, stop, a keep label and
  sources, and below them each distinct retention policy once with its label (A, B, … in
  order of first use; `-` = never forgotten) and its jobs; never the repository URL (it
  may hold credentials). Without jobs it says so instead of the tables.
- `restor snapshots [job...]`: `restic <repository global flags> snapshots --tag=<job>…`
  for all or the named jobs (`Plan.Select`, so unknown names are an error), attached to
  the terminal like `restor restic`; other snapshot options go through `restor restic`.
  `--all` (not with job names) and a config without jobs list every snapshot, without a
  `--tag` filter, so the series of renamed or removed jobs can be found.
- `restor restore <snapshot-ID> (--target <dir> | --in-place) [--apply]`: checks the
  restic version, then runs `restic <global> restore --target=… [--dry-run --verbose=2]
  [--include=… --iinclude=… --exclude=… --iexclude=… --overwrite=… --verify] -- <ID>`
  (`restic.RestoreFlags`), attached to the terminal; exit code is restic's. A dry run
  unless `--apply`; `--in-place` is `--target=/` (kept as its own flag because
  `--target <original dir>` nests the paths); exactly one of them
  (`app.ErrRestoreTarget`). The ID must be 8–64 lowercase hex characters, so `latest`
  (any job, any host) is refused (`restic.ErrRestore`). Files only: no services, locks
  or hooks, and no job config (a repository-only file works). Logs one info line before
  and one after (`snapshot`, `target`, `dry_run`). `--delete` is left out on purpose
  until an integration test shows that it stays inside the snapshot's paths in place.
- `restor restic <args>`: `restic <repository global flags> <args>` with the restic
  environment, attached to the terminal (`command.Runner.Attach`); restor's flags must
  come before the first restic argument (`SetInterspersed(false)`).
- `restor` without a command is an error.
- `--config`/`-c` defaults to `app.DefaultConfig`: `/etc/restor/config.yaml` for root,
  `restor/config.yaml` in `$XDG_CONFIG_HOME` (when absolute), else in `~/.config` for
  other users, like `lock.DefaultDir`; without an absolute home the command fails. A fixed
  rule, not a search path: a fallback could forget snapshots in another repository than
  intended. When a non-root user's default file is missing or unreadable, a warning
  explains the rule (pass `--config`, or run as root for the system file).

### Schedule
- `backup.schedule` starts a pending job when every job in its `After` that is part of
  the run has finished (successfully or not: ordering only), no running job holds one of
  its lock keys, and fewer than `concurrency` jobs run (0 = no limit). Among the jobs that
  may start, the earlier one in the list (sorted by name) starts first. After a cancel
  no job starts; jobs never started are neither failed nor complete.

### Hooks
- Split on whitespace and executed directly (no shell): no quoting, pipes or redirects.
  Use a script for anything more. A program that exits successfully while a process it
  started still holds its output succeeds after the 10 s `WaitDelay`, with a warning
  (`exec.ErrWaitDelay`).
- Run with the process environment, the run context and these variables: `pre` gets
  `RESTOR_JOB`, `RESTOR_HOOK=pre`; `post` and the job's `on_failure` also
  `RESTOR_STATUS` (`success`, `incomplete`, `failed`), `RESTOR_SNAPSHOT_ID` (empty without
  a snapshot) and, after a failure, `RESTOR_ERROR`. The run-level `post` and `on_failure`
  get the run summary (`backup.summary`): `RESTOR_HOOK`, `RESTOR_STATUS` (`failed` when a
  job or the maintenance failed, else `incomplete` when a snapshot is, else `success`),
  the comma-separated `RESTOR_JOBS`, `RESTOR_SUCCEEDED_JOBS`, `RESTOR_INCOMPLETE_JOBS`,
  `RESTOR_FAILED_JOBS`, `RESTOR_SNAPSHOT_IDS` (`job=ID`) and, when anything failed,
  `RESTOR_ERROR` (maintenance failures, then job failures, `; `-separated).
- The post hook runs after the services are started again whenever the pre hook succeeded
  (or there is none), also when the backup or the restart failed, so it can undo the pre
  hook. The job's on_failure hook runs after it when the job failed (also after a failed
  lock or pre hook). The run-level post runs once after maintenance, then the run-level
  on_failure when a job, forget, prune or unlock failed. After a cancel the job's post
  hook still runs (for at most 1 min), the others are skipped (a warning); their own
  failures are logged only.

### Service managers
- Compose: `<docker|podman> compose -f <file> [-f <override>] …`; the override is the first
  of `compose.override.yml`, `compose.override.yaml`, `docker-compose.override.yml`,
  `docker-compose.override.yaml` next to a file with a default name (`compose.yaml`,
  `compose.yml`, `docker-compose.yml`, `docker-compose.yaml`), found at load time
  (`config.composeFiles`), as Compose itself does without `-f`; the first file is the lock
  key. Services from `config --services` when not listed (a listed name that
  `config --services` does not report fails the job); stop with
  `stop -t <stop_timeout in s, rounded up> <services>` (ignores "no containers to stop"),
  start with `start <services>` (not `up`: a backup never recreates containers from a
  changed compose file). Running = `ps --format json` reports the service with `State`
  `running` (Docker JSON lines or a JSON array; podman-compose via the
  `com.docker.compose.service` label).
- systemd: `systemctl [--user] stop|start <units>` (stop ignores "not running"/"not
  loaded"); state from `systemctl is-active <units>`. Up (running before the backup, and
  not yet stopped) = `active`, `activating`, `deactivating`, `reloading` or `refreshing`;
  started = all `active` (a `Type=simple` unit is `active` as soon as its process runs,
  so a crash right after the start is not seen; README says so).
  `services` selects some of `units` (a name that is not one of them is a validation
  error); empty means all units.
- Files only (no type): the `noop` service manager; no services, every operation
  succeeds, lock key `files-<job>`.
- Stop and start poll every 500 ms until the expected state or the timeout; a failed
  status query is retried, and its error counts only when the last poll failed. The
  timeout counts from before the stop/start command, so a slow command shortens the wait
  (at least one status query still runs); a restart thus ends within `start_timeout` +
  2 min + one status query, inside the `start_timeout` + 3 min bound after a cancel.

### restic
- No caller passes raw restic arguments: every option is a typed config field that the
  `restic` package turns into flags, each value as `--flag=value` (so it can never be read
  as a flag of its own) and the sources after `--`.
- The repository is passed as `RESTIC_REPOSITORY` (not `-r`), so it never shows up in
  arguments or logs; with an empty `repository.url` a `RESTIC_REPOSITORY` from the env is
  used.
- Global flags before every command (`restic.Repository.Args`): `--option=k=v` per
  `backend_options` entry (sorted), `--limit-upload=`, `--limit-download=`,
  `--retry-lock=`. Settings restic reads from its environment (`RESTIC_CACHE_DIR`,
  `RESTIC_COMPRESSION`, `RESTIC_PACK_SIZE`, …) get no config key: they go in the env file.
- `restic [global] backup --json --group-by=host,tags [backup flags] --tag=<job> --
  <sources>`: the parent snapshot comes from the job's own snapshots (restic's default
  `host,paths` would compare a job with another one that has the same sources, and
  `--skip-if-unchanged` would then skip its snapshot); backup flags
  from `exclude`, `iexclude`, `exclude_file`, `exclude_if_present`, `exclude_caches`,
  `exclude_larger_than`, `one_file_system`, `skip_if_unchanged` and `tags` (extra
  `--tag=`). The last JSON `summary` message gives the snapshot ID (logged as the
  8-character short ID; none for a `dry_run` summary or a missing summary, which is a
  warning, not an error) and the counters logged at info (`files_new`, `bytes_added`,
  `duration`, …). Lines that are not JSON are skipped. Exit code 3 returns the ID with a
  `*restic.IncompleteError`; a real summary without ID (skipped as unchanged) returns a
  `*restic.UnchangedError`. `backup` matches them through `Incomplete() bool` and
  `Unchanged() bool` interfaces, so it does not import `restic`.
- Exit codes 10 (no repository), 11 (repository locked; hint: `repository.retry_lock`)
  and 12 (wrong password) are named in the error before the runner's error, which keeps
  only restic's last stderr lines. backup writes its errors there as JSON messages; each
  is replaced by its text (`message`, or `error.message` for an unreadable file), so logs
  and `RESTOR_ERROR` read `Fatal: …`, not `{"message_type":"exit_error",…}`.
- `restic [global] forget --group-by=host --tag=<job> [keep flags]`; keep flags
  `--keep-last=` … `--keep-yearly=`, `--keep-within…=` and `--keep-tag=`.
- `restic [global] prune [--max-unused=]` and `restic [global] unlock`.
- `restic version` before a run (`restic.ParseVersion` reads `restic X.Y.Z`): below
  `restic.Supported`'s oldest release (0.17.1) the run fails, above its newest tested one
  it warns.

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
  the config is written), ship the service units without `[Install]` (an enabled service
  would back up, and stop services, at every boot) and only *recommend* `restic` (often
  installed upstream).
- **Oneshot + systemd timer instead of a daemon:** scheduling, logging (journal), failure
  state and catch-up of missed runs come from systemd; restor stays a plain CLI. A failed
  run exits 1 and the next timer run is the retry; there is no container runtime restart
  and no in-process restart.
- **Ready for a daemon later:** `config.Load` has no side effects besides reading files
  and returns an immutable `Plan`; `Plan.Select` and `Manager.Run` work on any set of
  jobs. A daemon would add keys (a per-job `schedule`, a `daemon` section, a prune
  schedule under `repository`) and reload by loading again; the oneshot path stays as is.
- **Shell out to the CLIs instead of using libraries/APIs:** restic has no stable Go API,
  and the compose/systemctl CLIs behave the same for Docker, Podman and systemd scopes.
- **YAML only, versioned and strict:** one format keeps the code and the docs small; the
  file is decoded with go-yaml directly (no viper: it lowercased map keys) and checked
  against the struct tags, so a typo is an error, not a silently ignored key. `version`
  lets a later format change recognise old files. Version 1 keys are not accepted, only
  explained: there is no migration code to keep.
- **Typed restic options instead of raw arguments:** every option is validated at load
  time (before a service is stopped), restor keeps control of the flags it relies on
  (`--json`, `--tag`, `--dry-run`, `--host`, `--group-by` cannot be set), and values are
  passed as `--flag=value`. A missing option is a small new field; settings restic reads
  from its environment need none (env file), and `restor restic` covers everything done
  by hand.
- **Flat jobs with a `type`:** the type picks the service manager and the keys it allows;
  a new type adds a value and its keys, existing configs stay valid. Defaults replace
  rather than merge, so a job's value is always the whole value.
- **The env file belongs to restic:** it holds the repository credentials; hooks and
  compose files must not see them. Values are literal because expanding them broke
  passwords containing `$`.
- **Forget and prune after all backups, sequentially:** they take exclusive repository locks
  and would fail or block concurrent backups; one repository-wide prune is cheaper than one
  per job. After a failed job, forget still runs for the jobs that have a fresh complete
  snapshot (a permanently broken job must not stop retention for all others), but the
  failed job keeps its history and prune is skipped, so a broken run never deletes data.
  An incomplete snapshot is treated like a failure here: if files stay unreadable for
  days, forgetting would rotate out every complete snapshot and keep only incomplete ones.
  Forget runs without `prune` too: forgetting alone frees nothing but keeps the snapshot
  list bounded.
- **Forget groups by host, selected by tag:** restic's default grouping (`host,paths`)
  would leave the snapshots of changed `sources` in a group that is never forgotten, and
  `host,tags` the same after a change of `tags`. `--tag=<job>` selects the job, grouping by
  host keeps one series per job and host.
- **A supported restic range, not a pinned release:** restor depends on restic's CLI,
  JSON output and exit codes, so it refuses releases older than the oldest one that
  passes the integration tests (0.17.1: exit code 12 and `--skip-if-unchanged`; older
  releases lack `backup --group-by`). There is no maximum: restic stays compatible in
  practice, and refusing newer releases would stop backups after every restic update
  and block its security fixes. A newer release only logs a warning; Renovate raises the
  tested release once CI passes with it. Per-option version checks are not needed while
  the minimum covers every option; a future option that needs a newer restic raises the
  minimum or brings such a check back.
- **Ordering only, not dependencies:** `before`/`after` say when a job may start, not
  whether: a failed database dump must not cancel the backup of the files next to it. A
  "requires" relation can be added later as its own key.
- **Shared services wait instead of failing:** the schedule knows each job's lock keys,
  so jobs of one compose file run one after another even with `concurrency: 0`; the
  `flock` locks still guard against a second restor process.
- **Restart services even when the backup failed or the run was cancelled:** a failed
  backup must not leave production services down. `backup_timeout` exists for the same
  reason: a hanging backup would otherwise keep them down until someone notices. The
  job's post hook runs after a cancel for the same reason (it may end a maintenance mode
  the pre hook started), bounded so that a hanging hook cannot block the shutdown; the
  notification hooks are skipped.
- **Tag = lowercase job name:** keeps each job's retention separate in a shared
  repository; lowercase keeps the tags of snapshots made by earlier versions (viper
  lowercased the names). Renaming a job starts a new snapshot series (the old one is no
  longer forgotten).
- **Start order = sorted by name:** the config's `jobs` map has no order once decoded;
  sorting makes runs reproducible. `before`/`after` express any other order.
- **Repository in the environment:** `RESTIC_REPOSITORY` instead of `-r` keeps
  credentials in repository URLs out of debug logs and command errors (errors carry only
  the program name and the last 20 output lines).
- **Stdout and stderr apart:** `command.Runner` returns only stdout, which the callers
  parse (`compose ps --format json`, `config --services`, `systemctl is-active`, restic's
  JSON). Stderr carries warnings that would break the parsing: Compose's
  `level=warning` lines (obsolete `version:`, unset variables) and `podman compose`'s
  "Executing external compose provider" banner. Errors carry the last stderr lines (stdout
  when stderr is empty, as scripts often report there), so checks for expected failures
  ("not loaded", "no containers to stop") match the error, not the output.

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
  `fmt.Errorf("restic: backup %s: %w", job, err)`.
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
  gets `log.With().Str("component", name).Logger()` (plus `job` where it applies).
- Log with context fields, not formatted strings:
  `.Str("job", name).Err(err).Msg("job failed")`.
- Levels: `debug` for command lines, the last output lines of successful commands and
  per-step detail, `info` for lifecycle (run start,
  job done, snapshot ID), `warn` for recovered problems (services not stopped/started
  in time, stop command failed, no running service to stop, missing binary, suspicious
  config), `error` for failures
  that need attention (job failed, forget/prune/unlock failed, a hook
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
- `cmd/integration_test.go` drives the CLI end to end (`run`, `validate`, `snapshots`
  with jobs, `--all` and without jobs, `restore` dry run, `--target`, `--in-place`,
  `--target /`, the `restic` passthrough) against a local repository.
- `internal/app/integration_test.go` runs the restic on `PATH` against local repositories
  (`make integration`; `make integration-oldest` with the oldest supported release, which
  CI tests too): one subtest per restic feature restor uses (every backup, forget,
  global and prune flag, skip-if-unchanged, exit codes 3/10/11/12, a whole `restor run`,
  `restor restore`), so a failing release shows what it lacks. A new restic option gets a check there.
  Backups that must be unchanged use `testdata/static` and are retried
  (`unchangedWithin`): restic also records the directories above a source, those of a
  temp dir change while other tests run, and even the home directory can change.
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
| `Makefile` | `build` (ldflags build info), `check` (runs the script), `clean`, `run`, `integration` (restic on PATH), `integration-oldest` (downloads `minSupported` into `~/.cache/restor/restic`) |
| `.github/workflows/checks.yml` | on push to main, PRs and `workflow_call`: tidy check, build, race tests, deadcode, govulncheck, mock drift, golangci-lint, integration tests against the oldest supported and the newest tested restic (downloaded, checksum verified); a `gate` job skips them on a push to main that is a PR's merge commit (15.5); `concurrency` cancels superseded PR runs only |
| `.github/workflows/release.yml` | on `vX.Y.Z` / `vX.Y.Z-*` tags: a `guard` job verifies the exact tag format and that the tag is on `main`, then reuses `checks.yml` (granted `pull-requests: read` for the gate); passes `GOVERSION` to goreleaser; `concurrency` runs one release per tag, never cancelled |
| `.goreleaser.yaml` | `go mod verify` only (never rewrites), `-trimpath`, reproducible (`CommitDate`), `goamd64: v1`, grouped changelog, archives + nfpm `.deb`/`.rpm` (`mtime` = `CommitDate`), no image (section 8) |
| `.gitignore` | never commit local config (`config.yaml`, `*.env` in the root), secrets or build output |
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
- `enabledManagers` lists exactly the ecosystems restor has: `gomod` (incl. the Go
  tools), `github-actions`, plus `custom.regex` for the regex manager below.
- Every pinned version in the repo is tracked. Versions no built-in manager sees get a
  `customManagers` regex; today the `version:` of the golangci-lint action in CI and of
  the goreleaser action in the release (the `github-actions` manager tracks the actions,
  not that input), and the newest tested restic, marked with a
  `renovate: datasource=… depName=…` comment in `checks.yml` and
  `internal/restic/version.go` (one PR updates both; `TestSupported_MatchesCI` fails
  when they differ). Its PR merges only when the integration tests pass with the new
  release; restic majors are disabled. The oldest supported release is raised by hand.
- Updates that need manual work (Go module majors = new module path, golangci-lint and
  goreleaser majors = config migration) are disabled with a `description` saying why.
- Text that only describes a pinning pattern must not match a regex manager.
- **Auto-merge:** Renovate proposes a version only once it is 3 days old
  (`minimumReleaseAge`; security updates at once) and merges minor, patch and digest
  updates itself with a merge commit once the required checks pass, only between 00:00
  and 06:00 Europe/Berlin (`automergeSchedule`). Renovate merges, not GitHub
  (`platformAutomerge: false`): GitHub's auto-merge is armed when the PR is opened and
  would ignore the schedule. Majors and lock file maintenance stay manual PRs. Releases
  are still tagged by hand.
- A change that adds a new pinned version extends `renovate.json` in the same change.

---

## 15. Git Workflow

### 15.1 Branches
- Changes go through a PR from a `type/description` branch (`feat/…`, `fix/…`, `docs/…`,
  `refactor/…`, `chore/…`). The maintainer MAY commit or merge directly into `main`
  (ruleset bypass, 15.5) after `make check` passes; CI then runs on the pushed commit.
  Bots such as Renovate always need a PR with green checks.
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
- The required CI checks MUST be green before a PR is merged. No approval is required
  while the maintainer is the only reviewer; Renovate merges its own non-major PRs
  (section 14).

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
`main` (`~DEFAULT_BRANCH`) is protected by two repository rulesets (not classic branch
protection):

- "main: signed commits": every commit signed; no bypass, not even for the admin.
- "main: required checks": the `checks.yml` jobs `Gate`, `golangci-lint` and
  `Build, test, deadcode, govulncheck` must pass, with "branches must be up to date" on.
  The `Integration (oldest restic)` and `Integration (newest restic)` jobs are not in the
  ruleset on purpose: they gate releases (`release.yml` reuses `checks.yml`, so a tag only
  ships when they pass) and Renovate's merges (it waits for every check), while a direct
  push to `main` does not wait for a restic download.
  Bypass: only the Repository admin role, mode "always". Bots (Renovate) and other
  contributors are never on the bypass list.
- Renaming one of these jobs means updating the ruleset in the same change, or every PR
  waits for a check that never reports.
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

### Adding a job type
1. New file in `servicemanager` with a `…Settings` struct and a constructor that takes
   `Deps` (Runner, Clock) and validates them; it implements `backup.Services`: `LockKeys`
   (one key per independently managed unit), `Running`, `All`, and `Stop`/`Start` that
   wait with `waitUntil` and return false when the timeout passes.
2. Config: add the `Type…` constant, its keys in `JobConfig` (not in `Defaults`) and
   `Job`, validation in `validator.job` (a case for the type, and its keys as errors for
   the other types), and a commented example in `example/config.yaml`.
3. `internal/app`: add the `case` in `CreateServiceManager` and its binary in
   `warnMissingBinaries`. Nothing else may reference the new type.
4. Document the commands it runs in section 7.
5. Tests with the mocked runner and `clocktest` only (no real tools).
6. README: user-facing documentation.

### Adding a restic option
1. A typed field in `config.Defaults` (per job) or `config.RepositoryConfig`, with
   validation of its format in `validate.go`, and in the `Job`/`Repository` of the Plan.
2. The flag in the matching `restic` struct (`BackupFlags`, `Keep`, `Repository`,
   `PruneFlags`) and its `Args`, as `--flag=value`; the mapping in `app`. If the oldest
   supported restic lacks the flag, raise `minSupported` (and the CI matrix) or add a
   version check for the option.
3. `example/config.yaml`, README table, section 7; tests for validation and `Args`, and a
   check in the integration test.

---

## 17. Migration Backlog

Known gaps between the code and the rules above, in suggested order. Each item is its own
`refactor/…` or `fix/…` branch; update this list when an item is done.

None at the moment: the migration to these rules is complete.
