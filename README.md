# GitLab Achievements

An event-driven **achievement awarder bot** for self-hosted GitLab instances. Point it at your GitLab instance, and it automatically awards [GitLab Achievements](https://docs.gitlab.com/user/profile/achievements/) to users based on their activity: commits, merge requests, reviews, issues, pipelines, streaks, and more.

> [!NOTE]
> This project is in the design phase. See the [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current roadmap and to follow along with decisions as they're made.

Inspired by [BoxBoxJason/achievements](https://github.com/boxboxjason/achievements), a VS Code extension that gamifies local coding activity. This project brings the same idea to a team's shared GitLab instance, driven by real server-side events instead of local IDE telemetry.

## How it works

1. **Bootstrap**: on first start, the app verifies its GitLab permissions, creates the achievement definitions it needs (via the GitLab Achievements GraphQL API), and registers a [system webhook](https://docs.gitlab.com/administration/system_hooks/) pointing back at itself.
2. **Backfill**: it walks the instance's history (users, projects, commits, merge requests, issues, pipelines, etc.) once to award achievements for activity that happened before the bot existed.
3. **Event-driven**: from then on, it reacts to incoming webhook events in near real time, no polling.
4. **Reconciliation** *(planned)*: a periodic sync will re-check recent activity to catch any event the webhook pipeline missed (delivery failures, downtime, etc.).

All state (achievement progress, award history, sync cursors, processed-event idempotency) is stored in PostgreSQL to keep the impact on the GitLab instance itself minimal. The bot reads from GitLab, but GitLab never has to do extra work to serve it beyond normal API/webhook traffic.

## Why two tokens

GitLab's access tokens don't have a "read everything, but also let me create these two specific things" scope. Scopes are either `read_api` (read-only) or `api` (full read/write). So a single token can't be both "read-only across the instance" and "able to create webhooks and achievements."

Instead, this project uses **two credentials**, separated by *role* rather than by scope:

- A **read token** (`read_api`) belonging to an account with read access across the resources being tracked, used for all data fetching (backfill, event enrichment).
- A **write token** (`api` scope) belonging to a service account that is deliberately scoped narrowly by *GitLab role/membership*: Maintainer/Owner only on the specific namespace that owns the achievement definitions, and (if system hooks are used) instance Admin, since system hook management requires it. This token is only ever used for the small set of write calls: creating/awarding achievements and registering the webhook.

This keeps the blast radius of the write-capable credential as small as GitLab's permission model allows, even though the scope itself is broad.

## Webhook strategy

GitLab webhooks come in three tiers:

| Tier | Coverage | Required role | Availability |
|---|---|---|---|
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

## Deployment

The app is a single stateless Go binary plus a PostgreSQL database. It isn't tied to any one deployment platform. Supported/planned targets (see the deployment issue for details):

- Kubernetes (Helm chart)
- Docker / Docker Compose
- Bare binary + systemd unit, for a plain VM/server install

## Status

Early design stage: see [open issues](https://github.com/BoxBoxJason/gitlab-achievements/issues) for the current breakdown of work and open questions.

## License

[MIT](./LICENSE)
