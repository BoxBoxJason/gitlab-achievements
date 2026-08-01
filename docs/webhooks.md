# Webhooks

Live activity reaches the app through GitLab webhooks. It registers them itself at startup and keeps them healthy from then on, so there is nothing to click in the GitLab UI.

## Which kind, and why

GitLab offers three tiers of hook:

| Tier | Coverage | Required role | Availability |
| --- | --- | --- | --- |
| Project hooks | One project | Maintainer/Owner on the project | All tiers |
| Group hooks | A group and everything under it | Owner on the group | Premium/Ultimate only |
| System hooks | The whole instance | Instance Admin | All tiers |

System hooks look like the obvious answer: one hook, every project, every tier. The catch is that they carry a much narrower event set. No merge request approvals, no comments, and no pipeline events at all, which puts most of the achievement catalog out of reach. So the app uses group and project hooks, which both carry the full set.

Which of the two depends on your license, read at bootstrap from `GET /api/v4/license`:

- **Premium/Ultimate**: one hook per top-level group. A group hook covers the whole subtree, so projects created inside it later need no registration.
- **Free/CE, or no license**: one hook per project, across the instance.

Set `HOOK_SCOPE` to `group` or `project` to pin the choice, for instance when the write token cannot read the license or you would rather it not follow a licence change.

Either way the write token needs instance Admin, both to enumerate every group and project and to manage hooks on ones it is not a member of.

## Two things to know before you deploy

**Projects in personal namespaces earn nothing.** A group hook cannot reach `someuser/project`, because a personal namespace is not a group. Rather than let someone's progress depend on which license their instance holds, the project-hook path skips them too, and so does the historical backfill. Only group-owned projects count, on every tier.

**New projects are picked up within the hour.** `project_create` is only delivered to system hooks, so nothing tells the app a project appeared. One created after bootstrap gets its hook on the next hourly sweep. On Premium/Ultimate this only affects newly created top-level groups, since group hooks already cover new projects underneath them.

## The hourly sweep

Every hour the app walks its targets and re-applies each hook's configuration. In the steady state that costs one API call per target, issued straight off the hook ID recorded when it was registered. A hook deleted out of band costs two more: the edit returns 404, then the target's hooks are listed so any already pointing here can be adopted before a new one is registered.

The sweep is paced at `HOOK_RATE` targets per second, 20 by default, and runs hourly rather than every few minutes. On a Free instance with thousands of projects, a tighter cadence would be a permanent background load on somebody's production GitLab.

The edit is unconditional, and that is the point. GitLab never returns a hook's token, so there is no way to read the remote state and conclude it already matches. A rotated `WEBHOOK_SECRET` would be invisible and the hooks would keep presenting the old one. Re-applying every sweep is what makes a rotated secret and a hand-edited event set heal on the same pass.

## Authentication

Deliveries carry the `X-Gitlab-Token` header, compared against `WEBHOOK_SECRET` in constant time. Anything that does not match gets a 401 and is never parsed.

Rotating the secret is safe at any time. Change it, restart, and deliveries presenting the old value are refused until the sweep reaches their hook, which takes at most an hour.

## Event coverage

Hooks subscribe to every event type GitLab offers, not just the ones the catalog reads today. Turning one on later would mean editing every hook on the instance and silently missing that activity until the sweep ran, whereas leaving them all on costs nothing but deliveries the receiver ignores.

That includes the confidential issue and note variants. Work on a confidential issue is still work, and the app keeps only the record's identity and author, never its content.

## Taking them off

A retired deployment leaves its hooks behind, pointing at a URL that no longer answers. The `uninstall` subcommand removes them. See [Uninstalling](uninstall.md).
