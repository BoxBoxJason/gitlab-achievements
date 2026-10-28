# gitlab-achievements-chart

![Version: 0.7.1](https://img.shields.io/badge/Version-0.7.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.6.0](https://img.shields.io/badge/AppVersion-0.6.0-informational?style=flat-square)

Event-driven achievement awarder for self-hosted GitLab instances

**Homepage:** <https://github.com/BoxBoxJason/gitlab-achievements>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| BoxBoxJason |  | <https://github.com/BoxBoxJason> |

## Source Code

* <https://github.com/BoxBoxJason/gitlab-achievements>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling. |
| backfillJob.affinity | object | `{}` | Affinity rules of the Job's pod. |
| backfillJob.backoffLimit | int | `3` | Retries before the Job is marked failed. |
| backfillJob.enabled | bool | `false` | Run the historical walk as its own one-off Job, for instances big enough that the cold start shouldn't happen in the serving pod. Set `config.backfill.mode` to off with it. The Job is not a Helm hook: it runs once, and re-running means deleting it and upgrading. |
| backfillJob.force | bool | `false` | Walk history again even if a previous walk finished. |
| backfillJob.nodeSelector | object | `{}` | Node selector of the Job's pod. |
| backfillJob.podAnnotations | object | `{}` | Annotations added to the Job's pod. |
| backfillJob.resources | object | `{}` | Resource requests and limits of the Job's pod. |
| backfillJob.tolerations | list | `[]` | Tolerations of the Job's pod. |
| backfillJob.ttlSecondsAfterFinished | int | `86400` | Seconds a finished Job is kept before cleanup. |
| config.achievementsNamespace | string | `""` | Full path of the namespace that owns the achievement definitions. |
| config.api.allowedGroup | string | `""` | Full path of a GitLab group whose members (and instance admins) may use the API. Empty admits any GitLab user. |
| config.api.auth | string | `"none"` | API authentication: none, or gitlab to verify a GitLab token on every request. |
| config.api.authCacheTTL | string | `"1m"` | How long a verified GitLab credential is trusted before GitLab is asked again, which is also how long a revoked token keeps working. 0s disables the cache. |
| config.api.oauthClientId | string | `""` | Client ID of a hand-registered OAuth application; empty self-registers. |
| config.api.oauthClientIdRef | object | `{}` | `{name, key}` of a Secret holding the OAuth client ID; wins over `oauthClientId`. |
| config.api.returnOrigins | list | `[]` | Origins besides `publicUrl` a login may send the browser back to, e.g. a page of your own on another host. |
| config.api.sessionLifetime | string | `"168h"` | How long a browser stays signed in. Sessions renew their GitLab token as it runs out, so this is how often people log in again. |
| config.backfill.mode | string | `"auto"` | Cold-start history walk: auto, off or force. Use off when running it as the separate `backfillJob` rather than in the serving pod. |
| config.backfill.rate | int | `5` | Requests per second the historical walk may issue. It also caps the reconciliation sync, as both are instance-wide read sweeps. |
| config.backfill.since | string | `""` | How far back history is walked: a date (2024-01-01) or a duration (720h). Empty walks everything. |
| config.exclusions.allowOptOut | bool | `true` | Let users opt themselves out through the API. Needs `config.api.auth: gitlab`, since the API has to know who is asking. |
| config.exclusions.bots | bool | `true` | Leave out accounts GitLab flags as bots: access token users, service accounts and GitLab's own bots. |
| config.exclusions.usernamePattern | string | `""` | Regular expression (RE2); matching usernames are left out. |
| config.exclusions.users | list | `[]` | Usernames or GitLab user IDs to leave out. |
| config.extraEnv | list | `[]` | Extra environment variables, appended last so they override anything above. Standard Kubernetes env entries, so `valueFrom` works. |
| config.gitlabUrl | string | `""` | Base URL of the GitLab instance to mirror, e.g. https://gitlab.example.com. |
| config.hookRate | int | `20` | Groups or projects per second the webhook registration sweep works through. |
| config.hookScope | string | `"auto"` | Webhook scope: auto, group or project. auto picks group hooks where the license allows. |
| config.logLevel | string | `"info"` | Log level: debug, info, warn or error. |
| config.publicUrl | string | `""` | Externally reachable base URL of this app. GitLab's webhooks are registered against it, so the instance must be able to resolve and reach it. |
| config.reconcile.interval | string | `"24h"` | How often recent activity is re-read. |
| config.reconcile.lookback | string | `"48h"` | How far back each pass reaches. Must exceed the interval so consecutive windows overlap; the app refuses to start otherwise. |
| config.reconcile.mode | string | `"auto"` | Periodic re-read of recent activity, healing webhook deliveries GitLab never made. auto runs it in the serving pod; off disables it, for when you schedule `gitlab-achievements reconcile` from outside the chart. |
| deployment.annotations | object | `{}` | Annotations for the deployment |
| extraVolumeMounts | list | `[]` | Extra volume mounts added to the app container of the Deployment and of the Jobs. To trust a private CA, mount it and point `SSL_CERT_DIR` at it through `config.extraEnv`: Go then trusts it in addition to the image's bundle (`SSL_CERT_FILE` would replace the bundle). |
| extraVolumes | list | `[]` | Extra volumes added to the pod of the Deployment and of the backfill and uninstall Jobs, e.g. a ConfigMap holding a private CA. Standard Kubernetes volume entries. |
| fullnameOverride | string | `""` | Overrides the full object name prefix; empty derives it from the release name. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/boxboxjason/gitlab-achievements"` | Container image repository. |
| image.tag | string | `""` | Image tag; pin a released version in production, empty uses the chart's appVersion. A digest may be appended, e.g. `0.3.0@sha256:47c8...`. |
| imagePullSecrets | list | `[]` | Secrets used to pull the image from a private registry. |
| ingress.annotations | object | `{}` | Ingress annotations. |
| ingress.className | string | `""` | IngressClass name. |
| ingress.enabled | bool | `false` | Create an Ingress. |
| ingress.hosts | list | `[{"host":"achievements.example.com","paths":[{"path":"/","pathType":"Prefix"}]}]` | Hosts and paths the Ingress routes to the app. |
| ingress.tls | list | `[]` | TLS configuration. |
| livenessProbe.enabled | bool | `true` | Enable the liveness probe. |
| livenessProbe.failureThreshold | int | `3` | Failed probes before the container is restarted. |
| livenessProbe.periodSeconds | int | `10` | Seconds between probes. |
| livenessProbe.timeoutSeconds | int | `3` | Seconds before a probe times out. |
| metrics.enabled | bool | `true` | Serve Prometheus metrics on a port of their own, on a ClusterIP Service of their own, so neither the Ingress nor a LoadBalancer/NodePort app Service ever exposes them. See docs/metrics.md. |
| metrics.port | int | `9464` | Port the metrics are served on. |
| metrics.serviceMonitor.enabled | bool | `false` | Create a ServiceMonitor; requires the Prometheus Operator's CRDs. |
| metrics.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| metrics.serviceMonitor.labels | object | `{}` | Extra labels, e.g. the release label your Prometheus selects on. |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| nameOverride | string | `"gitlab-achievements"` | Overrides the chart name used in object names and labels. |
| nodeSelector | object | `{}` | Node selector for pod scheduling. |
| persistence.accessMode | string | `"ReadWriteOnce"` | Access mode of the volume. |
| persistence.enabled | bool | `true` | Create a PVC for the default SQLite database; disable when the database DSN points elsewhere. |
| persistence.existingClaim | string | `""` | Use an existing claim instead of creating one. |
| persistence.mountPath | string | `"/data"` | Directory the volume is mounted at. The default SQLite DSN points inside it. |
| persistence.size | string | `"1Gi"` | Size of the volume. |
| persistence.storageClassName | string | `""` | StorageClass of the volume; empty uses the cluster default. |
| podAnnotations | object | `{}` | Annotations added to the pod. |
| podLabels | object | `{}` | Labels added to the pod. |
| podSecurityContext.fsGroup | int | `1000` | Group that owns mounted volumes. |
| podSecurityContext.runAsGroup | int | `1000` | GID the container runs as. |
| podSecurityContext.runAsNonRoot | bool | `true` | Refuse to start containers running as root. |
| podSecurityContext.runAsUser | int | `1000` | UID the container runs as. |
| podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` | Seccomp profile type. |
| priorityClassName | string | `""` | PriorityClass of the pod. |
| readinessProbe.enabled | bool | `true` | Enable the readiness probe. |
| readinessProbe.failureThreshold | int | `3` | Failed probes before the pod is marked unready. |
| readinessProbe.periodSeconds | int | `10` | Seconds between probes. |
| readinessProbe.timeoutSeconds | int | `5` | Seconds before a probe times out. |
| replicaCount | int | `1` | Number of replicas. The app is a singleton by design: one process registers the instance's webhooks, runs the hourly reconciliation sweeps and walks history, so a second replica would duplicate all three against the same GitLab instance. |
| resources.limits.memory | string | `"512Mi"` | Memory limit. |
| resources.requests.cpu | string | `"50m"` | CPU request. |
| resources.requests.memory | string | `"128Mi"` | Memory request. |
| secrets.existingSecret | string | `""` | Name of a Secret loaded with envFrom. Keys must be GITLAB_READ_TOKEN, GITLAB_WRITE_TOKEN, WEBHOOK_SECRET, DATABASE_DSN, and OAUTH_CLIENT_SECRET / SESSION_SECRET if used. |
| secrets.refs | object | `{}` | Per-credential `{name, key}` of any Secret; wins over `existingSecret`. Keys: gitlabReadToken, gitlabWriteToken, webhookSecret, databaseDsn, oauthClientSecret, sessionSecret (required when `config.api.auth` is gitlab). |
| securityContext.allowPrivilegeEscalation | bool | `false` | Forbid gaining more privileges than the parent process. |
| securityContext.capabilities.drop | list | `["ALL"]` | Linux capabilities to drop. |
| securityContext.privileged | bool | `false` | Run the container privileged. |
| securityContext.readOnlyRootFilesystem | bool | `true` | Mount the root filesystem read-only. |
| service.annotations | object | `{}` | Service annotations. |
| service.port | int | `8080` | Service port. |
| service.type | string | `"ClusterIP"` | Service type. |
| serviceAccount.annotations | object | `{}` | ServiceAccount annotations. |
| serviceAccount.automountServiceAccountToken | bool | `false` | Mount the API token into pods; the app never talks to the Kubernetes API. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name; empty derives it from the release name. |
| startupProbe.enabled | bool | `true` | Enable the startup probe. |
| startupProbe.failureThreshold | int | `60` | Failed probes before the container is restarted. |
| startupProbe.periodSeconds | int | `10` | Seconds between probes. |
| startupProbe.timeoutSeconds | int | `3` | Seconds before a probe times out. |
| strategy | object | `{"type":"Recreate"}` | Deployment strategy; Recreate so two pods never overlap during a rollout. |
| terminationGracePeriodSeconds | int | `60` | Seconds granted on shutdown to drain webhook deliveries GitLab was already told succeeded. |
| tolerations | list | `[]` | Tolerations for pod scheduling. |
| uninstallJob.affinity | object | `{}` | Affinity rules of the Job's pod. |
| uninstallJob.backoffLimit | int | `3` | Retries before the Job is marked failed. |
| uninstallJob.dryRun | bool | `false` | Report what would be removed and call nothing; worth a run of its own first. |
| uninstallJob.enabled | bool | `false` | Run the Job that removes the app's webhooks and achievements from GitLab. Read the comment above in values.yaml before enabling. |
| uninstallJob.keepAchievements | bool | `false` | Remove only the webhooks, leaving people the badges they earned. |
| uninstallJob.nodeSelector | object | `{}` | Node selector of the Job's pod. |
| uninstallJob.podAnnotations | object | `{}` | Annotations added to the Job's pod. |
| uninstallJob.resources | object | `{}` | Resource requests and limits of the Job's pod. |
| uninstallJob.sweep | bool | `false` | Also enumerate the instance for hooks and achievements the app has no record of, for a lost or restored database. Walks every group or project, so it is not the default. |
| uninstallJob.tolerations | list | `[]` | Tolerations of the Job's pod. |
| uninstallJob.ttlSecondsAfterFinished | int | `86400` | Seconds a finished Job is kept. Keep it long enough to read the logs, the only report of what was removed and what was left behind. |

