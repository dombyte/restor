# Docker Backup

Backup Docker containers and volumes to a restic repository.

## Features

- Multiple Docker Compose projects from one config file
- Parallel or sequential execution
- Automatic container stop/start during backup
- Per-project retention policies (forget) with a single global prune
- Pre/post backup hooks
- YAML, JSON, TOML config support

## Quick Start

1. Copy and edit a config file:
   ```bash
   cp config.example.yaml config.yaml
   # Edit config.yaml with your restic repo and projects
   ```

2. Run manually:
   ```bash
   ./dbk --config config.yaml
   ```

## Systemd Installation

### 1. Install binary

Copy `dbk` to `/usr/local/bin/dbk`:
```bash
sudo cp dbk /usr/local/bin/dbk
```

### 2. Install config

Copy your config to `/etc/dbk/config.yaml`:
```bash
sudo mkdir -p /etc/dbk
sudo cp config.yaml /etc/dbk/
```

### 3. Install systemd files

```bash
sudo cp dbk.service /etc/systemd/system/
sudo cp dbk.timer /etc/systemd/system/
```

### 4. Enable and start

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now dbk.timer
```

### 5. Verify

```bash
sudo systemctl status dbk.timer
sudo journalctl -u dbk -f
```

## Configuration

See `config.example.yaml` for all options.

**Required:** `restic_repo`, `projects[]` with `compose_file`, `sources`, `stop_timeout`, `start_timeout`, `retention_policy`

**Global:** `auto_prune`, `prune_options`

**Per-project:** `forget_options`, `pre_backup_cmd`, `post_backup_cmd`

## License

MIT
