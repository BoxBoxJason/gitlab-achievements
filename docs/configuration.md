# Configuration

Every setting is a command-line flag and an environment variable, so no deployment target needs a config file or a code change. A flag wins over the matching variable; unset falls back to the default.

Everything is validated at startup, all at once. A misconfigured deployment reports every problem it has in one message rather than one per restart.

## Required

The app refuses to start without these six.

| Flag | Environment | What it is |
| --- | --- | --- |
| `--gitlab-url` | `GITLAB_URL` | Base URL of the GitLab instance to mirror, e.g. `https://gitlab.example.com` |
| `--gitlab-read-token` | `GITLAB_READ_TOKEN` | Token with `read_api` scope, used for all data fetching |
| `--gitlab-write-token` | `GITLAB_WRITE_TOKEN` | Token with `api` scope on an instance-admin account, used to manage webhooks and achievements. Must differ from the read token |
| `--achievements-namespace` | `ACHIEVEMENTS_NAMESPACE` | Full path of the group owning the achievement definitions, e.g. `achievements` |
| `--webhook-secret` | `WEBHOOK_SECRET` | Shared secret GitLab presents on every delivery, at least 32 characters, e.g. from `openssl rand -hex 32` |
| `--public-url` | `PUBLIC_URL` | Public base URL of this app, used for GitLab OAuth redirects and browser sessions |

`--database-dsn` / `DATABASE_DSN` is not in this list: left unset, it defaults to a local SQLite file, see [Databases](#databases).

Where the two credentials come from, and what trusting the admin one implies, is in [GitLab-side setup](gitlab-setup.md).

## Optional

| Flag | Environment | Default | What it does |
| --- | --- | --- | --- |
| `--database-dsn` | `DATABASE_DSN` | `sqlite:///var/lib/gitlab-achievements/data.db` | Where state is kept, see [Databases](#databases) |
| `--webhook-url` | `WEBHOOK_URL` | `PUBLIC_URL` | Base URL GitLab reaches for webhooks. Trailing slashes are stripped and the webhook path is appended. Set this to an in-cluster URL when GitLab can reach the cluster but users cannot |
| `--listen-addr` | `LISTEN_ADDR` | `:8080` | Address the HTTP server binds to |
| `--metrics-addr` | `METRICS_ADDR` | `:9464` | Address Prometheus metrics are served on, apart from `--listen-addr`. `off` serves none. See [Metrics](metrics.md) |
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
| `--exclude-bots` | `EXCLUDE_BOTS` | `true` | Leave out accounts GitLab flags as bots. See [Who takes part](participation.md) |
| `--exclude-users` | `EXCLUDE_USERS` | *(unset)* | Usernames or GitLab user IDs to leave out, comma-separated |
| `--exclude-username-pattern` | `EXCLUDE_USERNAME_PATTERN` | *(unset)* | Regular expression; matching usernames are left out |
| `--allow-opt-out` | `ALLOW_OPT_OUT` | `true` | Let users leave themselves out through the API. Needs `--api-auth gitlab` |
| `--session-secret` | `SESSION_SECRET` | *(required with `API_AUTH=gitlab`)* | Key the GitLab tokens browser sessions hold are encrypted under, at least 32 characters, e.g. from `openssl rand -base64 48`. Changing it signs everybody out |
| `--session-lifetime` | `SESSION_LIFETIME` | `168h` | How long a browser stays signed in, greater than 0. Sessions renew their GitLab token as it runs out |
| `--api-auth-cache-ttl` | `API_AUTH_CACHE_TTL` | `1m` | How long a verified credential is trusted before GitLab is asked again, and so how long a revoked token keeps working. `0s` disables, at most `1h` |
| `--api-allowed-group` | `API_ALLOWED_GROUP` | *(unset)* | Full path of a group; only its members and instance admins may use the API. Needs `--api-auth gitlab` |
| `--oauth-return-origins` | `OAUTH_RETURN_ORIGINS` | *(unset)* | Origins besides `--public-url`'s that a login may return to, comma-separated, e.g. `https://rankings.example.com` |
| `--frontend-dir` | `FRONTEND_DIR` | *(unset)* | Directory of a built static frontend served at `/` in place of the built-in one. See [The frontend](frontend.md#serving-a-different-frontend) |
| `--frontend-csp` | `FRONTEND_CSP` | *(the built-in frontend's)* | Content Security Policy sent with `FRONTEND_DIR`; `off` sends none |

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

SQLite is the default: nothing to stand up alongside the app, at the cost of being a single file rather than a service, so it belongs on backed-up storage rather than in a container that will be replaced (see the [Helm chart](deployment/kubernetes.md#the-database)'s PVC for the Kubernetes case). PostgreSQL, MySQL, and SQL Server are the scale-up path once one process and one file stop being enough, or where operational convention already points at a managed database.

SQLite is opened with `busy_timeout(10000)`, `journal_mode(WAL)` and `_txlock=immediate` unless the DSN sets them itself. The app always has several writers at once (the webhook workers, the backfill, the sweeps), and without these SQLite answers contention with `SQLITE_BUSY` rather than a wait. Override any of them in the DSN's query string, e.g. `sqlite:///data/data.db?_pragma=journal_mode(DELETE)` on a filesystem that cannot hold WAL's shared-memory file (some network filesystems). The SQLite driver stops reading the query string at `_time_format`, so a DSN that sets it loses `_txlock` whether the app added it or you did.

Migrations run at startup against whichever you pick. The schema is created on first start, so an empty database is all that has to exist.

## Tuning notes

**`--hook-rate`** is what keeps the hourly sweep from being a permanent background load on your instance. On Free/CE it touches every project there is. Lower it on a busy instance; a slower sweep only means a hook deleted out of band is repaired a little later.

**`--backfill-rate`** is low for a reason. The walk is the heaviest read workload the app ever runs against an instance it does not own, and it has no deadline. Raising it shortens the cold start at the cost of API capacity your instance exists to serve.

**`--reconcile-lookback`** has to be wider than `--reconcile-interval` so consecutive windows overlap. Windows that merely abut lose anything GitLab timestamps on the far side of the boundary, and nothing would report the loss, so the app refuses to start on a configuration where they do.

## Endpoints

Everything is served on `--listen-addr`, on one port. The list is in [The API](api.md#the-other-endpoints).
