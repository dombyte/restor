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
- `restor restore` restores a snapshot, as a dry run unless `--apply` is given
- `restor restic ...` runs restic against the configured repository, for everything else

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

The package also installs user units that read `~/.config/restor/config.yaml`, the file
every command reads for a user other than root:

```bash
mkdir -p ~/.config/restor
cp /usr/share/doc/restor/examples/config.yaml ~/.config/restor/
install -m 0600 /usr/share/doc/restor/examples/restor.env ~/.config/restor/
restor validate
systemctl --user enable --now restor.timer
sudo loginctl enable-linger "$USER"   # keep the timer running while logged out
```

## Usage

```bash
restor run                      # back up every job, then forget and prune
restor run web db               # only these jobs (their order among each other is kept)
restor validate                 # check the config, print the jobs in start order and
                                # each retention policy once, with the jobs using it
restor snapshots                # list the snapshots of every job
restor snapshots web            # of this job only (restic snapshots --tag=web)
restor snapshots --all          # every snapshot, also of renamed or removed jobs
restor restore 1a2b3c4d --target /tmp/restore          # dry run: list what it would do
restor restore 1a2b3c4d --target /tmp/restore --apply  # restore below /tmp/restore
restor restic snapshots         # run restic with the configured repository and env file
restor version
```

Every command reads `/etc/restor/config.yaml` when run as root and
`~/.config/restor/config.yaml` (`$XDG_CONFIG_HOME/restor/config.yaml` if set) as any other
user, unless `--config <file>` (`-c`) says otherwise. There is no fallback from one to the
other: a normal user who wants the system configuration runs restor with `sudo`.
`--debug` logs every command line. Logs go to stderr, under systemd to the
journal: `journalctl -u restor` (or `journalctl --user -u restor`).

