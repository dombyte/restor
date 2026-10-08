# Restor

Restic Backup Orchestrator - Back up services and files to a restic repository.

## Features

- Multiple projects from one config file
- Parallel or sequential execution
- Automatic service stop/start during backup (Docker/Podman Compose, systemd)
- Per-project retention policies (forget) with a single global prune
- Pre/post backup hooks
- YAML, JSON, TOML config support

## Quick Start

1. Copy and edit a config file:
   ```bash
   cp example/config.yaml config.yaml
   # Edit config.yaml with your restic repo and projects
   ```

2. Run manually:
   ```bash
   ./restor --config config.yaml
   ```

## Systemd Installation

### 1. Install binary

Copy `restor` to `/usr/local/bin/restor`:
```bash
sudo cp restor /usr/local/bin/restor
```

### 2. Install config

Copy your config to `/etc/restor/config.yaml`:
```bash
sudo mkdir -p /etc/restor
sudo cp config.yaml /etc/restor/
```

### 3. Install systemd files

```bash
sudo cp example/restor.service /etc/systemd/system/
sudo cp example/restor.timer /etc/systemd/system/
```

### 4. Enable and start

```bash
sudo systemctl daemon-reload
sudo systemctl enable restor.service
sudo systemctl start restor.timer
sudo systemctl enable restor.timer
```

### 5. Verify

```bash
systemctl list-timers
sudo systemctl status restor.timer
sudo journalctl -u restor -f
```

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
from the process environment when the config is loaded.

**Project names** are case-insensitive; the lowercase name is the restic tag.

**List options** (`prune_options`, `backup_options`, `forget_options`) are passed as one
argument per item: write `["-o", "s3.connections=10"]` or `["--option=s3.connections=10"]`.

## License

MIT
