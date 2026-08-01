# Reconciliation

Webhooks are best-effort. GitLab retries a delivery a few times and then gives up, so a network blip, a deploy or an instance restart is enough to lose one for good. Nothing in the live path notices, because a delivery that never arrives leaves no trace of having been missed.

The reconciliation sync is the safety net. Once a day (`RECONCILE_INTERVAL`, default `24h`) it asks GitLab what actually happened over the last two days (`RECONCILE_LOOKBACK`, default `48h`) and replays it through the achievement engine.

It reads the same two sources the backfill does, the Events API and the Pipelines API, but only over projects GitLab reports as active in the window. A quiet instance costs almost nothing however many projects it holds.

## Deduplication

Almost everything a pass reads is activity the live path already counted, so the sync is only safe because the read side and the live side derive byte-identical keys. They are reconstructed from the identifiers GitLab reports on both sides rather than from the event record's own ID:

| Activity | Key | From, on the read side |
| --- | --- | --- |
| Push, tag push | `push:<project>:<ref>:<after>` | `push_data.ref` + `push_data.commit_to` |
| Merge request | `merge_request:<id>:<action>` | `target_id` |
| Issue | `issue:<id>:<action>` | `target_id` |
| Comment | `note:<id>` | `note.id` |
| Pipeline | `pipeline:<id>` | the pipeline's ID |

The engine keeps a processed-event log keyed on exactly that, so a pass over a window the webhooks covered correctly is a no-op.

This matters more than it looks. GitLab never revokes an award, so a counter inflated by counting the same push twice cannot be brought back down. A cross-producer test suite (`internal/webhook/dedup_agreement_test.go`) asserts the agreement for every kind of activity, in both directions.

## What it cannot heal

The Events API has no representation for jobs, deployments, emoji reactions, wiki pages, resolved discussions or fast merges, so a lost delivery of one of those stays lost. That is the right direction to be wrong in: the sync undercounts what it cannot see rather than guessing at it under a key that would not match.

## Running it

`RECONCILE=auto` runs it inside the serving process: one pass a few minutes after startup, then every interval.

The startup pass is not optional. The timer's phase is the process's start time, so without it a deployment restarted more often than the interval would never reconcile at all, and nothing would say so. Repeating it is cheap, because the watermark means a pass after a restart covers only the gap since the last successful one. Passes are skipped until the historical backfill has completed, since until then the cold start is covering the same ground.

`RECONCILE=off` hands it to an external schedule instead, a systemd timer, a cron entry, a Kubernetes `CronJob`:

```bash
gitlab-achievements reconcile
```

Reach for that when you want a pass to be a unit you can start, watch and alert on, or pinned to a quiet wall-clock hour rather than to whenever the process last restarted.

Unlike the server and `backfill`, this subcommand does not bootstrap. It registers no webhooks and creates no achievements, so a scheduled pass costs one sweep of recently active projects rather than a sweep of the whole instance. It does need the database to have been bootstrapped once, by the server or by `backfill`, and refuses to run otherwise.

## Watching it

A pass records a watermark only on success, and the next pass widens its window to reach back to it, so a run that failed or never happened is made up rather than leaving a hole. A pass with ground to make up logs at `warn` with a `gap` field.

The number to watch is `activity_counted` in the completion log. In a healthy deployment it stays at zero pass after pass. A persistently non-zero value means deliveries are being lost for a reason worth finding rather than papering over.

## Interval and look-back

The look-back has to be wider than the interval so consecutive windows overlap. Windows that merely abut lose anything GitLab timestamps on the far side of the boundary, and nothing would report the loss, so the app refuses to start on a configuration where they do.
