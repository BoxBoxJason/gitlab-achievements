# GitLab Achievements

An event-driven **achievement awarder bot** for self-hosted GitLab instances. Point it at your GitLab instance, and it automatically awards [GitLab Achievements](https://docs.gitlab.com/user/profile/achievements/) to users based on their activity: commits, merge requests, reviews, issues, pipelines, streaks, and more.

> [!NOTE]
> This project is in the design phase. See the [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current roadmap and to follow along with decisions as they're made.

Inspired by [BoxBoxJason/achievements](https://github.com/boxboxjason/achievements), a VS Code extension that gamifies local coding activity. This project brings the same idea to a team's shared GitLab instance, driven by real server-side events instead of local IDE telemetry.

## How it works

1. **Bootstrap**: on every start, the app verifies its GitLab permissions (read token validity, write token instance-admin + Maintainer/Owner on the achievements namespace), idempotently creates or updates the achievement definitions it needs (via the GitLab Achievements GraphQL API), and registers a [system webhook](https://docs.gitlab.com/administration/system_hooks/) pointing back at itself, at `--public-url`/`PUBLIC_URL` plus its ingestion path. Bootstrap is strictly required: any failure here (bad permissions, a rejected mutation) fails startup rather than serving traffic in a half-working state. `/healthz` and `/readyz` are exposed once the app starts serving.
2. **Ongoing self-healing**: bootstrap's checks don't just run once. The system hook's GitLab ID is cached locally and re-verified roughly every 5 minutes, healing it if it was altered or deleted; achievement existence and award confirmation status are re-checked roughly every hour, recreating any achievement deleted on GitLab's side and retrying any award GitLab hasn't yet confirmed. Both loops log failures and retry on the next tick rather than crashing the process.
3. **Backfill**: it walks the instance's history (users, projects, commits, merge requests, issues, pipelines, etc.) once to award achievements for activity that happened before the bot existed.
4. **Event-driven**: from then on, it reacts to incoming webhook events in near real time, no polling.
5. **Activity reconciliation** *(planned)*: a periodic sync will re-check recent activity to catch any event the webhook pipeline missed (delivery failures, downtime, etc.).

All state (achievement progress, award history, sync cursors, processed-event idempotency) is stored in a local SQL database (PostgreSQL, SQLite, MySQL/MariaDB, or SQL Server) to keep the impact on the GitLab instance itself minimal. The bot reads from GitLab, but GitLab never has to do extra work to serve it beyond normal API/webhook traffic.

## Why two tokens

GitLab's access tokens don't have a "read everything, but also let me create these two specific things" scope. Scopes are either `read_api` (read-only) or `api` (full read/write). So a single token can't be both "read-only across the instance" and "able to create webhooks and achievements."

Instead, this project uses **two credentials**, separated by *role* rather than by scope:

- A **read token** (`read_api`) belonging to an account with read access across the resources being tracked, used for all data fetching (backfill, event enrichment).
- A **write token** (`api` scope) belonging to a service account that is deliberately scoped narrowly by *GitLab role/membership*: Maintainer/Owner only on the specific namespace that owns the achievement definitions, and (if system hooks are used) instance Admin, since system hook management requires it. This token is only ever used for the small set of write calls: creating/awarding achievements and registering the webhook.

This keeps the blast radius of the write-capable credential as small as GitLab's permission model allows, even though the scope itself is broad.

## Webhook strategy

GitLab webhooks come in three tiers:

| Tier | Coverage | Required role | Availability |
| --- | --- | --- | --- |
| Project hooks | One project | Maintainer/Owner on the project | All tiers |
| Group hooks | One group's projects | Owner on the group | **Premium/Ultimate only** |
| System hooks | Entire instance | Instance Admin | All tiers |

Since group-level webhooks require a paid GitLab tier, this project targets **system hooks** by default, one hook, registered once, covers the whole instance and works on GitLab Community Edition / Free. This does mean the write token needs instance Admin rights to register it. See the deployment issue for how this trust requirement is documented and (where possible) minimized.

## Achievement catalog

The initial achievement set adapts the tiered "stacking achievement" pattern from the VS Code extension (e.g. `Committer I → II → III → ...` with exponentially increasing thresholds and EXP rewards) to events GitLab can actually observe server-side:

- **Git activity**: commits, pushes, branches created, merges/rebases
- **Merge requests**: opened, merged, reviewed, approved, time-to-merge
- **Code review**: comments left, discussions resolved
- **Issues**: opened, closed, triaged
- **CI/CD**: pipeline runs, successful/failed pipelines
- **Engagement streaks**: consecutive days active, night-owl / early-bird activity timing

IDE-local categories from the VS Code extension (installed extensions, themes, debugger sessions, tab counts, etc.) don't apply here, since this bot only sees what the GitLab server sees.

> [!NOTE]
> The catalog bootstrap syncs today (`internal/catalog`) is a small placeholder used to exercise the create/update reconciliation logic end to end, not this full set. See the achievement catalog issue.

## Deployment

The app is a single Go binary plus a SQL database. It isn't tied to any one deployment platform or DBMS: the database is selected via the `--database-dsn`/`DATABASE_DSN` scheme (`postgres://`, `sqlite://`, `mysql://`, or `sqlserver://`). Supported/planned deployment targets (see the deployment issue for details):

- Kubernetes (Helm chart)
- Docker / Docker Compose
- Bare binary + systemd unit, for a plain VM/server install

## Development

Check the [CONTRIBUTING.md](./CONTRIBUTING.md) file for guidelines on how to contribute to this project.

Quick start:

```bash
make build   # build the binary into ./bin
make test    # run unit tests
make lint    # run golangci-lint
make package # build the container image (via podman/docker)
```

## Status

Early design stage: scaffolding, the GitLab client, and self-bootstrap (permission verification, achievement/webhook reconciliation, health/readiness endpoints) are implemented; backfill and webhook event ingestion are not yet. See [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current breakdown of work and open questions.

## License

[MIT](./LICENSE)
