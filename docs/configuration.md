# Configuration

Every setting is available as a command-line flag and as an environment
variable, so no deployment target needs a config file or a code change. A flag
wins over the matching variable; unset falls back to the default.

Everything is validated at startup, all at once: a misconfigured deployment
reports every problem it has in one message rather than one per restart.

## Required

The app refuses to start without these seven.

| Flag | Environment | What it is |
| --- | --- | --- |
| `--gitlab-url` | `GITLAB_URL` | Base URL of the GitLab instance to mirror, e.g. `https://gitlab.example.com` |
| `--gitlab-read-token` | `GITLAB_READ_TOKEN` | Token with `read_api` scope, used for all data fetching |
| `--gitlab-write-token` | `GITLAB_WRITE_TOKEN` | Token with `api` scope on the instance-admin account, used to manage webhooks and achievements. Must differ from the read token |
| `--achievements-namespace` | `ACHIEVEMENTS_NAMESPACE` | Full path of the group owning the achievement definitions, e.g. `achievements` |
| `--database-dsn` | `DATABASE_DSN` | Where state is kept; see [Databases](#databases) |
| `--webhook-secret` | `WEBHOOK_SECRET` | Shared secret GitLab presents on every delivery |
| `--public-url` | `PUBLIC_URL` | Base URL GitLab reaches this app at. Trailing slashes are stripped; the webhook path is appended to it |

Where the two credentials come from, and what trusting the admin one implies,
is in [GitLab-side setup](gitlab-setup.md).

## Optional

| Flag | Environment | Default | What it does |
| --- | --- | --- | --- |
| `--listen-addr` | `LISTEN_ADDR` | `:8080` | Address the HTTP server binds to |
| `--log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `--hook-scope` | `HOOK_SCOPE` | `auto` | `auto`, `group`, `project`; see [Webhook scope](#webhook-scope) |
| `--hook-rate` | `HOOK_RATE` | `20` | Groups or projects per second the hourly registration sweep works through |
| `--backfill` | `BACKFILL` | `auto` | `auto`, `off`, `force`; see [Historical backfill](#historical-backfill) |
| `--backfill-since` | `BACKFILL_SINCE` | *(unset)* | How far back history is walked: a date (`2024-01-01`) or a duration (`720h`). Unset walks everything |
| `--backfill-rate` | `BACKFILL_RATE` | `5` | Requests per second the historical walk may issue |
| `--api-auth` | `API_AUTH` | `none` | `none`, or `gitlab` to verify a GitLab token on every API request |
| `--oauth-client-id` | `OAUTH_CLIENT_ID` | *(unset)* | Client ID of an OAuth application you registered by hand. Unset lets the app register a public one for itself |
| `--oauth-client-secret` | `OAUTH_CLIENT_SECRET` | *(unset)* | Secret for that client ID, making it a confidential client. Requires `--oauth-client-id` |

## Databases

The DBMS is selected from the DSN's scheme:

| Scheme | Example |
| --- | --- |
| `postgres://`, `postgresql://` | `postgres://user:password@localhost:5432/achievements?sslmode=require` |
| `sqlite://`, `sqlite3://` | `sqlite:///var/lib/gitlab-achievements/data.db` |
| `mysql://` | `mysql://user:password@tcp(localhost:3306)/achievements?parseTime=true` |
| `sqlserver://` | `sqlserver://user:password@localhost:1433?database=achievements` |

PostgreSQL is what the deployment guides assume. SQLite is a reasonable choice
for a small single-host install, where it makes the app a single self-contained
process with a file for state; note that the file is then the only copy of
every user's EXP, so it belongs on backed-up storage rather than in a container
that will be replaced.

Migrations run at startup against whichever you pick. The schema is created on
first start, so an empty database is all that has to exist.

## Webhook scope

`auto` reads the instance's license and registers one hook per top-level group
where group webhooks are available (Premium/Ultimate), or one hook per project
otherwise (Free/CE). Pin it to `group` or `project` when the write token cannot
read the license, or when you would rather not have the choice depend on a
licence change.

`--hook-rate` is what keeps the hourly sweep from being a permanent background
load on the instance: on Free/CE it touches every project there is. Lower it on
a busy instance; a slower sweep only means a hook deleted out of band is
repaired a little later.

## Historical backfill

`auto` walks the instance's history once, in the background, after bootstrap: a
restart resumes an interrupted walk, and a finished one is never repeated.

`off` never walks from the serving process, leaving it to an explicit
`gitlab-achievements backfill` run — a Kubernetes Job, a one-off container, or
a `systemctl start gitlab-achievements-backfill`. The subcommand takes the same
flags and environment as the server, and runs bootstrap first, so it does not
need the server to have started.

`force` walks history again even though a previous walk finished. It exists for
recovering from a walk that ran against a broken catalog, not for steady state.

`--backfill-rate` is deliberately low. The walk is the heaviest read workload
the app ever runs against an instance it does not own, and it has no deadline;
raising it shortens the cold start at the cost of API capacity the instance
exists to serve.

## HTTP endpoints

Everything is served on `--listen-addr`, on one port.

| Path | Purpose |
| --- | --- |
| `/healthz` | Liveness. Answers 200 as soon as the process serves at all |
| `/readyz` | Readiness. 200 once bootstrap has finished *and* the database and GitLab are reachable right now |
| `/webhooks/gitlab` | Where GitLab's deliveries arrive. `POST` only, authenticated with the webhook secret |
| `/api/v1/...` | The read API: EXP, progress, leaderboard |
| `/oauth/...` | Browser login flow, when `--api-auth=gitlab` |

Bootstrap runs *before* the server listens, so nothing answers during it — on a
Free-tier instance with thousands of projects that can be several minutes. Give
health checks room for it: a startup probe on Kubernetes, a generous
`start_period` anywhere else.

`/healthz` and `/readyz` are unauthenticated by design, and so is the read API
unless `--api-auth=gitlab` is set.
