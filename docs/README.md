# Documentation

| Guide | What it covers |
| --- | --- |
| [GitLab-side setup](gitlab-setup.md) | The group, the two credentials, the webhook secret, and reachability. Read this first, whichever target you pick |
| [Configuration](configuration.md) | Every flag and environment variable, the database DSN formats, and the HTTP endpoints |
| [Kubernetes](deployment/kubernetes.md) | Helm chart, secrets, probes, ingress, running the backfill as a Job |
| [Docker Compose](deployment/docker-compose.md) | App plus PostgreSQL on one host, TLS in front, backups |
| [Bare binary](deployment/bare-binary.md) | systemd unit against an externally managed database |
| [Achievements API behavior](achievements-api-behavior.md) | What GitLab's achievement mutations actually do, verified on a live instance |

The project overview, how the app works, and the achievement catalog are in the
[README](../README.md).
