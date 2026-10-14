# Metrics

The server exports Prometheus metrics at `/metrics` on a listener of its own, `METRICS_ADDR` (`:9464` by default). It is kept off `LISTEN_ADDR` on purpose: that port is what an ingress exposes to GitLab and to browsers, and metrics are for the cluster only. Set `METRICS_ADDR=off` to serve none.

The listener starts before bootstrap, so a first start that spends minutes registering webhooks is visible from the beginning: `gitlab_achievements_ready` reads `0` until it finishes.

The one-shot subcommands (`backfill`, `reconcile`, `uninstall`) serve no metrics. They exit when they are done, and their result is in their exit code and logs.

On Kubernetes the chart exposes the port as `metrics` on the Service and can create a `ServiceMonitor` for the Prometheus Operator:

```yaml
metrics:
  enabled: true
  port: 9464
  serviceMonitor:
    enabled: true
    labels:
      release: kube-prometheus-stack
```

## What is exported

Every metric is prefixed `gitlab_achievements_`. Label values come from fixed sets only: no path, username, or project ID ever becomes a label, so the number of series does not grow with the instance or with hostile traffic.

| Metric | Type | Labels | What it says |
| --- | --- | --- | --- |
| `build_info` | gauge | `version`, `goversion` | Always 1; which build is running |
| `ready` | gauge | | 1 once bootstrap has finished and the server is serving |
| `http_requests_total` | counter | `route`, `method`, `code` | Requests served. `route` is the matched pattern (`/webhooks/gitlab`, `/api/v1/`, `/oauth/`, `/healthz`, `/readyz`) or `unmatched` |
| `http_request_duration_seconds` | histogram | `route` | Time to serve a request |
| `webhook_deliveries_total` | counter | `event_type`, `result` | GitLab deliveries by result: `accepted`, `ignored`, `unparsable`, `unauthorized`, `rejected`. Event types the app does not subscribe to are reported as `other` |
| `queue_activities_total` | counter | `outcome` | Activities through the webhook queue: `accepted`, `rejected` (queue full or shutting down), `processed`, `failed` (dropped after every retry) |
| `queue_pending` | gauge | | Activities waiting to be evaluated |
| `queue_capacity` | gauge | | How many the queue holds before deliveries are refused |
| `queue_evaluation_duration_seconds` | histogram | | Time to evaluate one activity, retries included |
| `engine_events_total` | counter | `outcome` | Activities evaluated, from every source (webhooks, backfill, reconciliation): `counted`, `duplicate`, `untracked` |
| `engine_tiers_awarded_total` | counter | | Achievement tiers newly earned |
| `awards_deliveries_total` | counter | `result` | Award changes pushed to GitLab: `confirmed`, `adopted`, `superseded`, `failed` |
| `job_runs_total` | counter | `job`, `result` | Background runs by `success` or `failure`. A run cut short by shutdown is not counted |
| `job_duration_seconds` | histogram | `job` | How long a run took |
| `job_last_success_timestamp_seconds` | gauge | `job` | When the job last succeeded, or when it was scheduled if it has not succeeded since the process started, so a job that never succeeds after a restart still goes stale |
| `gitlab_requests_total` | counter | `client`, `method`, `code` | Requests to GitLab by client (`read`, `write`, `verify`). Each retry counts on its own; `code` is `error` when no response came back |
| `gitlab_request_duration_seconds` | histogram | `client` | Time GitLab took to answer |

The jobs are `webhook_reconcile` and `achievement_reconcile` (hourly), `activity_reconcile` (`RECONCILE_INTERVAL`), `backfill` (once), and `session_prune` (hourly, only with `API_AUTH=gitlab`).

The standard Go runtime (`go_*`) and process (`process_*`) metrics are exported too.

## What to alert on

```yaml
groups:
  - name: gitlab-achievements
    rules:
      # Activity was accepted from GitLab and then lost. The daily
      # reconciliation will usually heal it, but something is wrong.
      - alert: GitLabAchievementsDroppingActivity
        expr: increase(gitlab_achievements_queue_activities_total{outcome="failed"}[15m]) > 0
        labels: { severity: warning }

      # GitLab is being told deliveries failed. Enough of these and it
      # disables the hook.
      - alert: GitLabAchievementsRejectingDeliveries
        expr: increase(gitlab_achievements_webhook_deliveries_total{result="rejected"}[10m]) > 0
        labels: { severity: warning }

      # Someone is hitting the webhook with a wrong token, or a hook was
      # registered with an old secret.
      - alert: GitLabAchievementsUnauthorizedDeliveries
        expr: increase(gitlab_achievements_webhook_deliveries_total{result="unauthorized"}[1h]) > 10
        labels: { severity: info }

      # The hourly sweeps re-register lost hooks and push awards. Three
      # missed hours means neither is happening.
      - alert: GitLabAchievementsSweepStale
        expr: time() - gitlab_achievements_job_last_success_timestamp_seconds{job=~"webhook_reconcile|achievement_reconcile"} > 3 * 3600
        labels: { severity: warning }

      - alert: GitLabAchievementsAwardsFailing
        expr: increase(gitlab_achievements_awards_deliveries_total{result="failed"}[2h]) > 0
        labels: { severity: warning }

      - alert: GitLabAchievementsGitLabErrors
        expr: |
          sum(rate(gitlab_achievements_gitlab_requests_total{code=~"5..|error"}[10m]))
            / sum(rate(gitlab_achievements_gitlab_requests_total[10m])) > 0.1
        labels: { severity: warning }
```
