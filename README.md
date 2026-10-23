# Restor

Restic Backup Orchestrator - Back up services and files to a restic repository.

## Features

- Several backup jobs from one YAML file, with defaults shared by all jobs
- Stops and restarts the services of a job during its backup (Docker/Podman Compose,
  systemd)
- Job order with `before`/`after`, and as many jobs at once as `concurrency` allows;
  jobs that share services never run at the same time
- Typed, validated restic options (excludes, tags, limits, ...) and retention policies
  (`keep`) with a single prune for the whole repository
- Hooks before and after each job and on failure, with the job's result in their
  environment
- `restor restic ...` runs restic against the configured repository, for restores and
  everything else

## Installation

Download a package or archive from the
[releases](https://github.com/dombyte/restor/releases). restor needs `restic` 0.17.1 or
newer; the packages install it as a recommended dependency.

**restic versions:** restor is tested against restic 0.17.1, the oldest release it
supports, and 0.19.1, the newest release it was tested with. Every CI run tests both, and
the newest is raised when restic releases. An older restic is refused before anything is
stopped. A newer one runs with a warning in the log; please report problems with it.
Distributions with an older restic (Debian 12 ships 0.14) need the
[upstream binary](https://github.com/restic/restic/releases), which `restic self-update`
keeps current.

### Package (.deb / .rpm)

```bash
sudo apt install ./restor_<version>_amd64.deb       # Debian, Ubuntu
sudo dnf install ./restor-<version>-1.x86_64.rpm    # Fedora, RHEL
```

Create the config and the env file, check them and enable the daily timer:

```bash
sudo cp /usr/share/doc/restor/examples/config.yaml /etc/restor/config.yaml
sudo install -m 0600 /usr/share/doc/restor/examples/restor.env /etc/restor/restor.env
sudoedit /etc/restor/config.yaml /etc/restor/restor.env
sudo restor validate
sudo systemctl enable --now restor.timer
```

### Archive

```bash
tar xzf restor_<version>_linux_amd64.tar.gz
cd restor_<version>_linux_amd64
sudo install -m 0755 restor /usr/local/bin/restor
sudo install -d -m 0700 /etc/restor
sudo cp example/config.yaml /etc/restor/config.yaml
sudo install -m 0600 example/restor.env /etc/restor/restor.env
sudoedit /etc/restor/config.yaml /etc/restor/restor.env
sudo restor validate
sudo cp example/restor.service example/restor.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now restor.timer
```

### As a normal user

The package also installs user units that read `~/.config/restor/config.yaml`:

```bash
mkdir -p ~/.config/restor
cp /usr/share/doc/restor/examples/config.yaml ~/.config/restor/
install -m 0600 /usr/share/doc/restor/examples/restor.env ~/.config/restor/
restor validate --config ~/.config/restor/config.yaml
systemctl --user enable --now restor.timer
sudo loginctl enable-linger "$USER"   # keep the timer running while logged out
```

## Usage

```bash
restor run                      # back up every job, then forget and prune
restor run web db               # only these jobs (their order among each other is kept)
restor validate                 # check the config, print the jobs in start order
restor restic snapshots         # run restic with the configured repository and env file
restor restic restore latest --tag web --target /tmp/restore
restor version
```

Every command reads `/etc/restor/config.yaml` unless `--config <file>` (`-c`) says
otherwise; `--debug` logs every command line. Logs go to stderr, under systemd to the
journal: `journalctl -u restor` (or `journalctl --user -u restor`).

`restor run` exits 0 when every job was backed up and 1 when the config is invalid, a job
failed or the run was cancelled. A failed forget or prune is logged and runs the
`on_failure` hook, a failed `post` or `on_failure` hook is only logged; neither changes
the exit code. Before anything is stopped, it checks
that `restic` is installed and supported (see below). `restor restic` exits with restic's
exit code.

On SIGINT/SIGTERM restor stops the running backups, restarts the services it stopped,
starts no further job and exits within its shutdown timeout (the largest `start_timeout`
+ 4 minutes, logged as `shutdown_timeout`). The shipped units allow 900 s
(`TimeoutStopSec`); raise it if a `start_timeout` is above 11 minutes.

## Configuration

`example/config.yaml` shows and explains every option; `example/restor.env` is a template
for the env file. A small configuration:

```yaml
version: 2
repository:
  url: s3:https://s3.example.com/backups
  env_file: restor.env          # RESTIC_PASSWORD_FILE, AWS_*, ...: only restic sees it
  prune: true
defaults:
  keep: {daily: 7, weekly: 4, monthly: 6}
jobs:
  db:
    type: docker-compose
    compose_file: /srv/app/compose.yml
    services: [db]
    sources: [/srv/app/db]
    before: [web]
  web:
    type: docker-compose
    compose_file: /srv/app/compose.yml
    services: [web]
    sources: [/srv/app/uploads]
    exclude: ["*.tmp", "cache/"]
  etc:
    sources: [/etc]
```

**The file** is YAML (`.yaml`/`.yml`) with `version: 2`. It is checked completely at
startup and every problem is reported with its field path; an unknown key is an error,
so typos never go unnoticed. Durations are strings such as `30s`, `5m` or `4h`.
`$VAR`, `${VAR}` and `${VAR:-default}` in values are replaced from restor's process
environment; `$$` is a literal `$`. Relative `env_file`, `compose_file`, `sources` and
`exclude_file` paths are resolved against the directory of the config file; `~` is not
expanded (use `$HOME`). YAML anchors and merge keys work: keep shared blocks under a
top-level key starting with `x-` and merge them with `<<: *name`.

**Repository:** `url` (or `RESTIC_REPOSITORY`/`RESTIC_REPOSITORY_FILE` in the env file),
`env_file`, `backend_options` (`--option key=value`), `limit_upload`, `limit_download`
(KiB/s), `retry_lock`, `prune`, `max_unused`. The repository is passed to restic as
`RESTIC_REPOSITORY`, so it never shows up in command lines or logs.

**The env file** holds the variables of restic only: the repository password and the
backend credentials, and restic's own settings such as `RESTIC_CACHE_DIR` or
`RESTIC_COMPRESSION`. One `KEY=value` per line, as systemd's `EnvironmentFile=`; values
are taken literally, so a `$` in a password stays a `$`. Keep it private (`chmod 600`);
restor warns when other users can read it. Hooks and service managers do not see it.

**Jobs:** the name is case-insensitive, its lowercase form is the restic tag, and it must
not start with `-` or contain `,` or whitespace.

| Key | Meaning |
|---|---|
| `type` | `docker-compose`, `podman-compose` or `systemd`; leave it out for files only |
| `sources` | paths to back up (required) |
| `compose_file` | the compose file (compose types, required) |
| `units`, `scope` | the systemd units (required) and `system` (default) or `user` |
| `services` | services to stop; default: all of the compose file / all `units` |
| `before`, `after` | jobs that run after / before this one |
| `stop` | stop the services during the backup (default `true`) |
| `stop_timeout`, `start_timeout` | wait for the services to stop / start (default `1m` / `2m`) |
| `backup_timeout` | cancel a backup that takes longer; the services start again, the job fails |
| `keep` | retention: `last`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`, `within…`, and `tags` next to another rule |
| `exclude`, `iexclude`, `exclude_file`, `exclude_if_present`, `exclude_caches`, `exclude_larger_than`, `one_file_system` | what restic leaves out (the restic options of the same name) |
| `skip_if_unchanged` | create no snapshot when nothing changed |
| `tags` | extra snapshot tags (not the name of a job) |
| `pre`, `post`, `on_failure` | hooks, see below |

Every key except `type`, `sources`, `compose_file`, `units`, `scope`, `services`,
`before` and `after` can also be set under `defaults`. A job's own value replaces the
default; lists are replaced, not merged.

**Order and concurrency:** `concurrency` (default `1`) is how many jobs run at once; `0`
runs all at once. A job starts once the jobs it runs after have finished, whether they
succeeded or not: `before`/`after` order jobs, they do not make one depend on the success
of another. Among the jobs that may start, the one first by name starts first, so with
`concurrency: 1` and no `before`/`after` the jobs run in name order. Jobs that share a
compose file or a systemd unit never run at the same time. `restor validate` prints the
resulting order.

**Services:** only the services that are running when a job's backup starts are stopped,
and only those are started again afterwards, also when the backup failed. A systemd unit
counts as started once `systemctl is-active` reports `active`. A `Type=simple` unit is
`active` as soon as its process runs, so restor cannot tell when it crashes right after
starting; use `Type=notify`, or check the unit in a `post` hook, when its start has to be
confirmed. A lock per compose file and per unit keeps two restor runs from working on the
same services; the second one fails.

**Hooks** are commands split on spaces and run without a shell; use a script with its
absolute path for anything more (a relative path resolves against the working directory,
`/` under systemd, and restor warns about it).

| Hook | Runs | Environment |
|---|---|---|
| `pre` | before the services are stopped; if it fails, the job fails and nothing else runs | `RESTOR_JOB`, `RESTOR_HOOK=pre` |
| `post` | after the services are started again, whenever `pre` succeeded | also `RESTOR_STATUS` (`success`, `incomplete`, `failed`), `RESTOR_SNAPSHOT_ID`, `RESTOR_ERROR` |
| job `on_failure` | after `post`, when the job failed | as `post` |
| top-level `on_failure` | once after the run, when a job, forget or prune failed | `RESTOR_HOOK=on_failure`, `RESTOR_STATUS=failed`, `RESTOR_FAILED_JOBS`, `RESTOR_ERROR` |

A failing `post` or `on_failure` hook is logged and changes nothing. After a cancel
(SIGTERM) the remaining hooks are skipped.

**Retention:** after all backups, every job with `keep` and a complete snapshot runs
`restic forget --group-by=host --tag=<job>`, so each job's snapshots on this host are one
group, also when its `sources` or `tags` change. With `repository.prune`, one `prune` and
`unlock` follow, but only when every job of the run has a complete snapshot: a job that
failed, or whose snapshot is incomplete (restic could not read some files), keeps its
history and prune waits for a better run. A run that `skip_if_unchanged` skipped counts as
complete.

## Upgrading from configuration version 1

restor now reads only configuration version 2 and needs a command. The old keys are
reported with their replacement, so `restor validate` guides the rewrite:

| Version 1 | Version 2 |
|---|---|
| `restor --config <file>` | `restor run --config <file>` (the shipped units are updated) |
| JSON or TOML file | YAML |
| `projects` | `jobs` |
| `mode: sequential` / `parallel` | `concurrency: 1` / `0` |
| `global.restic_repo` | `repository.url` |
| `env_file`, `environments` | `repository.env_file` only; values are no longer expanded and only restic gets them |
| `global.auto_prune` | `repository.prune`; forget runs for every job with `keep`, also without prune |
| `global.prune_options` | `repository.backend_options`, `limit_upload`, `limit_download`, `retry_lock`, `max_unused`, or `RESTIC_*` in the env file |
| `global.pre_backup_cmd`, `post_backup_cmd` | `defaults.pre`, `defaults.post` |
| `service_manager` | `type`; `noop` is no type at all |
| `systemd_units`, `systemd_scope` | `units`, `scope` |
| `stop_services` | `stop` |
| `stop_timeout: 30` | `stop_timeout: 30s` (now optional, default `1m`; `start_timeout` `2m`) |
| `retention_policy: --keep-daily 7` | `keep: {daily: 7}` |
| `backup_options` | `exclude`, `iexclude`, `exclude_file`, `exclude_if_present`, `exclude_caches`, `exclude_larger_than`, `one_file_system`, `skip_if_unchanged`, `tags` |
| `forget_options` | `keep` (e.g. `keep: {tags: [important]}`) |
| `pre_backup_cmd`, `post_backup_cmd` | `pre`, `post` |

Changed behaviour:

- **Retention groups by host:** forget used restic's default grouping (`host,paths`). It
  now groups each job's snapshots by host only, so the first forget after the upgrade
  applies `keep` to all of a job's snapshots together, including those of older `sources`
  that were never forgotten before.
- **Compose variables:** compose files no longer see the variables of the env file. Put
  the variables a compose file needs into the `.env` file next to it, as Compose expects.
- **Jobs that share services** wait for each other instead of failing as locked.

## License

MIT
