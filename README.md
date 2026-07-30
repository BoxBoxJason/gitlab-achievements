# GitLab Achievements

An event-driven **achievement awarder bot** for self-hosted GitLab instances. Point it at your GitLab instance, and it automatically awards [GitLab Achievements](https://docs.gitlab.com/user/profile/achievements/) to users based on their activity: commits, merge requests, reviews, issues, pipelines, streaks, and more.

> [!NOTE]
> This project is in the design phase. See the [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current roadmap and to follow along with decisions as they're made.

Inspired by [BoxBoxJason/achievements](https://github.com/boxboxjason/achievements), a VS Code extension that gamifies local coding activity. This project brings the same idea to a team's shared GitLab instance, driven by real server-side events instead of local IDE telemetry.

## How it works

1. **Bootstrap**: on every start, the app verifies its GitLab permissions (read token validity, write token instance-admin + Maintainer/Owner on the achievements namespace), idempotently creates or updates the achievement definitions it needs (via the GitLab Achievements GraphQL API), and registers the webhooks that feed it, pointing back at `--public-url`/`PUBLIC_URL` plus its ingestion path. Bootstrap is strictly required: any failure here (bad permissions, a rejected mutation) fails startup rather than serving traffic in a half-working state. `/healthz` and `/readyz` are exposed once the app starts serving.
2. **Ongoing self-healing**: bootstrap's checks don't just run once. Hook registration is swept roughly every hour: a hook altered or deleted on GitLab's side is repaired, and groups or projects created since the last sweep get one. Achievement existence and award confirmation status are re-checked on the same cadence, recreating any achievement deleted on GitLab's side and retrying any award GitLab hasn't yet confirmed. Both loops log failures and retry on the next tick rather than crashing the process.
3. **Backfill**: it walks the instance's history once to award achievements for activity that happened before the bot existed. See [Historical backfill](#historical-backfill) below.
4. **Event-driven**: from then on, it reacts to incoming webhook events in near real time, no polling. Deliveries are authenticated against the configured secret, normalized into the same activity model the backfill produces, acknowledged immediately, and evaluated by background workers, so a slow database never makes GitLab record the hook as failing.
5. **Activity reconciliation** *(planned)*: a periodic sync will re-check recent activity to catch any event the webhook pipeline missed (delivery failures, downtime, etc.).

All state (achievement progress, award history, sync cursors, processed-event idempotency) is stored in a local SQL database (PostgreSQL, SQLite, MySQL/MariaDB, or SQL Server) to keep the impact on the GitLab instance itself minimal. The bot reads from GitLab, but GitLab never has to do extra work to serve it beyond normal API/webhook traffic.

## Why two tokens

GitLab's access tokens don't have a "read everything, but also let me create these two specific things" scope. Scopes are either `read_api` (read-only) or `api` (full read/write). So a single token can't be both "read-only across the instance" and "able to create webhooks and achievements."

Instead, this project uses **two credentials**, separated by *role* rather than by scope:

- A **read token** (`read_api`) belonging to an account with read access across the resources being tracked, used for all data fetching (backfill, event enrichment).
- A **write token** (`api` scope) belonging to a service account that is deliberately scoped narrowly by *GitLab role/membership*: Maintainer/Owner on the specific namespace that owns the achievement definitions, plus instance Admin, which is what lets it enumerate every group and project and manage hooks across them. This token is only ever used for the small set of write calls: creating/awarding achievements and registering the webhooks.

This keeps the blast radius of the write-capable credential as small as GitLab's permission model allows, even though the scope itself is broad.

## Webhook strategy

GitLab webhooks come in three tiers:

| Tier | Coverage | Required role | Availability |
| --- | --- | --- | --- |
| Project hooks | One project | Maintainer/Owner on the project | All tiers |
| Group hooks | One group's projects, subgroups included | Owner on the group | **Premium/Ultimate only** |
| System hooks | Entire instance | Instance Admin | All tiers |

System hooks look like the obvious choice: one hook covers everything, on every tier; but they deliver a much narrower event set than the other two: **no merge request approvals, no comments, and no pipeline events at all**. That leaves most of the achievement catalog unreachable from live activity, so this project uses group and project hooks instead, which both carry the full event set.

Which of the two is used depends on what the instance's license allows, resolved at bootstrap from `GET /api/v4/license`:

- **Premium/Ultimate** → one hook per **top-level group**. A group hook covers that group's whole subtree, so projects created inside it later are covered with no further registration.
- **Free/CE, or no license** → one hook per **project**, across the whole instance.

`--hook-scope`/`HOOK_SCOPE` (`auto`, `group`, `project`) overrides the detection when the write token can't read the license or the choice needs pinning. Either way the write token needs instance Admin, to enumerate every group and project on the instance and to manage hooks on ones it isn't otherwise a member of.

Two consequences worth knowing before deploying:

- **Projects in personal namespaces earn nothing.** A group hook cannot reach `someuser/project`, since a personal namespace is not a group. Rather than let a user's progress depend on their instance's license, the project-hook path skips them too, and so does the historical backfill. Only group-owned projects count, on every tier.
- **New projects are picked up within the hour.** `project_create` is delivered only to system hooks, so nothing tells the app a project appeared; one created after bootstrap is hooked by the next reconciliation sweep rather than immediately. On paid instances this only affects newly created *top-level groups*, since group hooks already cover new projects underneath them.

The sweep costs one API call per target in the steady state, the edit that re-applies the hook's configuration, issued straight off the hook ID recorded when it was registered. (A hook deleted out of band costs two more: the edit returns 404, and the target's hooks are listed to adopt any already pointing here before a new one is registered.) It's paced at `--hook-rate`/`HOOK_RATE` targets per second, 20 by default, and runs hourly rather than every few minutes: on a Free instance with thousands of projects, a tighter cadence would be a permanent background load on someone's production GitLab.

The edit is unconditional, and deliberately so. GitLab never returns a hook's token, so there's no reading the remote state and concluding it already matches; a rotated `--webhook-secret` would be invisible and the hooks would keep presenting the old one. Re-applying every sweep is what makes a rotated secret and a hand-edited event set heal on the same pass.

Deliveries are authenticated with the `X-Gitlab-Token` secret (`--webhook-secret`), compared in constant time; anything that doesn't match is refused with a 401 and never parsed.

Hooks subscribe to **every event type GitLab offers**, not just the ones the catalog reads today. Enabling one later would mean editing every hook on the instance, and silently missing that activity until the sweep ran; leaving them all on costs nothing but deliveries the receiver ignores by design. That includes the confidential issue and note variants, work on a confidential issue is still work, and the app keeps only the record's identity and author, never its content.

## Historical backfill

Before the bot goes live, activity has already happened. The backfill walks every group-owned project the read token can see, in ascending project ID order, and replays each one's history through the same achievement engine live webhook events feed, so historical and live activity are judged by identical rules rather than by two implementations that drift apart.

The walk stops at the moment the process started, just before the hooks were registered. The two paths observe the same activity through different APIs and can only derive a matching deduplication key where GitLab gives them a shared identifier, it does for pipelines, and not for pushes, merge requests, issues, or notes. Giving them disjoint windows is what keeps a push that lands mid-walk from being counted twice. (A one-off `gitlab-achievements backfill` run imposes no such ceiling, since it isn't ingesting events itself.)

**There is a gap, and it is the registration sweep's duration.** Hooks start delivering as the sweep reaches them, not all at once when the process starts, so activity between startup and a given hook's registration is counted by neither path. One ceiling can't avoid that: set any later and the same window would be counted by both instead. The gap is the better failure, because it's recoverable, that's what the planned activity reconciliation is for, whereas an inflated counter isn't, since GitLab awards are never revoked. The process logs the window's width as `uncovered_window` once bootstrap finishes; on a paid instance it's seconds, and on a Free instance with thousands of projects it's however long a full project sweep takes.

Per project it pulls:

- the **Events API** (`GET /projects/:id/events`) as the primary source: pushes and their commit counts, branch/tag creation, merge requests opened/merged/approved/closed, issues opened/closed, and comments. The [Audit Events API](https://docs.gitlab.com/api/audit_events/) would be richer, but it's Premium/Ultimate-only, so it can't be relied on for a Free/CE-compatible backfill.
- the **Pipelines API**, since pipelines don't appear in the Events API at all.

Between them these feed every criteria in the catalog, including the engagement ones: each dated activity also records the day it happened on, which is what streaks and the night-owl/early-bird criteria are derived from.

Three things shape the implementation more than speed does:

- **Resumability.** Progress is persisted as it goes (last completed project, in-flight phase, event date cursor, last processed pipeline), so an interrupted walk resumes near where it stopped. Re-walked activity is deduplicated by the engine, so a coarse cursor costs a few repeated reads, never a double-counted commit. A completion watermark records when history was walked end to end, which is what tells the app the cold start is over.
- **Restraint.** This is the heaviest read workload the app ever runs against an instance it doesn't own. Requests are paced with a configurable cap (`--backfill-rate`, default 5/s) on a client of its own, so a readiness probe never queues behind the walk; 429 and 5xx responses are retried with GitLab's own `Retry-After` on top of that. A project the read token can't see is skipped, not fatal. Projects in personal namespaces are skipped too, matching what the webhooks cover (see [Webhook strategy](#webhook-strategy)).
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

Two things the extension has don't survive the move intact. **EXP has nowhere to live on GitLab**: an achievement there is a flat object with a name, description and avatar. No tier field, no points. So every tier's EXP reward, and each user's running total, are kept in this app's database and nowhere else (see below). And **anything that needs to watch an editor** (lines of code, files created, per-language breakdowns, tabs, extensions, themes, debugger sessions, terminal tasks, time spent) has no server-side equivalent at all. Two git criteria are missing for subtler reasons: an amend is indistinguishable from an ordinary commit once pushed, and the Events API doesn't report whether a push was forced.

The engagement criteria are derived from a per-user record of which days someone was active, rather than from a running total: two commits in one afternoon are one active day, and a streak can be extended by a day arriving between two known ones. That also makes them independent of the order activity is observed in, which matters because the backfill walks project by project rather than in date order. The streak awarded is the **longest** run a user ever managed, not their current one. GitLab awards aren't revoked, so a criteria that can fall as well as rise would mean nothing after the first time it was reached.

Achievement icons are borrowed from the VS Code extension's own icon set where a criteria matches; the rest ship without an avatar for now.

### EXP

Every tier is worth EXP, on one curve shared by the whole catalog, so the same tier pays the same whatever criteria it was earned in and the easiest curve to climb isn't also the most rewarding to farm. A user's total is written to their row in the same transaction that records the award, so a crash can't leave someone holding a tier they weren't paid for.

The total is **derived, not accumulated**: it is always recomputed as the sum of what the tiers a user holds are worth. That is what makes a backfill awarding tiers in arbitrary order, a catalog retune changing what an old tier pays, and hiding superseded tiers from a profile all safe — none of them can drift the number. When a bootstrap or reconciliation pass finds a tier's reward changed, it re-derives the totals of everyone holding it on the spot.

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

Early development: scaffolding, the GitLab client, self-bootstrap (permission verification, achievement/webhook reconciliation, health/readiness endpoints), the achievement catalog, the historical backfill, and live webhook event ingestion are implemented, so the app now awards from history and keeps awarding from live activity. The rule engine handles cumulative and day-derived criteria and maintains each user's EXP total. Serving that total over HTTP, hiding superseded tiers on the profile, and the activity reconciliation job that would recover events lost to downtime are still open. See [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current breakdown of work and open questions.

## License

[MIT](./LICENSE)
