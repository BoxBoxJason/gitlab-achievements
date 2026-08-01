# Docker Compose

The self-contained option: the app and a PostgreSQL to keep its state in, on
one host, from one file. Work through [GitLab-side setup](../gitlab-setup.md)
first.

## Install

```bash
git clone https://github.com/BoxBoxJason/gitlab-achievements.git
cd gitlab-achievements
cp .env.example .env
$EDITOR .env
docker compose up -d
```

You only need [`docker-compose.yml`](../../docker-compose.yml) and
[`.env.example`](../../.env.example) — download the two if you would rather not
clone the repository.

`.env` holds both GitLab tokens, one of them instance-admin. It should be mode
`0600`, owned by whoever runs compose, and never committed; the repository's
`.gitignore` already covers it.

Watch the first start, which runs bootstrap before anything is served:

```bash
docker compose logs -f app
curl -sf http://127.0.0.1:8080/readyz && echo ready
```

The app image is built `FROM scratch` and holds nothing but the binary, so
there is no shell to exec into and no in-container healthcheck. Logs and
`/readyz` from the host are how you check on it.

## What to set

Everything in `.env.example` is annotated; the seven required settings are
listed in [configuration.md](../configuration.md). Two are compose-specific:

- **`DATABASE_DSN`** points at the bundled database by hostname `db`:
  `postgres://achievements:<password>@db:5432/achievements?sslmode=disable`.
  TLS is off because the connection never leaves compose's own network.
- **`POSTGRES_PASSWORD`** must be the same password as the one in that DSN. It
  is spelled twice because the app and PostgreSQL are configured separately —
  the most common reason a first start fails on the database.

To use a database you already run instead, point `DATABASE_DSN` at it and drop
the `db` service with an override file (`!reset` needs Compose 2.24 or later):

```yaml
# docker-compose.override.yml
services:
  app:
    depends_on: !reset null
  db: !reset null
```

## Putting TLS in front

The app is published on `127.0.0.1:8080` by default and speaks plain HTTP. That
is not what GitLab should be delivering to: `PUBLIC_URL` needs to be an address
the instance can reach, with a certificate the instance trusts.

Run a reverse proxy — Caddy, nginx, Traefik — on the same host, terminating TLS
for `achievements.example.com` and proxying to `127.0.0.1:8080`. Everything is
one origin: the webhook endpoint, the read API and the probes all share the
port, so a single `proxy_pass` covers it.

Publishing the port directly instead (`LISTEN_ADDRESS=0.0.0.0`) means GitLab
delivers webhooks, and anyone else reaches the API, over unencrypted HTTP on a
public interface. Only reasonable on a trusted private network.

## Day-to-day

```bash
docker compose logs -f app          # follow
docker compose restart app          # after editing .env
docker compose pull && docker compose up -d   # upgrade
docker compose down                 # stop, keeping the database volume
```

Rotating a token is an edit to `.env` and a `restart`; nothing on GitLab's side
has to be re-done. Upgrading pulls a new image and recreates the container,
which re-runs bootstrap and the schema migrations. Pin `IMAGE_TAG` to a release
rather than tracking `latest`, so an upgrade is something you choose.

`docker compose down` leaves the `pgdata` volume in place; `down -v` deletes it,
and with it every user's EXP and the record of which awards were already
delivered. Back that volume up if the numbers matter:

```bash
docker compose exec db pg_dump -U achievements achievements > backup.sql
```

## Running the backfill separately

By default the app walks history in the background on first start. To run it as
its own job instead, set `BACKFILL=off` in `.env` and run the subcommand with
the same environment:

```bash
docker compose run --rm app backfill
```

It runs bootstrap first, then walks, resuming near where an interrupted run
stopped. On a large instance it is a long job by design: `BACKFILL_RATE`
deliberately caps it to a trickle against someone's production GitLab.

## Running the reconciliation sync separately

By default the app re-reads the last 48 hours of activity a few minutes after
startup and once a day thereafter, picking up webhook deliveries GitLab never
managed to make. See
[Reconciliation sync](../configuration.md#reconciliation-sync) for what it does
and does not heal.

That is the right setting for a Compose deployment, which is a single app
container. To hand it to the host's cron instead, set `RECONCILE=off` in `.env`
and schedule:

```bash
docker compose run --rm app reconcile
```

Compose has no scheduler of its own, so this needs a crontab entry (or a
systemd timer) on the host pointing at the project directory:

```cron
0 3 * * *  cd /srv/gitlab-achievements && docker compose run --rm app reconcile
```

The subcommand needs the database to have been bootstrapped once, by the app or
by `backfill`, and says so rather than sweeping the instance for nothing.

## Uninstalling

`docker compose down -v` removes the app and its database, but nothing it put
on GitLab. Clear that first, while the database it reads its records from is
still there:

```bash
docker compose stop app                      # stop it re-registering what you remove
docker compose run --rm app uninstall --dry-run
docker compose run --rm app uninstall
docker compose down -v
```

`uninstall` removes the webhooks and the achievements it created; pass
`--keep-achievements` to take only the hooks and leave people the badges they
earned. Deleting an achievement deletes every award of it, and nothing brings
those back.

Run it before `down -v`, not after: the removal is driven by the records in the
database, so a volume deleted first leaves the app no idea what to clean up.
Recovering from that means `uninstall --sweep`, which enumerates the instance
instead.
