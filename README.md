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

Install from a [package](#install-from-a-deb-or-rpm-package) or a
[release archive](#manual-installation-release-archive), or build with `make build`. To try
it without installing:

1. Copy and edit a config file:
   ```bash
   cp example/config.yaml config.yaml
   # Edit config.yaml with your restic repo and projects
   ```

2. Run manually:
   ```bash
   ./restor --config config.yaml
   ```

## Install from a .deb or .rpm package

Each [release](https://github.com/dombyte/restor/releases) has packages for amd64, arm64 and
armv7 (`armhf` / `armv7hl`), next to `checksums.txt`. They install:

| Path | Content |
|---|---|
| `/usr/bin/restor` | the binary |
| `/usr/lib/systemd/system/restor.{service,timer}` | system units (config `/etc/restor/config.yaml`) |
| `/usr/lib/systemd/user/restor.{service,timer}` | user units (config `~/.config/restor/config.yaml`) |
| `/usr/share/doc/restor/examples/` | `config.yaml` and `restor.env` templates |
| `/etc/restor/` | empty, root only (0700) |

`restic` is a recommended dependency: apt and dnf install it unless you have it already or
turn recommends off. Docker or Podman are not pulled in.

### 1. Download, verify and install

```bash
v=1.2.3   # the release version without "v"
base=https://github.com/dombyte/restor/releases/download/v$v
curl -LO "$base/checksums.txt"

# Debian, Ubuntu
curl -LO "$base/restor_${v}_amd64.deb"
sha256sum --check --ignore-missing checksums.txt
sudo apt install "./restor_${v}_amd64.deb"

# Fedora, RHEL, openSUSE (zypper install)
curl -LO "$base/restor-${v}-1.x86_64.rpm"
sha256sum --check --ignore-missing checksums.txt
sudo dnf install "./restor-${v}-1.x86_64.rpm"
```

Replace `amd64`/`x86_64` with `arm64`/`aarch64` or `armhf`/`armv7hl` on ARM.

### 2. Configure

```bash
sudo cp /usr/share/doc/restor/examples/config.yaml /etc/restor/config.yaml
sudoedit /etc/restor/config.yaml

# Secrets: an env file (set env_file: /etc/restor/restor.env in the config) and the password
sudo cp /usr/share/doc/restor/examples/restor.env /etc/restor/restor.env
sudoedit /etc/restor/restor.env
sudo sh -c 'umask 077; printf %s "your-password" > /etc/restor/restic-password'
```

### 3. Test once, then enable the timer

The timer is not enabled by the package, because a run fails until the config is written.

```bash
sudo restor --config /etc/restor/config.yaml   # or: sudo systemctl start restor.service
sudo systemctl enable --now restor.timer
systemctl list-timers restor.timer
journalctl -u restor -e
```

The timer runs daily at 01:00 and catches up a missed run after boot. To change the time:
`sudo systemctl edit restor.timer`.

### Upgrade

Install the newer package the same way (`apt install ./…deb`, `dnf install ./…rpm`). The
config in `/etc/restor` and the enabled timer are kept; the next run uses the new binary.

### Remove

```bash
sudo apt remove restor     # or: sudo dnf remove restor
```

This disables the system timer and keeps `/etc/restor` with your files (delete it by hand).
Users who enabled their own timer should run `systemctl --user disable restor.timer`
first.

### Switch from a manual installation

Units in `/etc/systemd/system/` take precedence over the packaged ones and still point to
`/usr/local/bin/restor`. Remove them after installing the package:

```bash
sudo systemctl disable --now restor.timer
sudo rm /etc/systemd/system/restor.service /etc/systemd/system/restor.timer \
  /usr/local/bin/restor
sudo systemctl daemon-reload
sudo systemctl enable --now restor.timer
```

An existing `/etc/restor/config.yaml` is used as is.

### Run as a normal user

A user can back up what they can read and manage their own services (`systemd_scope: user`,
rootless podman; the `docker` group is effectively root). The user units read
`~/.config/restor/config.yaml`:

```bash
mkdir -p ~/.config/restor
cp /usr/share/doc/restor/examples/config.yaml ~/.config/restor/config.yaml
chmod 600 ~/.config/restor/config.yaml
"$EDITOR" ~/.config/restor/config.yaml

systemctl --user daemon-reload          # only needed if you were logged in during install
restor --config ~/.config/restor/config.yaml   # test once
systemctl --user enable --now restor.timer
sudo loginctl enable-linger "$USER"     # keep the timer running while logged out
journalctl --user -u restor -e
```

Each user has their own lock directory (`/run/restor` for root, `$XDG_RUNTIME_DIR/restor`
or `$TMPDIR/restor-<uid>` otherwise), so runs of different users never block each other.
Locks of different users are not shared: do not let root and a user back up the same
compose file.

## Manual Installation (release archive)

Download `restor_<version>_<linux|darwin>_<arch>.tar.gz` from the
[releases](https://github.com/dombyte/restor/releases) and unpack it. It contains the
binary, this README, the license and `example/`.

### 1. Install binary

```bash
sudo install -m 0755 restor /usr/local/bin/restor
```

### 2. Install config

```bash
sudo install -d -m 0700 /etc/restor
sudo cp example/config.yaml /etc/restor/config.yaml
sudoedit /etc/restor/config.yaml
```

For secrets, see step 2 of the package installation above.

### 3. Install systemd files

```bash
sudo cp example/restor.service example/restor.timer /etc/systemd/system/
sudo systemctl daemon-reload
```

### 4. Test once, then enable the timer

```bash
sudo restor --config /etc/restor/config.yaml
sudo systemctl enable --now restor.timer
```

Enable only the timer, not `restor.service`: an enabled service would also run a backup at
every boot.

### 5. Verify

```bash
systemctl list-timers restor.timer
sudo systemctl status restor.timer
journalctl -u restor -f
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
