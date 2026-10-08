# Restor

Restic Backup Orchestrator - Back up services and files to a restic repository.

## Features

- Multiple projects from one config file
- Parallel or sequential execution
- Automatic service stop/start during backup (Docker/Podman Compose, systemd)
- Per-project retention policies (forget) with a single global prune
- Pre/post backup hooks
- YAML, JSON, TOML config support

## Installation

Download a package or archive from the
[releases](https://github.com/dombyte/restor/releases). restor needs `restic`; the packages
install it as a recommended dependency.

### Package (.deb / .rpm)

```bash
sudo apt install ./restor_<version>_amd64.deb       # Debian, Ubuntu
sudo dnf install ./restor-<version>-1.x86_64.rpm    # Fedora, RHEL
```

Create the config and enable the daily timer:

```bash
sudo cp /usr/share/doc/restor/examples/config.yaml /etc/restor/config.yaml
sudoedit /etc/restor/config.yaml
sudo systemctl enable --now restor.timer
```

### Archive

```bash
tar xzf restor_<version>_linux_amd64.tar.gz
cd restor_<version>_linux_amd64
sudo install -m 0755 restor /usr/local/bin/restor
sudo install -d -m 0700 /etc/restor
sudo cp example/config.yaml /etc/restor/config.yaml
sudoedit /etc/restor/config.yaml
sudo cp example/restor.service example/restor.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now restor.timer
```

### As a normal user

The package also installs user units that read `~/.config/restor/config.yaml`:

```bash
mkdir -p ~/.config/restor
cp /usr/share/doc/restor/examples/config.yaml ~/.config/restor/
systemctl --user enable --now restor.timer
sudo loginctl enable-linger "$USER"   # keep the timer running while logged out
```

To run a backup by hand: `restor --config <file>`. Logs: `journalctl -u restor` (or
`journalctl --user -u restor`).

## Configuration

See `example/config.yaml` for all options and `example/restor.env` for an env file template.
The config file can be YAML, JSON or TOML (chosen by extension).

**Global:** `mode` (`sequential`, the default, runs projects sorted by name; `parallel` runs
them all at once), `restic_repo`, `auto_prune`, `prune_options`, `pre_backup_cmd`,
`post_backup_cmd`

**Per project (required):** `service_manager` (`docker-compose`, `podman-compose`,
`systemd` or `noop`), `sources`, plus `compose_file` (compose) or `systemd_units` (systemd);
`stop_timeout` and `start_timeout` (seconds) when services are stopped

**Per project (optional):** `services`, `systemd_scope`, `stop_services`,
`retention_policy`, `backup_options`, `forget_options`, `pre_backup_cmd`, `post_backup_cmd`

restor checks the whole file at startup and lists every problem with its field path.

**Environment:** `env_file` and inline `environments` are passed to restic, the service
managers and the hooks (inline wins). `$VAR`, `${VAR}` and `${VAR:-default}` are expanded
from the process environment when the config is loaded; write `$$` for a literal `$`
(e.g. in a password). In `.env` files a value in single quotes is never expanded.

**Project names** are case-insensitive; the lowercase name is the restic tag.

**List options** (`prune_options`, `backup_options`, `forget_options`) are passed as one
argument per item: write `["-o", "s3.connections=10"]` or `["--option=s3.connections=10"]`.

## License

MIT
