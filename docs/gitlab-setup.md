# GitLab-side setup

Everything the app needs to exist on your GitLab instance before it starts,
whichever way you deploy it. None of it is deployment-target specific: the
same group, the same two credentials and the same reachable URL are what
Kubernetes, Docker Compose and a systemd unit all end up pointing at.

Work through it once, then pick a target:
[Kubernetes](deployment/kubernetes.md) ·
[Docker Compose](deployment/docker-compose.md) ·
[bare binary](deployment/bare-binary.md).

## Prerequisites at a glance

| What | Why |
| --- | --- |
| GitLab **16.6 or later**, self-hosted | Achievements do not exist before it |
| A **group** to own the achievement definitions | The 275 definitions are created in it |
| A **read credential** (`read_api`) | All data fetching: backfill, event enrichment |
| A **write credential** (`api`, instance Admin) | Registering webhooks, creating and awarding achievements |
| A **URL GitLab can reach** | Webhook deliveries are pushed to it |
| A **database** | All state lives here, not on GitLab |

## 1. Check the GitLab version and feature flag

Achievements arrived in GitLab 16.6 and are still marked **Experiment**, which
on some versions means they sit behind a feature flag. If the app's bootstrap
fails on the achievement GraphQL mutation while the tokens check out, enable
the flag from the instance's Rails console:

```ruby
Feature.enabled?(:achievements)
Feature.enable(:achievements)
```

Being an Experiment also means the API can change between majors. What was
verified, on which version, and how to re-verify it is in
[achievements-api-behavior.md](achievements-api-behavior.md).

Group webhooks are a Premium/Ultimate feature. On Free/CE the app registers one
webhook per project instead, which works on every tier and is what
`HOOK_SCOPE=auto` falls back to; nothing else about the setup changes.

## 2. Create the achievements namespace

Create a group whose only job is to own the achievement definitions, e.g.
`achievements`. Its full path is what `ACHIEVEMENTS_NAMESPACE` is set to
(`achievements`, or `platform/achievements` for a subgroup).

It must be a **group**, not a personal namespace: the app resolves it with the
groups API, and both credentials are checked against it at startup.

Nothing in it needs projects, and the app creates nothing there but
achievements. Awards it hands out point back at these definitions, so deleting
the group later orphans every award on the instance.

## 3. Create the read credential

An account with read access to everything you want tracked, holding a personal
access token with the **`read_api`** scope alone.

The backfill and every event enrichment call go through this token, so its
reach decides what earns achievements: a project it cannot see is skipped
silently rather than failing anything. On most instances the simplest answer is
an account with instance-wide read access; a service account added to the
groups you care about works equally well if you would rather be selective.

It must also be able to read the achievements group from step 2 — startup
checks exactly that, along with the token authenticating at all.

## 4. Create the write credential

A **separate** account with:

- **Administrator** access on the instance, and
- at least **Maintainer** on the achievements group, as a member of it,

holding a personal access token with the **`api`** scope.

Add it to the group explicitly (Group → Manage → Members). The check at startup
reads its direct membership, so the implicit access an administrator has over
every group is not enough.

Create it as a service account if your GitLab version lets you grant one
Administrator, otherwise as a dedicated bot user — the app only cares that
`GET /api/v4/user` reports `is_admin: true`. Either way it should be an account
nobody logs in as.

The two tokens must be different credentials; the app refuses to start if they
are the same string.

> [!IMPORTANT]
> **What the admin token means.** An `api`-scoped token on an administrator
> account can do anything on your instance: read every repository, impersonate
> the API as an admin, change settings, delete projects. GitLab has no scope
> that grants "manage webhooks and achievements" and nothing else, so this is
> the smallest credential that can do the job, not a small credential.
>
> Wherever you deploy, that token is as sensitive as your instance's root
> password. Keep it in the platform's secret store rather than in a file next
> to the config, keep it out of version control and CI logs, restrict who can
> read it to whoever already has admin, and rotate it on the same schedule you
> rotate other admin credentials. The per-target guides say where it goes.
>
> The admin requirement is not the achievements: it is instance-wide webhook
> management. If that trade is not one you want to make, this app is not
> deployable on your instance as designed.

## 5. Generate the webhook secret

A shared secret GitLab presents on every delivery, checked in constant time
before a payload is parsed:

```bash
openssl rand -hex 32
```

It goes in `WEBHOOK_SECRET`. Changing it is safe at any time: the hourly sweep
re-applies every hook's configuration, so a rotated secret heals across the
instance within the hour without re-registering anything.

## 6. Make the app reachable from GitLab

`PUBLIC_URL` is the base URL GitLab delivers webhooks to, so it has to resolve
and be reachable **from the GitLab instance**, not just from you. The hooks are
registered against it at startup, which is also when a wrong value becomes
expensive: correcting it means the next sweep re-pointing every hook.

Two settings commonly get in the way, both under **Admin → Settings → Network →
Outbound requests**:

- *Allow requests to the local network from webhooks and integrations* — needed
  when the app is on a private address or a cluster-internal hostname.
- The allowlist, when your instance restricts outbound webhook destinations.

If you terminate TLS in front of the app, GitLab must trust the certificate;
deliveries to an untrusted one fail with nothing arriving in the app's logs at
all.

Traffic in the other direction matters too: the app calls the GitLab API
constantly, so wherever it runs must be able to reach `GITLAB_URL`.

## 7. Verify

Once deployed, in order:

```bash
curl -sf https://achievements.example.com/readyz && echo ready
```

`/readyz` only answers 200 after bootstrap has verified both tokens, created
the achievements and registered the hooks. Until then the logs name whichever
of those is failing, and every permission problem is reported in one pass
rather than one restart at a time.

Then confirm on GitLab's side:

- The achievements group's **Achievements** page lists the catalog.
- A group (or project) shows a webhook pointing at
  `PUBLIC_URL/webhooks/gitlab`, and its **Recent events** fill up as people
  work.
- A user with past activity has awards — remember an award is invisible until
  its recipient accepts it, so check as an admin rather than by looking at a
  profile.

## Rotating credentials

Both tokens are read at startup and used from memory: replace the value where
your target keeps it and restart the app. Nothing on GitLab's side needs
re-doing, and no state is lost — everything the app knows is in its database,
not in the credential.

The webhook secret is the exception in the other direction: it is stored on
every hook on the instance, and the sweep is what re-applies it. Rotate it,
restart, and deliveries presenting the old secret are refused with a 401 until
the sweep reaches their hook.
