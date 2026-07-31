# Kubernetes

A Helm chart lives in [`chart/gitlab-achievements-chart`](../../chart).
It deploys the app and nothing else: point it at a PostgreSQL you already
manage. Work through [GitLab-side setup](../gitlab-setup.md) first.

## Install

From the published chart:

```bash
helm install gitlab-achievements \
  oci://ghcr.io/boxboxjason/gitlab-achievements-chart \
  --version 0.1.0 \
  --namespace gitlab-achievements --create-namespace \
  --values values.yaml
```

Or from a checkout of this repository:

```bash
helm install gitlab-achievements ./chart \
  --namespace gitlab-achievements --create-namespace \
  --values values.yaml
```

A minimal `values.yaml`:

```yaml
image:
  tag: "0.1.0"          # pin a release rather than tracking latest

config:
  gitlabUrl: https://gitlab.example.com
  achievementsNamespace: achievements
  publicUrl: https://achievements.example.com

secrets:
  existingSecret: gitlab-achievements-credentials

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: achievements.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: gitlab-achievements-tls
      hosts:
        - achievements.example.com
```

Every value, with comments, is in the chart's
[values.yaml](../../chart/values.yaml); what the
settings underneath them do is in [configuration.md](../configuration.md).

## Credentials

The chart reads all four credentials from one Secret, loaded with `envFrom`, so
they never appear in a pod spec or a `helm get values` output.

Recommended: create it yourself, from whatever you already use — External
Secrets, sealed secrets, SOPS, or by hand — and name it in
`secrets.existingSecret`. The keys are the app's environment variables:

```bash
kubectl -n gitlab-achievements create secret generic gitlab-achievements-credentials \
  --from-literal=GITLAB_READ_TOKEN='glpat-...' \
  --from-literal=GITLAB_WRITE_TOKEN='glpat-...' \
  --from-literal=WEBHOOK_SECRET="$(openssl rand -hex 32)" \
  --from-literal=DATABASE_DSN='postgres://achievements:...@postgres.databases.svc:5432/achievements?sslmode=require'
```

Add `OAUTH_CLIENT_SECRET` only if you registered a confidential OAuth
application by hand.

Alternatively, set `secrets.gitlabReadToken` and friends and let the chart
create the Secret. That puts an instance-admin token in your values file and in
the release's stored manifest, which is a poor fit for anything committed to
git — see the trust callout in [GitLab-side setup](../gitlab-setup.md#4-create-the-write-credential).
When the chart owns the Secret it also annotates the pod with its checksum, so
rotating a token in values rolls the pod instead of leaving it running on the
old credential.

## The database

The chart deploys no database, on purpose: a bundled one would be a dependency
to version, patch and CVE-track for the sake of a workload that is trivially
small and belongs on whatever PostgreSQL your cluster already has.

Point `DATABASE_DSN` at it. Anything the schemes in
[configuration.md](../configuration.md#databases) cover works; PostgreSQL is
what this is tested against. The app creates its own schema at startup, so an
empty database and a user that owns it are enough.

## Why one replica

`replicaCount` defaults to 1 and the update strategy to `Recreate`. The process
is a singleton by design: it registers the instance's webhooks, runs the hourly
reconciliation sweeps, and walks history. A second replica would do all three
again against the same GitLab instance — duplicate API load, and two writers
racing over the same rows.

It is not a high-availability workload. Nothing is lost while it restarts:
deliveries GitLab cannot reach are retried on its side, and anything missed
entirely is what the periodic reconciliation sync is for.

## Startup, probes and timing

Bootstrap runs before the server listens: it verifies both tokens, creates 275
achievements, and registers a webhook on every group — or, on Free/CE, every
project on the instance, paced at `config.hookRate` per second. Nothing answers
until that finishes.

The startup probe is what grants it that time, 10 minutes by default. On a Free
instance with thousands of projects, raise it:

```yaml
startupProbe:
  failureThreshold: 180   # 30 minutes at the default 10s period
```

Liveness and readiness only start once it passes, so they need no adjusting.
Readiness checks the database and GitLab are reachable *right now*, which means
a pod goes unready during a GitLab outage and the Service stops routing to it —
intended, but worth knowing before you alert on it.

## Ingress and reachability

`config.publicUrl` is what the app registers its webhooks against, so GitLab
must be able to resolve and reach it. A cluster-internal hostname works only if
GitLab runs in the same cluster and outbound local-network requests are allowed
on the instance; see [GitLab-side setup](../gitlab-setup.md#6-make-the-app-reachable-from-gitlab).

The chart's Ingress is optional and deliberately plain. A Gateway API route, a
`LoadBalancer` Service or a service mesh works just as well — set
`ingress.enabled: false` and route to the Service yourself. Whatever fronts it
must handle one path prefix for everything: webhooks, the API and the probes
all share a port.

## Running the backfill as a Job

By default the serving pod walks history in the background. On an instance big
enough that the cold start deserves its own workload, hand it to a Job instead:

```yaml
config:
  backfill:
    mode: off        # the Deployment must not walk as well
    rate: 5

backfillJob:
  enabled: true
```

The chart refuses to render if you enable the Job while the Deployment is still
set to `auto`, since both would walk the same history.

The Job runs the same bootstrap the server does and then walks, resuming near
where a killed pod stopped. It is a plain Job, not a Helm hook, so `helm
install` does not wait on it. Re-running it means deleting it and upgrading
again, with `backfillJob.force: true` if the previous walk completed.

## Upgrades

```bash
helm upgrade gitlab-achievements oci://ghcr.io/boxboxjason/gitlab-achievements-chart \
  --version 0.2.0 --namespace gitlab-achievements --values values.yaml
```

Schema migrations run at startup, and `Recreate` means the old pod is gone
before the new one starts, so there is never a mixed-version pair against one
database. The pod is unready for as long as bootstrap takes; deliveries in that
window are retried by GitLab.

## Uninstalling

```bash
helm uninstall gitlab-achievements --namespace gitlab-achievements
```

This removes the app, not its footprint on GitLab. The webhooks it registered
stay on every group or project, the achievements stay in the namespace, and the
awards stay on people's profiles — nothing revokes them, and no reinstall
depends on them being gone. Clean up the hooks by hand if the removal is meant
to be permanent.
