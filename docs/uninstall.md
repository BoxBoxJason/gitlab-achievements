# Uninstalling

Retiring the deployment leaves its footprint on GitLab: webhooks pointing at a URL that no longer answers, and 275 achievement definitions in your namespace. The `uninstall` subcommand takes both off.

```bash
gitlab-achievements uninstall --dry-run          # report what would go, call nothing
gitlab-achievements uninstall                    # hooks and achievements
gitlab-achievements uninstall --keep-achievements # hooks only, leave people their badges
gitlab-achievements uninstall --sweep            # also hunt down what the database has no record of
```

Same flags and environment as the server.

> [!IMPORTANT]
> Removing the achievements needs 0.2.0 or later. Earlier versions have an `uninstall` that takes the webhooks off and stops there — it reports what it did, says nothing about achievements, and exits successfully, so a half-finished removal looks like a complete one. Run the subcommand from 0.2.0 or later whatever version the deployment itself was running; it reads the same database and removes what any earlier version created.

> [!WARNING]
> Deleting an achievement takes its awards with it. GitLab removes the badge from the profile of everyone holding it, and nothing brings those back. If people have earned things you would rather they keep, use `--keep-achievements`.

## Stop the server first

A running server re-registers every hook on its next hourly sweep and recreates every achievement on its next start, so removing them underneath it accomplishes nothing.

That is also why this is a subcommand rather than something the server does on `SIGTERM`. A hook torn down on shutdown would be rebuilt on the next start, so every rollout and every pod eviction would churn every hook on the instance and lose the events arriving in between.

## How it works

Removal is driven by the same recorded IDs the hourly sweep re-applies configuration with, so it costs one `DELETE` per hook rather than a walk of the instance. Each record is dropped as its hook goes, so an interrupted run leaves behind exactly what it had not reached yet, and re-running finishes the job.

The hooks go first. With ingestion stopped, nothing new is earned while the achievements are being deleted underneath it.

Something GitLab no longer has counts as removed. Something on a group, project or namespace the write token may not manage is left in place, reported, and keeps its record, so re-running with a better-privileged token picks up precisely those. A run that skipped anything says so at `warn` with a count.

## When the database is gone

`--sweep` covers the case the records cannot: a database that was lost, or restored from a backup predating the last registration sweep. It enumerates the instance and removes any hook whose URL is this app's, plus any achievement in the namespace matching its catalog, leaving every other integration alone.

Under `HOOK_SCOPE=auto` on a Premium/Ultimate instance it sweeps projects as well as groups, since the deployment may have been running the project scope before the license changed.

Run `uninstall` before you drop the database, not after. Doing it in the other order leaves the app no record of what it registered, and `--sweep` is the more expensive way back.

## Per target

Each deployment guide has the runnable version:

- [Kubernetes](deployment/kubernetes.md#uninstalling), as a one-off Job carrying the release's own configuration
- [Docker Compose](deployment/docker-compose.md#uninstalling), as `docker compose run --rm app uninstall`
- [Bare binary](deployment/bare-binary.md#uninstalling), as a transient `systemd-run` unit
