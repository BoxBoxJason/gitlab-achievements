# Documentation

## Getting it running

| Guide | What it covers |
| --- | --- |
| [GitLab-side setup](gitlab-setup.md) | The group, the two credentials, the webhook secret, reachability. Read this first, whichever target you pick |
| [Configuration](configuration.md) | Every flag and environment variable, the DSN formats, the subcommands |
| [Kubernetes](deployment/kubernetes.md) | Helm chart, secrets, probes, ingress, running the backfill as a Job |
| [Docker Compose](deployment/docker-compose.md) | App plus PostgreSQL on one host, TLS in front, backups, scheduled jobs |
| [Bare binary](deployment/bare-binary.md) | systemd unit against a database you manage, plus timers |
| [Uninstalling](uninstall.md) | Taking the webhooks and achievements back off your instance |

## Understanding it

| Guide | What it covers |
| --- | --- |
| [How it works](how-it-works.md) | The lifecycle from bootstrap to steady state, the two tokens, where state lives |
| [Achievements and EXP](achievements.md) | The catalog, the curves, how EXP is derived, what reaches GitLab |
| [The API](api.md) | Endpoints, response shapes, authentication |
| [Webhooks](webhooks.md) | Which hooks get registered and why, the hourly sweep, event coverage |
| [Historical backfill](backfill.md) | Awarding for activity that predates the bot |
| [Reconciliation](reconciliation.md) | Catching activity whose webhook delivery was lost |
| [GitLab achievements API behavior](achievements-api-behavior.md) | What GitLab's achievement mutations actually do, verified on a live instance |

The pitch, the quick start and the catalog at a glance are in the [README](../README.md).
