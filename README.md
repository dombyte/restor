# Restor

Restic Backup Orchestrator - Back up services and files to a restic repository.

## Features

- Multiple projects from one config file
- Parallel or sequential execution
- Automatic container stop/start during backup
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

**Required:** `restic_repo`, `projects[]` with `compose_file`, `sources`, `stop_timeout`, `start_timeout`, `retention_policy`

**Global:** `auto_prune`, `prune_options`

**Per-project:** `forget_options`, `pre_backup_cmd`, `post_backup_cmd`

## License

MIT
