---
title: Documentation
---

## Getting it running

| Guide | What it covers |
| --- | --- |
| [GitLab-side setup]({{< relref "/docs/gitlab-setup" >}}) | The group, the two credentials, the webhook secret, reachability. Read this first, whichever target you pick |
| [Quick start]({{< relref "/docs/quick-start" >}}) | Docker Compose on one host, in a few minutes |
| [Configuration]({{< relref "/docs/configuration" >}}) | Every flag and environment variable, the DSN formats, the subcommands |
| [Kubernetes]({{< relref "/docs/deployment/kubernetes" >}}) | Helm chart, secrets, probes, ingress, running the backfill as a Job |
| [Docker Compose]({{< relref "/docs/deployment/docker-compose" >}}) | App plus PostgreSQL on one host, TLS in front, backups, scheduled jobs |
| [Bare binary]({{< relref "/docs/deployment/bare-binary" >}}) | systemd unit against a database you manage, plus timers |
| [Helm chart values]({{< relref "/docs/deployment/helm-values" >}}) | Every value of the chart, generated from `values.yaml` |
| [The frontend]({{< relref "/docs/frontend" >}}) | The built-in web frontend, working on it, and serving a different one |
| [Metrics]({{< relref "/docs/metrics" >}}) | What is exported for Prometheus, and what to alert on |
| [Uninstalling]({{< relref "/docs/uninstall" >}}) | Taking the webhooks and achievements back off your instance |

## Understanding it

| Guide | What it covers |
| --- | --- |
| [How it works]({{< relref "/docs/how-it-works" >}}) | The lifecycle from bootstrap to steady state, the two tokens, where state lives |
| [Who takes part]({{< relref "/docs/participation" >}}) | Bot exclusion, excluding accounts by name or pattern, opting out |
| [Achievements and EXP]({{< relref "/docs/achievements" >}}) | The catalog, the curves, how EXP is derived, what reaches GitLab |
| [The API]({{< relref "/docs/api" >}}) | Endpoints, response shapes, authentication |
| [CLI reference]({{< relref "/docs/cli-reference" >}}) | The generated `--help` of every command |
| [Webhooks]({{< relref "/docs/webhooks" >}}) | Which hooks get registered and why, the hourly sweep, event coverage |
| [Historical backfill]({{< relref "/docs/backfill" >}}) | Awarding for activity that predates the bot |
| [Reconciliation]({{< relref "/docs/reconciliation" >}}) | Catching activity whose webhook delivery was lost |
| [GitLab achievements API behavior]({{< relref "/docs/achievements-api-behavior" >}}) | What GitLab's achievement mutations actually do, verified on a live instance |

New here? Start with the [Quick start]({{< relref "/docs/quick-start" >}}).
