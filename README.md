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
5. **Activity reconciliation**: webhooks are best-effort, so once a day the app re-reads the last couple of days of activity through the Events API and replays it through the same engine, picking up anything whose delivery was lost to a blip, a deploy, or GitLab downtime. Activity that was already counted is discarded rather than counted again. See [Reconciliation sync](#reconciliation-sync) below.
6. **Reading it back**: everything it works out is served over a read-only HTTP API — a user's EXP, the progress behind it, and a leaderboard. See [HTTP API](#http-api) below.

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

### Removing them

Retiring the app leaves its hooks behind, pointing at a URL that no longer answers. `gitlab-achievements uninstall` takes them off the instance:

```bash
gitlab-achievements uninstall              # same flags/env as the server
gitlab-achievements uninstall --dry-run    # report what would be removed, call nothing
gitlab-achievements uninstall --sweep      # also hunt down hooks the database no longer knows about
```

**Stop the serving deployment first.** A running server re-registers everything on its next hourly sweep, so removing hooks underneath it accomplishes nothing. That is also why this is a subcommand rather than something the server does on `SIGTERM`: a hook torn down on shutdown would be rebuilt on the next start, so every rollout and pod eviction would churn every hook on the instance and lose the events arriving in between.

Removal works off the same recorded hook IDs the sweep re-applies its configuration with, so it costs one `DELETE` per hook rather than a walk of the instance, and each record is dropped as its hook goes: an interrupted run leaves behind exactly what it hadn't reached yet, and re-running finishes the job. A hook GitLab no longer has counts as removed; one on a group or project the write token may not manage is left in place, reported, and keeps its record, so re-running with a better-privileged token picks up precisely those.

`--sweep` covers the case records can't: a database that was lost, or restored from a backup predating the last registration sweep. It enumerates the instance and removes any hook whose URL is this app's, leaving every other integration's hooks alone. Under `auto` on a Premium/Ultimate instance it sweeps projects as well as groups, since the deployment may have been running the project scope before the license changed.

Achievement definitions and the awards users earned are deliberately untouched: those are what people got out of running this, and deleting them is a separate decision, made in GitLab's own UI.

### Event coverage

Hooks subscribe to **every event type GitLab offers**, not just the ones the catalog reads today. Enabling one later would mean editing every hook on the instance, and silently missing that activity until the sweep ran; leaving them all on costs nothing but deliveries the receiver ignores by design. That includes the confidential issue and note variants, work on a confidential issue is still work, and the app keeps only the record's identity and author, never its content.

## Historical backfill

Before the bot goes live, activity has already happened. The backfill walks every group-owned project the read token can see, in ascending project ID order, and replays each one's history through the same achievement engine live webhook events feed, so historical and live activity are judged by identical rules rather than by two implementations that drift apart.

The walk stops at the moment the process started, just before the hooks were registered, so it doesn't spend requests re-reading a window live ingestion is already covering. That ceiling is a request-budget boundary rather than a correctness one: both paths derive the *same* deduplication key for the same activity (see [Deduplication](#deduplication)), so anything either has already counted is discarded whichever one re-observes it. A one-off `gitlab-achievements backfill` run imposes no ceiling at all, since it isn't ingesting events itself.

**There is a gap, and it is the registration sweep's duration.** Hooks start delivering as the sweep reaches them, not all at once when the process starts, so activity between startup and a given hook's registration is seen by neither path at the time. The [reconciliation sync](#reconciliation-sync) closes it on its first pass. The process logs the window's width as `uncovered_window` once bootstrap finishes; on a paid instance it's seconds, and on a Free instance with thousands of projects it's however long a full project sweep takes.

Per project it pulls:

- the **Events API** (`GET /projects/:id/events`) as the primary source: pushes and their commit counts, branch/tag creation, merge requests opened/merged/approved/closed, issues opened/closed, and comments. The [Audit Events API](https://docs.gitlab.com/api/audit_events/) would be richer, but it's Premium/Ultimate-only, so it can't be relied on for a Free/CE-compatible backfill.
- the **Pipelines API**, since pipelines don't appear in the Events API at all.

Between them these feed most of the catalog, including the engagement criteria: each dated activity also records the day it happened on, which is what streaks and the night-owl/early-bird criteria are derived from. What they don't reach is the six criteria the Events API has no equivalent for at all (deployments, jobs, emoji, wiki pages, discussion resolutions) — those only ever advance from live deliveries.

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

## Reconciliation sync

Webhooks are best-effort. GitLab retries a delivery a few times and then gives up, so a network blip, a deploy, or an instance restart is enough to lose one for good — and nothing in the live path notices, because a delivery that never arrives leaves no trace of having been missed.

The reconciliation sync is the safety net. Once a day (`--reconcile-interval`, default `24h`) it asks GitLab what actually happened over the last two days (`--reconcile-lookback`, default `48h`) and replays it through the achievement engine. It reads the same two sources the backfill does — the Events API, plus the Pipelines API — but only over projects GitLab reports as active in the window, so a quiet instance costs almost nothing however many projects it holds.

### Deduplication

Almost everything a pass reads is activity the live path already counted, so **the sync is only safe because the read side and the live side derive byte-identical deduplication keys**. They are reconstructed from the identifiers GitLab reports on both sides rather than from the event record's own ID:

| Activity | Key | From, on the read side |
| --- | --- | --- |
| Push, tag push | `push:<project>:<ref>:<after>` | `push_data.ref` + `push_data.commit_to` |
| Merge request | `merge_request:<id>:<action>` | `target_id` |
| Issue | `issue:<id>:<action>` | `target_id` |
| Comment | `note:<id>` | `note.id` |
| Pipeline | `pipeline:<id>` | the pipeline's ID |

The engine keeps a processed-event log keyed on exactly that, so a pass over a window the webhooks covered correctly is a no-op. This matters more than it looks: GitLab never revokes an award, so a counter inflated by counting the same push twice can't be brought back down. A cross-producer test suite (`internal/webhook/dedup_agreement_test.go`) asserts the agreement for every kind, in both directions.

### What it can't heal

The Events API has no representation for jobs, deployments, emoji reactions, wiki pages, resolved discussions, or fast merges, so a lost delivery of one of those stays lost. That's the deliberate direction to be wrong in: the sync undercounts what it can't see rather than guessing at it under a key that wouldn't match.

### Running it

`--reconcile=auto` runs it inside the serving process: one pass a few minutes after startup, then every interval. The startup pass matters more than it looks — the timer's phase is the process's start time, so without it a deployment restarted more often than the interval would never reconcile at all, and nothing would say so. Repeating it is cheap, because the watermark means a pass after a restart covers only the gap since the last successful one. Passes are skipped until the historical backfill has completed.

`--reconcile=off` leaves it to an external schedule — a systemd timer, a cron entry, a Kubernetes `CronJob`:

```bash
gitlab-achievements reconcile   # same flags/env as the server
```

Unlike the server and `backfill`, the subcommand doesn't bootstrap: it registers no webhooks and creates no achievements, so a scheduled pass costs one sweep of recently active projects rather than a sweep of the whole instance. Reach for it when you want a pass to be a unit you can start, watch and alert on, or pinned to a wall-clock hour rather than to whenever the process last restarted.

A pass records a watermark only on success, and the next pass widens its window to reach back to it, so a run that failed or never happened is made up rather than leaving a hole. Watch `activity_counted` in the completion log: in a healthy deployment it stays at zero pass after pass, and a persistently non-zero value means deliveries are being lost for a reason worth finding.

## Achievement catalog

The catalog ports the tiered "stacking achievement" pattern from the VS Code extension: one criteria, one difficulty curve, and a run of tiers generated off it (`Committer I → II → III → …`). It is generated from templates in `internal/catalog`, not written out by hand, so a new criteria is one template.

| Category | Criteria |
| --- | --- |
| **Git activity** | commits, pushes, branches created, tags created |
| **Merge requests & review** | opened, merged, merged within an hour of opening, approved, closed without merging, comments left, review discussions resolved |
| **Issues** | opened, closed |
| **CI/CD** | pipelines run, passed, failed, jobs run, deployments, deployments succeeded |
| **Collaboration** | emoji reactions given, wiki pages created |
| **Engagement** | days active, longest activity streak, night-owl days, early-bird days |

25 criteria at 11 tiers each is **275 GitLab achievements**, created idempotently on the first bootstrap and reconciled cheaply thereafter. The thresholds follow the extension's four curves, built from powers of 2, 5 and 10 so tiers land on round numbers (`Committer` runs 1, 5, 10, 50, … 100,000). The top tiers are deliberately out of reach on most instances.

The catalog is a fixed, compiled-in list rather than something an operator retunes per deployment. Thresholds are only half the contract: the other half is 275 achievement objects that already exist on the instance, with awards already pointing at them, so a configurable catalog is really a question about renaming and deleting live GitLab records — worth its own issue, not v1's.

Six of the criteria advance from **live webhook deliveries only**. The Events API the backfill reads reports pushes, merge requests, issues and notes, but not deployments, jobs, emoji, wiki pages, or which discussions were resolved, so those start from zero on a freshly bootstrapped instance however long it has existed.

Two things the extension has don't survive the move intact. **EXP has nowhere to live on GitLab**: an achievement there is a flat object with a name, description and avatar. No tier field, no points. So every tier's EXP reward, and each user's running total, are kept in this app's database and nowhere else (see below). And **anything that needs to watch an editor** (lines of code, files created, per-language breakdowns, tabs, extensions, themes, debugger sessions, terminal tasks, time spent) has no server-side equivalent at all. Two git criteria are missing for subtler reasons: an amend is indistinguishable from an ordinary commit once pushed, and the Events API doesn't report whether a push was forced.

Four event types the hooks subscribe to earn no achievements either, for want of anyone to credit: release and vulnerability payloads carry no user at all, member payloads name the member rather than whoever added them, and feature flag payloads carry a user but no identifier for the change, so a second toggle of one flag can't be told apart from a redelivery of the first. Milestone events have no payload type in the API client to parse. The hooks stay subscribed to all of them, so adding a criteria later is a code change rather than a re-registration across every project.

The engagement criteria are derived from a per-user record of which days someone was active, rather than from a running total: two commits in one afternoon are one active day, and a streak can be extended by a day arriving between two known ones. That also makes them independent of the order activity is observed in, which matters because the backfill walks project by project rather than in date order. The streak awarded is the **longest** run a user ever managed, not their current one. GitLab awards aren't revoked, so a criteria that can fall as well as rise would mean nothing after the first time it was reached.

Achievement icons are borrowed from the VS Code extension's own icon set where a criteria matches; the rest ship without an avatar for now.

### EXP

Every tier is worth EXP, on one curve shared by the whole catalog, so the same tier pays the same whatever criteria it was earned in and the easiest curve to climb isn't also the most rewarding to farm. A user's total is written to their row in the same transaction that records the award, so a crash can't leave someone holding a tier they weren't paid for.

The total is **derived, not accumulated**: it is always recomputed as the sum of what the tiers a user holds are worth. That is what makes a backfill awarding tiers in arbitrary order, a catalog retune changing what an old tier pays, and withdrawing a superseded tier from GitLab all safe — none of them can drift the number. When a bootstrap or reconciliation pass finds a tier's reward changed, it re-derives the totals of everyone holding it on the spot.

### What reaches GitLab

Only the **top tier a user has reached in each criteria** is ever pushed to GitLab. Every tier below it stays recorded here and keeps paying its EXP; when a user is promoted, the new tier is awarded and the one it replaces is revoked, so a profile carries at most one badge per criteria rather than a run of eleven near-identical ones.

That split exists because GitLab draws the line somewhere unexpected: an awarded achievement is invisible until its recipient accepts it, **only the recipient can accept it**, and every award emails them. This app can award and revoke; it cannot decide what anyone's profile shows, and it cannot batch the notifications. Pushing every reached tier would mean emailing a long-serving user roughly a hundred times on the first backfill to no visible end. Awarding is also not idempotent on GitLab's side, so delivery matches against what GitLab already holds for a user rather than retrying blind.

The full findings, and how to re-verify them on a throwaway instance, are in [docs/achievements-api-behavior.md](docs/achievements-api-behavior.md).

## HTTP API

GitLab shows which achievements someone holds but has no notion of EXP, so this app is the only place that number exists. It is served read-only under `/api/v1/`, from the local database alone — no GitLab call sits on the data path, so the API keeps answering while the instance it mirrors is down or rate-limiting.

| Method | Path | Returns |
| --- | --- | --- |
| `GET` | `/api/v1/users/{ref}` | EXP total, criteria counters, and every tier earned |
| `GET` | `/api/v1/users/{ref}/exp` | The EXP total alone |
| `GET` | `/api/v1/leaderboard?limit=N` | Top N users by EXP (default 10, max 100) |

`{ref}` is either a numeric GitLab user ID or a username. An all-digit ref is tried as an ID first and falls back to a username, so an all-numeric username still resolves unless it collides with a real user ID. Usernames resolve through whatever this app last saw, so somebody who was renamed on GitLab is still found under their current name.

A `404` means this app has recorded no activity for that user at all. A user it knows who has simply earned nothing is a `200` with `"exp_total": 0` — the two are different answers and are reported differently.

```console
$ curl -s https://achievements.example.com/api/v1/users/alice/exp
{"username":"alice","gitlab_user_id":42,"exp_total":1350}
```

Awards are reported whatever their delivery status, matching how EXP is totalled: a tier is earned the moment the engine says so, and a `superseded` tier still pays even though it is deliberately not what GitLab displays. `status` and `shown_on_profile` are both exposed, because they answer different questions — how far this app got pushing the award, and whether the recipient accepted it onto their profile.

### Authentication

Off by default (`--api-auth=none`), matching the posture `/healthz` already has, so upgrading doesn't lock anything and a deployment on a private network can opt out deliberately.

Setting `--api-auth=gitlab` makes the mirrored instance the identity provider. A caller presents any GitLab token — a personal access token, or an OAuth access token — as `Authorization: Bearer <token>`, and it is checked against `GET /api/v4/user` before anything is served:

```console
$ curl -s -H "Authorization: Bearer $GITLAB_TOKEN" \
    https://achievements.example.com/api/v1/leaderboard
```

Browsers can instead log in at `/oauth/login`, which runs the standard authorization-code flow (with PKCE) against the instance and leaves an `HttpOnly` session cookie; `POST /oauth/logout` ends it. Unless `--oauth-client-id` names an application you registered by hand, the app registers a **public** OAuth application for itself on startup — using the instance-admin write token it already holds — and adopts that same application on every later start rather than creating another. A public client has no secret to store; PKCE is what secures the exchange. Pass `--oauth-client-secret` alongside a client ID to run as a confidential client instead.

Any authenticated GitLab identity may read anything the API serves. Achievements are already public on GitLab profiles and are social by nature; what authentication closes is an anonymous caller enumerating who exists on the instance.

> [!NOTE]
> With `--api-auth=gitlab`, credentials are verified against GitLab on **every** request, with no cache. Revoking a token therefore takes effect immediately, but it also means authenticated requests cannot be served while the instance is unreachable. The DB-only guarantee above applies to the data; under the default `--api-auth=none` it applies to the whole request.

## Deployment

The app is a single static Go binary plus a SQL database, configured entirely from environment variables, so it isn't tied to any one platform or DBMS: the database is selected via the `--database-dsn`/`DATABASE_DSN` scheme (`postgres://`, `sqlite://`, `mysql://`, or `sqlserver://`). Three targets are supported as equals, and none of them needs a code change:

| Target | What you get | Guide |
| --- | --- | --- |
| **Kubernetes** | Helm chart: Deployment, Service, optional Ingress, probes wired to `/healthz` and `/readyz`, credentials from a Secret you manage, optional backfill Job | [docs/deployment/kubernetes.md](docs/deployment/kubernetes.md) |
| **Docker Compose** | The app plus a PostgreSQL on one host, from [`docker-compose.yml`](./docker-compose.yml) and a `.env` | [docs/deployment/docker-compose.md](docs/deployment/docker-compose.md) |
| **Bare binary** | The compiled binary under a [systemd unit](./deploy/systemd), against a database you already run | [docs/deployment/bare-binary.md](docs/deployment/bare-binary.md) |

Whichever you pick, the GitLab side is the same: a group to own the achievement definitions, the two credentials, a webhook secret, and a URL the instance can reach. That's [docs/gitlab-setup.md](docs/gitlab-setup.md), including what trusting the instance-admin token to a deployment actually implies. Every flag and environment variable is in [docs/configuration.md](docs/configuration.md).

The app is a **singleton**: one process registers the instance's webhooks, runs the hourly reconciliation sweeps, walks history, and re-reads recent activity daily, so a second replica would do all four again against the same GitLab. Nothing is lost while it restarts.

## Development

Check the [CONTRIBUTING.md](./CONTRIBUTING.md) file for guidelines on how to contribute to this project.

Quick start:

```bash
make build      # build the binary into ./bin
make test       # run unit tests
make lint       # run golangci-lint
make package    # build the container image (via podman/docker)
make chart-lint # lint and render the Helm chart
```

Longer-form documentation lives in [docs/](./docs), indexed in [docs/README.md](docs/README.md).

## Status

Early development: scaffolding, the GitLab client, self-bootstrap (permission verification, achievement/webhook reconciliation, health/readiness endpoints), the achievement catalog, the historical backfill, and live webhook event ingestion are implemented, so the app now awards from history and keeps awarding from live activity. The rule engine handles cumulative and day-derived criteria and maintains each user's EXP total, and award delivery pushes only each criteria's top tier, withdrawing the ones it supersedes. That total, the progress behind it, and an instance leaderboard are now served over HTTP, optionally behind GitLab-backed authentication, and the app ships packaged for Kubernetes, Docker Compose and a plain systemd host. The daily reconciliation sync that recovers activity whose webhook delivery was lost is implemented too, in the process or as a scheduled job, on top of read and live paths that now derive identical deduplication keys. See [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current breakdown of work and open questions.

## License

[MIT](./LICENSE)
