# GitLab Achievements

An event-driven **achievement awarder bot** for self-hosted GitLab instances. Point it at your GitLab instance, and it automatically awards [GitLab Achievements](https://docs.gitlab.com/user/profile/achievements/) to users based on their activity: commits, merge requests, reviews, issues, pipelines, streaks, and more.

> [!NOTE]
> This project is in the design phase. See the [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current roadmap and to follow along with decisions as they're made.

Inspired by [BoxBoxJason/achievements](https://github.com/boxboxjason/achievements), a VS Code extension that gamifies local coding activity. This project brings the same idea to a team's shared GitLab instance, driven by real server-side events instead of local IDE telemetry.

## How it works

1. **Bootstrap**: on every start, the app verifies its GitLab permissions (read token validity, write token instance-admin + Maintainer/Owner on the achievements namespace), idempotently creates or updates the achievement definitions it needs (via the GitLab Achievements GraphQL API), and registers a [system webhook](https://docs.gitlab.com/administration/system_hooks/) pointing back at itself, at `--public-url`/`PUBLIC_URL` plus its ingestion path. Bootstrap is strictly required: any failure here (bad permissions, a rejected mutation) fails startup rather than serving traffic in a half-working state. `/healthz` and `/readyz` are exposed once the app starts serving.
2. **Ongoing self-healing**: bootstrap's checks don't just run once. The system hook's GitLab ID is cached locally and re-verified roughly every 5 minutes, healing it if it was altered or deleted; achievement existence and award confirmation status are re-checked roughly every hour, recreating any achievement deleted on GitLab's side and retrying any award GitLab hasn't yet confirmed. Both loops log failures and retry on the next tick rather than crashing the process.
3. **Backfill**: it walks the instance's history once to award achievements for activity that happened before the bot existed. See [Historical backfill](#historical-backfill) below.
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

## Historical backfill

Before the bot goes live, activity has already happened. The backfill walks every project the read token can see, in ascending project ID order, and replays each one's history through the same achievement engine live webhook events will feed, so historical and live activity are judged by identical rules rather than by two implementations that drift apart.

Per project it pulls:

- the **Events API** (`GET /projects/:id/events`) as the primary source: pushes and their commit counts, branch/tag creation, merge requests opened/merged/approved/closed, issues opened/closed, and comments. The [Audit Events API](https://docs.gitlab.com/api/audit_events/) would be richer, but it's Premium/Ultimate-only, so it can't be relied on for a Free/CE-compatible backfill.
- the **Pipelines API**, since pipelines don't appear in the Events API at all.

Between them these feed every criteria in the catalog, including the engagement ones: each dated activity also records the day it happened on, which is what streaks and the night-owl/early-bird criteria are derived from.

Three things shape the implementation more than speed does:

- **Resumability.** Progress is persisted as it goes (last completed project, in-flight phase, event date cursor, last processed pipeline), so an interrupted walk resumes near where it stopped. Re-walked activity is deduplicated by the engine, so a coarse cursor costs a few repeated reads, never a double-counted commit. A completion watermark records when history was walked end to end, which is what tells the app the cold start is over.
- **Restraint.** This is the heaviest read workload the app ever runs against an instance it doesn't own. Requests are paced with a configurable cap (`--backfill-rate`, default 5/s) on a client of its own, so a readiness probe never queues behind the walk; 429 and 5xx responses are retried with GitLab's own `Retry-After` on top of that. A project the read token can't see is skipped, not fatal.
- **Bounded scope.** `--backfill-since` caps how far back the walk reaches, as a date (`2024-01-01`) or a duration (`720h`). It's passed to GitLab as a server-side filter, so a narrower window means proportionally fewer requests rather than fetching everything and discarding the excess. Unset walks the full history.

### Running it

By default (`--backfill=auto`) the serving process runs the walk once in the background after bootstrap: it never blocks startup, a restart resumes an interrupted walk, and a finished one is never repeated. On instances big enough that the cold start deserves to be its own job, set `--backfill=off` on the deployment and run it explicitly instead:

```bash
gitlab-achievements backfill    # same flags/env as the server; add --backfill=force to walk a finished instance again
```

Awards the walk records are pushed to GitLab as soon as it finishes, rather than waiting for the hourly award reconciliation to notice them.

## Achievement catalog

The catalog ports the tiered "stacking achievement" pattern from the VS Code extension: one criteria, one difficulty curve, and a run of tiers generated off it (`Committer I → II → III → …`). It is generated from templates in `internal/catalog`, not written out by hand, so a new criteria is one template.

| Category | Criteria |
| --- | --- |
| **Git activity** | commits, pushes, branches created, tags created |
| **Merge requests & review** | opened, merged, approved, closed without merging, comments left |
| **Issues** | opened, closed |
| **CI/CD** | pipelines run, passed, failed |
| **Engagement** | days active, longest activity streak, night-owl days, early-bird days |

18 criteria at 11 tiers each is **198 GitLab achievements**, created idempotently on the first bootstrap and reconciled cheaply thereafter. The thresholds follow the extension's four curves, built from powers of 2, 5 and 10 so tiers land on round numbers (`Committer` runs 1, 5, 10, 50, … 100,000). The top tiers are deliberately out of reach on most instances.

Two things the extension has don't survive the move. **EXP has nowhere to live**: a GitLab achievement is a flat object with a name, description and avatar — no tier field, no points — so the progression exists only in this app's database and in the achievement names. And **anything that needs to watch an editor** (lines of code, files created, per-language breakdowns, tabs, extensions, themes, debugger sessions, terminal tasks, time spent) has no server-side equivalent at all. Two git criteria are missing for subtler reasons: an amend is indistinguishable from an ordinary commit once pushed, and the Events API doesn't report whether a push was forced.

The engagement criteria are derived from a per-user record of which days someone was active, rather than from a running total: two commits in one afternoon are one active day, and a streak can be extended by a day arriving between two known ones. That also makes them independent of the order activity is observed in, which matters because the backfill walks project by project rather than in date order. The streak awarded is the **longest** run a user ever managed, not their current one — GitLab awards aren't revoked, so a criteria that can fall as well as rise would mean nothing after the first time it was reached.

Achievement icons are borrowed from the VS Code extension's own icon set where a criteria matches; the rest ship without an avatar for now.

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

Early development: scaffolding, the GitLab client, self-bootstrap (permission verification, achievement/webhook reconciliation, health/readiness endpoints), the achievement catalog, and the historical backfill are implemented. Webhook event ingestion is not, so the app currently awards from history and then goes quiet until it lands. The rule engine handles cumulative and day-derived criteria; configurable threshold curves and EXP-style rewards are still open questions. See [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current breakdown of work and open questions.

## License

[MIT](./LICENSE)
