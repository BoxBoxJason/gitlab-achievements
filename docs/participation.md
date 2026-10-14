# Who takes part

Everyone active on the instance takes part by default, except bots. Three things can leave an account out: GitLab flagging it as a bot, the operator listing it, or the user opting out.

## What being left out means

An excluded user:

- has no activity counted from the moment they are excluded. Nothing about their activity is stored, not even that it happened;
- does not appear on the leaderboard, and is a `404` on `/api/v1/users/{ref}`, the same answer as somebody this app has never seen;
- has no further awards pushed to GitLab.

Nothing already on their GitLab profile is withdrawn. An award belongs to its recipient, who can hide it from their profile, and deleting it is not something this app does on their behalf. Their earlier counters and awards stay in this app's database, hidden, so that being let back in picks up where they left off.

## Bots

On by default (`EXCLUDE_BOTS=true`). GitLab flags as bots the users behind project and group access tokens, service accounts, and its own internal bots (alert, support, security, and the like). Left in, they earn achievements for every pipeline they trigger and every merge request a dependency updater opens, and top the leaderboard doing it.

The first time an account is seen, the app asks GitLab (`GET /api/v4/users/:id`) and remembers the answer, which never changes for a given account. While GitLab cannot be asked, the account is judged on its username. GitLab names its token users `project_<id>_bot_<hash>` and `group_<id>_bot_<hash>`, and those, along with the `ghost` user, are recognized without a lookup. The account is asked about again on its next activity.

At every start, a background sweep asks about every tracked user not yet classified, paced at `BACKFILL_RATE`. That covers everyone recorded before bot exclusion existed. Its progress shows as the `bot_sweep` job in the [metrics](metrics.md).

Accounts that run automation under a human-looking user (a shared `ci` account, say) are not flagged by GitLab. List them yourself.

## Operator exclusions

| Setting | What it does |
| --- | --- |
| `EXCLUDE_USERS` | Comma-separated usernames or numeric GitLab user IDs, e.g. `renovate,dependabot,1234`. Usernames match case-insensitively and may carry a leading `@`. An all-digit entry is matched as an ID and as a username |
| `EXCLUDE_USERNAME_PATTERN` | A regular expression ([RE2](https://github.com/google/re2/wiki/Syntax)) matched against usernames, e.g. `^svc-` or `-bot$`. Unanchored, so anchor it if you mean a prefix or suffix. An invalid pattern stops the app from starting |

These are applied to every tracked user at each start, before anything is served, so a newly listed account is off the leaderboard as soon as the app answers. Taking an account off the list lets it back in the same way.

## Opting out

With `API_AUTH=gitlab`, a signed-in user can leave themselves out, and come back:

```console
$ curl -s -X POST   -H "Authorization: Bearer $TOKEN" https://achievements.example.com/api/v1/me/opt-out
$ curl -s -X DELETE -H "Authorization: Bearer $TOKEN" https://achievements.example.com/api/v1/me/opt-out
$ curl -s           -H "Authorization: Bearer $TOKEN" https://achievements.example.com/api/v1/me/opt-out
{"opted_out_at":"2026-10-13T20:41:07Z","reason":"opted_out","opted_out":true,"excluded":true,"opt_out_allowed":true}
```

`reason` says why someone is excluded even when it is not their doing (`bot`, `configured`), so a user wondering why they are not ranked finds out.

Opting out is allowed by default (`ALLOW_OPT_OUT=true`). With it off, `POST` answers `403`, but `DELETE` still works, so nobody who opted out earlier is stuck that way. Activity from the time someone was opted out is not counted when they come back, except whatever the [reconciliation](reconciliation.md) look-back window still reaches.

Without authentication the API cannot tell who is asking, so the endpoints answer `404`. Use `EXCLUDE_USERS` instead.

Browsers are protected against cross-site request forgery on these endpoints: a cross-origin `POST` or `DELETE` from a browser is refused with `403`.
