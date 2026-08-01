# GitLab-side setup

Everything that has to exist on your GitLab instance before the app starts. None of it is deployment-specific: the same group, the same two credentials and the same reachable URL are what Kubernetes, Docker Compose and a systemd unit all end up pointing at.

Work through it once, then pick a target: [Kubernetes](deployment/kubernetes.md), [Docker Compose](deployment/docker-compose.md), [bare binary](deployment/bare-binary.md).

## What you need

| What | Why |
| --- | --- |
| GitLab **16.6 or later**, self-hosted | Achievements do not exist before it |
| A **group** to own the achievement definitions | The 275 definitions are created in it |
| A **read credential** (`read_api`) | All data fetching: backfill, event enrichment |
| A **write credential** (`api`, instance Admin) | Registering webhooks, creating and awarding achievements |
| A **URL GitLab can reach** | Webhook deliveries are pushed to it |
| A **database** | All state lives here, not on GitLab |

## 1. Check the version and the feature flag

Achievements arrived in GitLab 16.6 and are still marked **Experiment**, which on some versions means they sit behind a feature flag. If bootstrap fails on the achievement GraphQL mutation while both tokens check out, enable the flag from your instance's Rails console:

```ruby
Feature.enabled?(:achievements)
Feature.enable(:achievements)
```

Being an Experiment also means the API can change between majors. What was verified, on which version, and how to re-verify it is in [GitLab achievements API behavior](achievements-api-behavior.md).

Group webhooks are a Premium/Ultimate feature. On Free/CE the app registers one webhook per project instead, which works on every tier and is what `HOOK_SCOPE=auto` falls back to. Nothing else about the setup changes. See [Webhooks](webhooks.md) for the details.

## 2. Create the achievements group

A group whose only job is to own the achievement definitions, for instance `achievements`. Its full path is what `ACHIEVEMENTS_NAMESPACE` gets set to (`achievements`, or `platform/achievements` for a subgroup).

It has to be a group, not a personal namespace: the app resolves it through the groups API, and both credentials are checked against it at startup.

It needs no projects, and the app creates nothing in it but achievements. Awards point back at these definitions, so deleting the group later orphans every award on the instance.

## 3. Create the read credential

An account with read access to everything you want tracked, holding a personal access token with the **`read_api`** scope alone.

The backfill and every event enrichment call go through this token, so its reach decides what earns achievements. A project it cannot see is skipped silently rather than failing anything. On most instances the simplest answer is an account with instance-wide read access; a service account added to the groups you care about works just as well if you would rather be selective.

It also has to be able to read the achievements group from step 2. Startup checks exactly that, along with the token authenticating at all.

## 4. Create the write credential

A **separate** account with:

- **Administrator** access on the instance, and
- at least **Maintainer** on the achievements group, as a member of it,

holding a personal access token with the **`api`** scope.

Add it to the group explicitly (Group, then Manage, then Members). The startup check reads its direct membership, so the implicit access an administrator has over every group is not enough.

Create it as a service account if your GitLab version lets you grant one Administrator, otherwise as a dedicated bot user. The app only cares that `GET /api/v4/user` reports `is_admin: true`. Either way it should be an account nobody logs in as.

The two tokens have to be different credentials. The app refuses to start if they are the same string.

> [!IMPORTANT]
> **What the admin token means.** An `api`-scoped token on an administrator account can do anything on your instance: read every repository, act as an admin through the API, change settings, delete projects. GitLab has no scope that grants "manage webhooks and achievements" and nothing else, so this is the smallest credential that can do the job, not a small credential.
>
> Wherever you deploy, treat that token as being as sensitive as your instance's root password. Keep it in your platform's secret store rather than in a file next to the config, keep it out of version control and CI logs, restrict who can read it to whoever already has admin, and rotate it on the same schedule as your other admin credentials. The per-target guides say where it goes.
>
> The admin requirement is not about the achievements. It is instance-wide webhook management. If that is not a trade you want to make, this app is not deployable on your instance as designed.

## 5. Generate the webhook secret

A shared secret GitLab presents on every delivery, checked in constant time before a payload is parsed:

```bash
openssl rand -hex 32
```

It goes in `WEBHOOK_SECRET`. Changing it is safe at any time: the hourly sweep re-applies every hook's configuration, so a rotated secret heals across the instance within the hour without re-registering anything.

## 6. Make the app reachable from GitLab

`PUBLIC_URL` is the base URL GitLab delivers webhooks to, so it has to resolve and be reachable **from the GitLab instance**, not just from you. The hooks are registered against it at startup, which is also when a wrong value gets expensive: correcting it means the next sweep re-pointing every hook.

Two settings commonly get in the way, both under **Admin, Settings, Network, Outbound requests**:

- *Allow requests to the local network from webhooks and integrations*, needed when the app is on a private address or a cluster-internal hostname.
- The allowlist, when your instance restricts outbound webhook destinations.

**Silent mode** (Admin, Settings, General, or `silent_mode_enabled` in the application settings API) suppresses every outbound webhook instance-wide. It is meant for restored backups and staging clones, and it is easy to leave on: hooks register normally, deliveries never happen, and testing one by hand answers `{"message":"Silent mode enabled"}` rather than an error.

If you terminate TLS in front of the app, GitLab has to trust the certificate. Deliveries to an untrusted one fail with nothing arriving in the app's logs at all.

Traffic in the other direction matters too. The app calls the GitLab API constantly, so wherever it runs has to be able to reach `GITLAB_URL`.

## 7. Verify

Once deployed:

```bash
curl -sf https://achievements.example.com/readyz && echo ready
```

`/readyz` only answers 200 after bootstrap has verified both tokens, created the achievements and registered the hooks. Until then the logs name whichever of those is failing, and every permission problem is reported in one pass rather than one restart at a time.

Then check GitLab's side:

- The achievements group's **Achievements** page lists the catalog.
- A group (or project) shows a webhook pointing at `PUBLIC_URL/webhooks/gitlab`, and its **Recent events** fill up as people work.
- A user with past activity has awards. Remember that an award is invisible until its recipient accepts it, so check as an admin rather than by looking at a profile.

## Rotating credentials

Both tokens are read at startup and used from memory. Replace the value where your target keeps it and restart. Nothing on GitLab's side has to be redone, and no state is lost: everything the app knows is in its database, not in the credential.

The webhook secret goes the other way. It is stored on every hook on the instance, and the sweep is what re-applies it. Rotate it, restart, and deliveries presenting the old secret are refused with a 401 until the sweep reaches their hook.
