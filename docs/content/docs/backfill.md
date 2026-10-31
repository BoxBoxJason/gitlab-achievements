---
title: Historical backfill
weight: 120
---

Before the bot existed, people were already working. The backfill walks that history once and awards for it, so a mature instance does not start everybody at zero.

It reads every group-owned project the read token can see, in ascending project ID order, and replays each one's history through the same achievement engine live webhook events feed. Historical and live activity are judged by identical rules rather than by two implementations that drift apart.

## What it reads

Per project:

- **The Events API** (`GET /projects/:id/events`) for pushes and their commit counts, branch and tag creation, merge requests opened, merged, approved and closed, issues opened and closed, comments, including which of them were left on a merge request's diff, and wiki pages created. The [Audit Events API](https://docs.gitlab.com/api/audit_events/) would be richer, but it is Premium/Ultimate only, so a Free/CE-compatible backfill cannot rely on it.
- **The Pipelines API**, because pipelines do not appear in the Events API at all.
- **The releases API**, the only place releases and their authors are recorded.
- **The merge requests API** for merged merge requests. The Events API reports a merge as the merger's, without saying whose work it was, so this is where merge requests landing for their authors come from.

Between them that covers most of the catalog, including the engagement criteria: every dated activity also records the day it happened on, which is what streaks and the night owl and early bird criteria are derived from.

Five criteria have no Events API equivalent at all (deployments, jobs, emoji, discussion resolutions, fast merges). Those only ever advance from live deliveries, so they start at zero on a freshly bootstrapped instance however long it has existed.

## How it behaves

**It resumes.** Progress is persisted as it goes: last completed project, in-flight phase (events, pipelines, merge requests, releases), event date cursor, last processed pipeline. An interrupted walk picks up near where it stopped. Re-walked activity is deduplicated by the engine, so a coarse cursor costs a few repeated reads and never a double-counted commit. A completion watermark records when history was walked end to end, which is what tells the app the cold start is over.

**It goes slowly on purpose.** This is the heaviest read workload the app ever runs against an instance it does not own, and it has no deadline. Requests are capped at `BACKFILL_RATE`, 5 per second by default, on a client of its own so a readiness probe never queues behind the walk. 429 and 5xx responses are retried using GitLab's own `Retry-After`. A project the read token cannot see is skipped rather than fatal, and projects in personal namespaces are skipped to match [what the webhooks cover]({{< relref "/docs/webhooks#two-things-to-know-before-you-deploy" >}}).

**You can bound it.** `BACKFILL_SINCE` caps how far back the walk reaches, as a date (`2024-01-01`) or a duration (`720h`). It is passed to GitLab as a server-side filter, so a narrower window means proportionally fewer requests rather than fetching everything and throwing most of it away. Unset walks the full history.

## The gap, and how it closes

The walk stops at the moment the process started, just before hooks were registered, so it does not spend requests re-reading a window live ingestion already covers. That ceiling is about request budget, not correctness: both paths derive the [same deduplication key]({{< relref "/docs/reconciliation#deduplication" >}}) for the same activity, so anything either has counted is discarded whichever one sees it again.

There is a real gap, though, and it is the registration sweep's duration. Hooks start delivering as the sweep reaches them, not all at once when the process starts, so activity between startup and a given hook's registration is seen by neither path at the time. The [daily reconciliation]({{< relref "/docs/reconciliation" >}}) closes it on its first pass. Bootstrap logs the window's width as `uncovered_window`: seconds on a paid instance, however long a full project sweep takes on a Free one.

## Running it

By default (`BACKFILL=auto`) the serving process runs the walk once in the background after bootstrap. It never blocks startup, a restart resumes an interrupted walk, and a finished one is never repeated.

On instances big enough that the cold start deserves its own job, set `BACKFILL=off` on the deployment and run it explicitly:

```bash
gitlab-achievements backfill
```

Same flags and environment as the server. It runs bootstrap first, so it does not need the server to have started. Add `BACKFILL=force` to walk an instance whose previous walk already finished, which exists for recovering from a walk that ran against a broken catalog rather than for steady state. It is also how criteria added in an upgrade get their history: everything the earlier walk counted is skipped again, while what only the new criteria read is counted for the first time.

Awards the walk records are pushed to GitLab as soon as it finishes, instead of waiting for the hourly award reconciliation to notice them.

Each deployment guide has the target-specific version: a [Kubernetes Job]({{< relref "/docs/deployment/kubernetes#running-the-backfill-as-a-job" >}}), a [compose run]({{< relref "/docs/deployment/docker-compose#running-the-backfill-separately" >}}), a [systemd-run unit]({{< relref "/docs/deployment/bare-binary#running-the-backfill-separately" >}}).
