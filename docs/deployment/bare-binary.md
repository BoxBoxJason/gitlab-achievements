# Bare binary (systemd)

No containers: one static binary, a systemd unit, and a database you manage. Work through [GitLab-side setup](../gitlab-setup.md) first.

The unit and a template environment file are in [`deploy/systemd`](../../deploy/systemd).

## 1. Get the binary

From a [release](https://github.com/BoxBoxJason/gitlab-achievements/releases). `linux_amd64` and `linux_arm64` are published, alongside macOS and Windows builds:

```bash
VERSION=v0.1.1
curl -fsSLo gitlab-achievements \
  "https://github.com/BoxBoxJason/gitlab-achievements/releases/download/${VERSION}/gitlab-achievements_${VERSION}_linux_amd64"
sudo install -o root -g root -m 0755 gitlab-achievements /usr/local/bin/gitlab-achievements
gitlab-achievements --version
```

Or build it yourself, with Go 1.26 or later:

```bash
git clone https://github.com/BoxBoxJason/gitlab-achievements.git
cd gitlab-achievements && make build
sudo install -o root -g root -m 0755 ./bin/gitlab-achievements /usr/local/bin/
```

It is statically linked with no runtime dependencies, so any modern Linux works and no interpreter, libc version or package repository is involved.

## 2. Create the service account

An unprivileged system account that owns nothing but the process:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin gitlab-achievements
```

## 3. Configure it

```bash
sudo install -d -o root -g root -m 0750 /etc/gitlab-achievements
sudo install -o root -g root -m 0600 \
  deploy/systemd/gitlab-achievements.env.example \
  /etc/gitlab-achievements/gitlab-achievements.env
sudo $EDITOR /etc/gitlab-achievements/gitlab-achievements.env
```

`root:root 0600` is deliberate: systemd reads `EnvironmentFile` as root before dropping privileges, so the service account never needs to read the file that holds an instance-admin token. Anyone who can read it can administer your GitLab instance, so see the callout in [GitLab-side setup](../gitlab-setup.md#4-create-the-write-credential).

What every setting does is in [configuration.md](../configuration.md); the template lists them all.

## 4. The database

PostgreSQL, on this host or elsewhere:

```sql
CREATE USER achievements WITH PASSWORD '...';
CREATE DATABASE achievements OWNER achievements;
```

```ini
DATABASE_DSN=postgres://achievements:...@localhost:5432/achievements?sslmode=require
```

The app creates its own schema at startup, so an empty database is enough.

For a small install you can skip the server entirely and use SQLite:

```ini
DATABASE_DSN=sqlite:///var/lib/gitlab-achievements/data.db
```

The unit's `StateDirectory` creates `/var/lib/gitlab-achievements` owned by the service account, so that path works with no further setup. That file then holds every user's EXP and the record of which awards were delivered, so back it up.

## 5. Install the unit

```bash
sudo install -o root -g root -m 0644 \
  deploy/systemd/gitlab-achievements.service \
  /etc/systemd/system/gitlab-achievements.service
sudo systemctl daemon-reload
sudo systemctl enable --now gitlab-achievements
```

Watch the first start. Bootstrap runs before anything is served, verifying both tokens, creating 275 achievements, and registering a webhook on every group (or, on Free/CE, every project). Only then does the port open:

```bash
journalctl -u gitlab-achievements -f
curl -sf http://127.0.0.1:8080/readyz && echo ready
```

A failure here stops the process rather than serving half-working, so a restart loop means a real problem and the journal names it. Every permission problem is reported in one pass, not one restart at a time.

The unit ships with systemd's hardening options on (`ProtectSystem=strict`, no capabilities, a system-call filter, a private `/tmp`). Check what your kernel actually enforces with `systemd-analyze security gitlab-achievements`.

## 6. Put TLS in front

The app speaks plain HTTP. Bind it to localhost:

```ini
LISTEN_ADDR=127.0.0.1:8080
```

and run nginx, Caddy or HAProxy in front, terminating TLS for the hostname in `PUBLIC_URL` and proxying everything to it. Webhooks, the read API and the probes all share one port, so one proxy rule for `/` covers the lot. GitLab has to trust the certificate or deliveries fail silently.

## Day to day

```bash
systemctl status gitlab-achievements
journalctl -u gitlab-achievements -f
systemctl restart gitlab-achievements    # after editing the environment file
```

Rotating a token is an edit and a restart; nothing on GitLab's side has to be redone. Upgrading is replacing the binary and restarting:

```bash
sudo install -o root -g root -m 0755 gitlab-achievements /usr/local/bin/
sudo systemctl restart gitlab-achievements
```

Schema migrations run at startup, so an upgrade needs no separate step. Give shutdown its time: the unit allows 45 seconds, which is what draining webhook deliveries GitLab has already been told succeeded can take.

## Running the backfill separately

By default the service walks history in the background on first start. To make it its own job, set `BACKFILL=off` in the environment file and run the subcommand with the same configuration:

```bash
sudo systemd-run --uid=gitlab-achievements --gid=gitlab-achievements \
  --property=EnvironmentFile=/etc/gitlab-achievements/gitlab-achievements.env \
  --unit=gitlab-achievements-backfill --collect \
  /usr/local/bin/gitlab-achievements backfill

journalctl -u gitlab-achievements-backfill -f
```

It runs bootstrap first, then walks, resuming near where an interrupted run stopped. On a large instance it runs for a long time by design: `BACKFILL_RATE` caps it to a trickle against somebody's production GitLab. See [Historical backfill](../backfill.md).

## Running the reconciliation sync on a timer

By default the service re-reads the last 48 hours of activity a few minutes after startup and once a day thereafter, in the background, to pick up webhook deliveries GitLab never managed to make. See [Reconciliation](../reconciliation.md) for what it does and does not heal.

To make it a timer instead, so a pass is a unit you can start, watch and alert on at an hour you pick rather than whenever the service last restarted, set `RECONCILE=off` in the environment file and install these alongside the service unit:

```ini
# /etc/systemd/system/gitlab-achievements-reconcile.service
[Unit]
Description=GitLab Achievements: re-read recent activity
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=gitlab-achievements
Group=gitlab-achievements
EnvironmentFile=/etc/gitlab-achievements/gitlab-achievements.env
ExecStart=/usr/local/bin/gitlab-achievements reconcile
StateDirectory=gitlab-achievements
```

```ini
# /etc/systemd/system/gitlab-achievements-reconcile.timer
[Unit]
Description=Daily activity reconciliation for GitLab Achievements

[Timer]
OnCalendar=daily
# Spreads the sweep off the hour, so it doesn't land with everything else
# the host runs at midnight.
RandomizedDelaySec=1h
# A pass missed while the host was off is run at the next boot. Not strictly
# needed, since the next pass widens its window to reach back to the last
# successful one, but it shortens how long a lost delivery goes unhealed.
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now gitlab-achievements-reconcile.timer

# Run one now, and watch it.
sudo systemctl start gitlab-achievements-reconcile
journalctl -u gitlab-achievements-reconcile -f
```

The subcommand needs the database to have been bootstrapped once, by the service or by `backfill`, and says so rather than sweeping the instance for nothing.

## Uninstalling

Clear the GitLab side first, while the unit's configuration and database are still in place:

```bash
sudo systemctl stop gitlab-achievements

sudo systemd-run --uid=gitlab-achievements --gid=gitlab-achievements \
  --property=EnvironmentFile=/etc/gitlab-achievements/gitlab-achievements.env \
  --unit=gitlab-achievements-uninstall --collect --pipe --wait \
  /usr/local/bin/gitlab-achievements uninstall --dry-run

sudo systemd-run --uid=gitlab-achievements --gid=gitlab-achievements \
  --property=EnvironmentFile=/etc/gitlab-achievements/gitlab-achievements.env \
  --unit=gitlab-achievements-uninstall --collect --pipe --wait \
  /usr/local/bin/gitlab-achievements uninstall
```

`uninstall` removes the webhooks and the achievements it created. Pass `--keep-achievements` to take only the hooks and leave people the badges they earned, because deleting an achievement deletes every award of it and nothing brings those back. Full details in [Uninstalling](../uninstall.md).

Then remove the app itself:

```bash
sudo systemctl disable --now gitlab-achievements
sudo rm /etc/systemd/system/gitlab-achievements.service /usr/local/bin/gitlab-achievements
sudo rm -rf /etc/gitlab-achievements
sudo systemctl daemon-reload
```

The database is yours and is left alone. Dropping it before running `uninstall` leaves the app no record of what it registered; `uninstall --sweep` enumerates the instance to recover from that.