`restor run` exits 0 when every job was backed up and 1 when the config is invalid, a job
failed or the run was cancelled. A failed forget or prune is logged and runs the
top-level `on_failure` hook; a failed `post` or `on_failure` hook is only logged. Neither
changes the exit code. Before anything is stopped, it checks
that `restic` is installed and supported (see below). `restor snapshots`,
`restor restore` and `restor restic` exit with restic's exit code.

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
top-level key starting with `x-` and merge them with `<<: *name`. A file with only
`version` and `repository` is valid: `restor snapshots`, `restor restore` and
`restor restic` work with it, for example on a new host whose real configuration is still
in the backup; `restor run` then fails with "no jobs configured".

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
| `start_all` | after the backup, also start the services that were not running (default `false`) |
| `stop_timeout`, `start_timeout` | wait for the services to stop / start (default `1m` / `2m`) |
| `backup_timeout` | cancel a backup that takes longer; the services start again, the job fails |
| `keep` | retention: `last`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`, `within…`, and `tags` next to another rule (not one of the job's own `tags`) |
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
and only those are started again afterwards, also when the backup failed. With
`start_all: true` the backup also starts the job's other services (`services`, or all of
the compose file / all `units`), so a service that was down comes back up; a failed start
of those is only a warning and does not fail the job. `start_all` works with `stop: false`
too. Compose `start` does not create containers that were removed (`down`), and one-shot
services (init containers, `Type=oneshot` units) run again, so list `services` when the
project has some. After a cancel only the stopped services are started. A systemd unit
counts as started once `systemctl is-active` reports `active`. A `Type=simple` unit is
`active` as soon as its process runs, so restor cannot tell when it crashes right after
starting; use `Type=notify`, or check the unit in a `post` hook, when its start has to be
confirmed. A lock per compose file and per unit keeps two restor runs from working on the
same services; the second one fails.

**Dependencies between units:** restor stops and waits for the listed `units` only, and
it does not read systemd dependencies. A Podman quadlet pod runs every container in a unit
of its own: list the pod's unit and all of its containers' units, or the backup may start
while a container is still shutting down. A unit with `Wants=`, `Requires=` or `BindsTo=`
on another job's unit starts that unit again when it is started, which can cancel the
other job's stop while both run at once (systemctl reports "Job for … canceled"). Give
such a job `after` the other one, or set `concurrency: 1`.

**Hooks** are commands split on spaces and run without a shell; use a script with its
absolute path for anything more (a relative path resolves against the working directory,
`/` under systemd, and restor warns about it).

| Hook | Runs | Environment |
|---|---|---|
| `pre` | before the services are stopped; if it fails, the job fails and nothing else runs | `RESTOR_JOB`, `RESTOR_HOOK=pre` |
| `post` | after the services are started again, whenever `pre` succeeded | also `RESTOR_STATUS` (`success`, `incomplete`, `failed`), `RESTOR_SNAPSHOT_ID`, `RESTOR_ERROR` |
| job `on_failure` | after `post`, when the job failed | as `post` |
| top-level `post` | once after every run, after forget and prune | the run summary below |
| top-level `on_failure` | after the top-level `post`, when a job, forget or prune failed | the run summary below |

The top-level hooks get a summary of the whole run: `RESTOR_HOOK`, `RESTOR_STATUS`
(`failed` when anything failed, `incomplete` when a snapshot is incomplete, else
`success`), the comma-separated job names in `RESTOR_JOBS`, `RESTOR_SUCCEEDED_JOBS`,
`RESTOR_INCOMPLETE_JOBS` and `RESTOR_FAILED_JOBS`, the snapshots in `RESTOR_SNAPSHOT_IDS`
(`db=4f2a…,web=9c1e…`) and, when anything failed, every failure in `RESTOR_ERROR`
(`; `-separated). A top-level `post` is the place for one notification per run.

A failing `post` or `on_failure` hook is logged and changes nothing. After a cancel
(SIGTERM) the remaining hooks are skipped.

`example/hooks/` (in the release archives, and in `/usr/share/doc/restor/examples/hooks/`
of the packages) has scripts to copy and adapt; each one explains its use at the top:

| Script | Hooks | Does |
|---|---|---|
| `ntfy.sh` | top-level `post`, or job `pre`/`post` | push notification through [ntfy](https://ntfy.sh): one per run with every job's result and the errors, or one per job (started, backed up, unchanged, incomplete, failed); priority by result |
| `notifyapp.sh` | top-level `post`, or job `pre`/`post` | the same messages to the [Notify](https://getnotifyapp.com) iPhone app, one thread per host |
| `mail.sh` | top-level `post`, or job `post` | a mail through the local `sendmail` with the results and every `RESTOR_*` variable |
| `healthchecks.sh` | job `pre` + `post`, or top-level `post` | reports each job (or the whole run) to [Healthchecks](https://healthchecks.io): start, success, or failure with the error |

The notification scripts report whatever hook they run as, so the hooks pick the events,
and `NOTIFY_LEVEL` in `hooks.conf` drops the less important ones (`warning`: only runs
with incomplete or failed backups). For one message per run, set only the top-level
`post`:

```yaml
post: /etc/restor/hooks/ntfy.sh   # top level, next to jobs; not under defaults
```

A job's `post` already reports a failed job, and the top-level `post` a failed run; an
`on_failure` with the same script would repeat them.

```sh
sudo install -d -m 0700 /etc/restor/hooks
sudo install -m 0755 example/hooks/*.sh /etc/restor/hooks/
sudo install -m 0600 example/hooks/hooks.conf /etc/restor/hooks.conf  # tokens, targets
```

Tokens go into `hooks.conf`, not into the hook command: restor logs a hook's arguments
at debug level, and hooks do not see the env file.

**Retention:** after all backups, every job with `keep` and a complete snapshot runs
`restic forget --group-by=host --tag=<job>`, so each job's snapshots on this host are one
group, also when its `sources` or `tags` change. With `repository.prune`, one `prune` and
`unlock` follow, but only when every job of the run has a complete snapshot: a job that
failed, or whose snapshot is incomplete (restic could not read some files), keeps its
history and prune waits for a better run. A run that `skip_if_unchanged` skipped counts as
complete.

## Restore

`restor restore <snapshot-ID>` restores one snapshot with `restic restore`. Find the ID
with `restor snapshots` (or `--all`), or in the log line of the backup that made it; the
8-character short ID works. `latest` is refused: in a shared repository it is the newest
snapshot of any job and host.

```bash
restor restore 1a2b3c4d --target /tmp/restore            # dry run, lists every file
restor restore 1a2b3c4d --target /tmp/restore --apply    # /tmp/restore/<original path>
restor restore 1a2b3c4d --in-place --include /srv/app/db --apply
```

- **Dry run by default:** without `--apply` restic only lists what it would restore;
  nothing is written.
- **Where:** exactly one of `--target <dir>` and `--in-place` is required. `--target`
  restores the snapshot's absolute paths below `dir`: `--target /srv/app/db` writes
  `/srv/app/db/srv/app/db/…`, not into the original place. `--in-place` restores to the
  original paths; it is the same as `--target /`.
- **Which files:** `--include`, `--iinclude`, `--exclude`, `--iexclude` (patterns, as in
  restic); `--overwrite` `always` (restic's default), `if-changed`, `if-newer` or `never`;
  `--verify` reads the restored files back.
- **Files only:** restore does not stop or start services, take job locks or run hooks.
  Before an in-place restore, stop the services that use the files and `restor.timer`
  (`systemctl stop restor.timer`), so that no backup runs meanwhile.
- Only the repository part of the configuration is needed, so it works on a new host with
  a file that has `version` and `repository` only. Ownership is restored only when run
  as root.

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
| `forget_options` | `keep` (e.g. `keep: {daily: 7, tags: [important]}`) |
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
