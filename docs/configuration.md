# Configuration

Every setting is a command-line flag and an environment variable, so no deployment target needs a config file or a code change. A flag wins over the matching variable; unset falls back to the default.

Everything is validated at startup, all at once. A misconfigured deployment reports every problem it has in one message rather than one per restart.

## Required

The app refuses to start without these seven.

| Flag | Environment | What it is |
| --- | --- | --- |
| `--gitlab-url` | `GITLAB_URL` | Base URL of the GitLab instance to mirror, e.g. `https://gitlab.example.com` |
| `--gitlab-read-token` | `GITLAB_READ_TOKEN` | Token with `read_api` scope, used for all data fetching |
| `--gitlab-write-token` | `GITLAB_WRITE_TOKEN` | Token with `api` scope on an instance-admin account, used to manage webhooks and achievements. Must differ from the read token |
| `--achievements-namespace` | `ACHIEVEMENTS_NAMESPACE` | Full path of the group owning the achievement definitions, e.g. `achievements` |
| `--database-dsn` | `DATABASE_DSN` | Where state is kept, see [Databases](#databases) |
| `--webhook-secret` | `WEBHOOK_SECRET` | Shared secret GitLab presents on every delivery |
| `--public-url` | `PUBLIC_URL` | Base URL GitLab reaches this app at. Trailing slashes are stripped; the webhook path is appended |

Where the two credentials come from, and what trusting the admin one implies, is in [GitLab-side setup](gitlab-setup.md).

## Optional

| Flag | Environment | Default | What it does |
| --- | --- | --- | --- |
| `--listen-addr` | `LISTEN_ADDR` | `:8080` | Address the HTTP server binds to |
| `--log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `--hook-scope` | `HOOK_SCOPE` | `auto` | `auto`, `group`, `project`, see [Webhooks](webhooks.md#which-kind-and-why) |
| `--hook-rate` | `HOOK_RATE` | `20` | Groups or projects per second the hourly registration sweep works through |
| `--backfill` | `BACKFILL` | `auto` | `auto`, `off`, `force`, see [Historical backfill](backfill.md#running-it) |
| `--backfill-since` | `BACKFILL_SINCE` | *(unset)* | How far back history is walked: a date (`2024-01-01`) or a duration (`720h`). Unset walks everything |
| `--backfill-rate` | `BACKFILL_RATE` | `5` | Requests per second the historical walk may issue. Also caps the reconciliation sync |
| `--reconcile` | `RECONCILE` | `auto` | `auto`, `off`, see [Reconciliation](reconciliation.md#running-it) |
| `--reconcile-interval` | `RECONCILE_INTERVAL` | `24h` | How often recent activity is re-read to heal lost webhook deliveries |
| `--reconcile-lookback` | `RECONCILE_LOOKBACK` | `48h` | How far back each pass reaches. Must exceed `--reconcile-interval` |
| `--api-auth` | `API_AUTH` | `none` | `none`, or `gitlab` to verify a GitLab token on every API request |
| `--oauth-client-id` | `OAUTH_CLIENT_ID` | *(unset)* | Client ID of an OAuth application you registered by hand. Unset lets the app register a public one for itself |
| `--oauth-client-secret` | `OAUTH_CLIENT_SECRET` | *(unset)* | Secret for that client ID, making it a confidential client. Requires `--oauth-client-id` |

## Subcommands

All of them take the same flags and environment as the server.

| Command | What it does |
| --- | --- |
| `gitlab-achievements` | The server: bootstrap, ingestion, backfill, reconciliation, the API |
| `gitlab-achievements backfill` | Bootstraps, then walks history once and exits. See [Historical backfill](backfill.md) |
| `gitlab-achievements reconcile` | Re-reads recent activity once and exits. Does not bootstrap. See [Reconciliation](reconciliation.md) |
| `gitlab-achievements uninstall` | Removes the webhooks and achievements from GitLab. See [Uninstalling](uninstall.md) |

## Databases

The DBMS is picked from the DSN's scheme:

| Scheme | Example |
| --- | --- |
| `postgres://`, `postgresql://` | `postgres://user:password@localhost:5432/achievements?sslmode=require` |
| `sqlite://`, `sqlite3://` | `sqlite:///var/lib/gitlab-achievements/data.db` |
| `mysql://` | `mysql://user:password@tcp(localhost:3306)/achievements?parseTime=true` |
| `sqlserver://` | `sqlserver://user:password@localhost:1433?database=achievements` |

PostgreSQL is what the deployment guides assume. SQLite is a reasonable choice for a small single-host install, where it makes the app a self-contained process with a file for state. Note that the file is then the only copy of every user's EXP, so it belongs on backed-up storage rather than in a container that will be replaced.

Migrations run at startup against whichever you pick. The schema is created on first start, so an empty database is all that has to exist.

## Tuning notes

**`--hook-rate`** is what keeps the hourly sweep from being a permanent background load on your instance. On Free/CE it touches every project there is. Lower it on a busy instance; a slower sweep only means a hook deleted out of band is repaired a little later.

**`--backfill-rate`** is low for a reason. The walk is the heaviest read workload the app ever runs against an instance it does not own, and it has no deadline. Raising it shortens the cold start at the cost of API capacity your instance exists to serve.

**`--reconcile-lookback`** has to be wider than `--reconcile-interval` so consecutive windows overlap. Windows that merely abut lose anything GitLab timestamps on the far side of the boundary, and nothing would report the loss, so the app refuses to start on a configuration where they do.

## Endpoints

Everything is served on `--listen-addr`, on one port. The list is in [The API](api.md#the-other-endpoints).
