# The API

GitLab shows which achievements somebody holds but has no notion of EXP, so this app is the only place that number exists. It is served read-only under `/api/v1/`, from the local database alone. No GitLab call sits on the data path, which means the API keeps answering while the instance it mirrors is down or rate-limiting.

## Endpoints

| Method | Path | Returns |
| --- | --- | --- |
| `GET` | `/api/v1/users/{ref}` | EXP total, criteria counters, and every tier earned |
| `GET` | `/api/v1/users/{ref}/exp` | The EXP total alone |
| `GET` | `/api/v1/leaderboard` | Users ranked by EXP, paged, for all time or over a period. See [Rankings](#rankings) |
| `GET` | `/api/v1/leaderboard/criteria/{criteria}` | Users ranked by one criteria's counter, with the tier each has reached |
| `GET` | `/api/v1/achievements` | The catalog: every criteria, its category, and every tier with its threshold and EXP |
| `GET` | `/api/v1/achievements/{criteria}/icon` | The PNG every tier of a criteria shares |
| `GET` `POST` `DELETE` | `/api/v1/me/opt-out` | The caller's own opt-out: read it, opt out, opt back in. Needs `API_AUTH=gitlab`, see [Who takes part](participation.md#opting-out) |

Users who are [left out](participation.md) (bots, accounts the operator listed, people who opted out) are absent from every endpoint: not ranked, and a `404` when looked up.

`{ref}` is either a numeric GitLab user ID or a username. An all-digit ref is tried as an ID first and falls back to a username, so an all-numeric username still resolves unless it collides with a real user ID. Usernames resolve through whatever this app last saw, so somebody who was renamed on GitLab is still found under their current name.

A `404` means this app has recorded no activity for that user at all. A user it knows who has simply earned nothing is a `200` with `"exp_total": 0`. Those are different answers and they are reported differently.

## Responses

```console
$ curl -s https://achievements.example.com/api/v1/users/alice/exp
{"username":"alice","gitlab_user_id":42,"exp_total":1350}
```

The full record adds the counters behind the total and the tiers earned off them:

```json
{
  "username": "alice",
  "gitlab_user_id": 42,
  "exp_total": 1350,
  "counters": [
    { "criteria_key": "commits", "count": 812 },
    { "criteria_key": "merge_requests_merged", "count": 57 }
  ],
  "awards": [
    {
      "awarded_at": "2026-03-14T09:21:04Z",
      "criteria_key": "commits",
      "name": "Committer VII",
      "description": "Author 1000 commits.",
      "status": "accepted",
      "tier": 7,
      "threshold": 1000,
      "exp_reward": 500,
      "shown_on_profile": false
    }
  ]
}
```

`counters` and `awards` are always encoded, as `[]` rather than `null` when empty, so you can index into them without a nil check.

Awards are reported whatever their delivery status, which matches how EXP is totalled: a tier is earned the moment the engine says so, and a `superseded` tier still pays even though it is not what GitLab displays. `status` and `shown_on_profile` both appear because they answer different questions. `status` is how far this app got pushing the award; `shown_on_profile` is whether the recipient accepted it, which [only they can do](achievements-api-behavior.md).

The full record also carries `rank`, the user's position on the all-time leaderboard.

## Rankings

`/api/v1/leaderboard` is the summary shape with a rank, one page at a time:

```json
{
  "period": "all",
  "limit": 10,
  "offset": 0,
  "total": 57,
  "entries": [
    { "rank": 1, "username": "alice", "gitlab_user_id": 42, "exp_total": 1350 },
    { "rank": 2, "username": "bob", "gitlab_user_id": 17, "exp_total": 940 }
  ]
}
```

| Parameter | Default | Meaning |
| --- | --- | --- |
| `limit` | `10` | Page size, 1 to 100 |
| `offset` | `0` | How many ranks to skip. `total` says how many there are; past the end is an empty page, not an error |
| `period` | `all` | `all`, `day`, `week`, `month`, `year`: rank by EXP earned in the last 24 hours, 7, 30 or 365 days |
| `since` | | An RFC 3339 timestamp to rank from instead of a named period. Give one or the other |

Ties are broken by GitLab user ID, so pages never overlap or skip anyone.

Over a period, each entry gains `exp_earned`, the EXP of the tiers earned inside it, which is what the ranking is by. `exp_total` stays the all-time figure. Only users who earned something in the period are ranked, and the response carries `since`:

```json
{
  "period": "week",
  "since": "2026-10-06T20:41:07Z",
  "limit": 10, "offset": 0, "total": 3,
  "entries": [
    { "rank": 1, "username": "bob", "gitlab_user_id": 17, "exp_total": 940, "exp_earned": 300 }
  ]
}
```

Periods are rolling rather than calendar weeks or months, which would start on a different day depending on where the reader is. A tier counts toward the period of the activity that earned it, not the moment this app evaluated it. Tiers earned during the historical backfill therefore land back in history, not all in the week the app was installed.

`/api/v1/leaderboard/criteria/{criteria}` ranks by one counter, with `tier` the highest tier the count reaches (`0` for none yet):

```json
{
  "criteria_key": "commits",
  "limit": 10, "offset": 0, "total": 41,
  "entries": [
    { "rank": 1, "username": "bob", "gitlab_user_id": 17, "count": 1203, "tier": 8 }
  ]
}
```

It takes `limit` and `offset` but no period: counters keep running totals, not a history, so there is no telling how much of a count was earned in a given week. An unknown criteria is a `404`.

## The catalog

`/api/v1/achievements` lists what there is to earn, in the catalog's order, grouped into `git`, `merge_requests`, `issues`, `ci_cd`, `collaboration` and `engagement`:

```json
{
  "criteria": [
    {
      "criteria_key": "commits",
      "category": "git",
      "title": "Committer",
      "icon_url": "/api/v1/achievements/commits/icon",
      "tiers": [
        { "tier": 1, "name": "Committer I", "description": "Author 1 commits.", "threshold": 1, "exp_reward": 10 }
      ]
    }
  ]
}
```

Together with a user's `counters`, that is everything a page needs to show progress toward the next tier. The icon is served from the copy compiled into the binary, so a frontend never has to reach GitLab for it, and is cacheable for a day.

## Authentication

Off by default (`API_AUTH=none`), which is the posture `/healthz` already has, so upgrading does not lock anything and a deployment on a private network can opt out on purpose.

Set `API_AUTH=gitlab` and the mirrored instance becomes the identity provider. A caller presents any GitLab token, a personal access token or an OAuth access token, as `Authorization: Bearer <token>`, and it is checked against `GET /api/v4/user` before anything is served:

```console
$ curl -s -H "Authorization: Bearer $GITLAB_TOKEN" \
    https://achievements.example.com/api/v1/leaderboard
```

Browsers can log in at `/oauth/login` instead, which runs the standard authorization-code flow with PKCE against your instance and leaves an `HttpOnly` session cookie. `POST /oauth/logout` ends it.

Unless `OAUTH_CLIENT_ID` names an application you registered by hand, the app registers a public OAuth application for itself on startup, using the instance-admin write token it already holds, and adopts that same application on every later start rather than creating another. A public client has no secret to store; PKCE is what secures the exchange. Pass `OAUTH_CLIENT_SECRET` alongside a client ID to run as a confidential client.

Any authenticated GitLab identity may read anything the API serves. Achievements are already public on GitLab profiles and are social by nature. What authentication closes is an anonymous caller enumerating who exists on your instance.

> [!NOTE]
> With `API_AUTH=gitlab`, credentials are verified against GitLab on every request, with no cache. Revoking a token takes effect immediately, but it also means authenticated requests cannot be served while your instance is unreachable. The database-only guarantee above applies to the data; under the default `API_AUTH=none` it applies to the whole request.

## The other endpoints

Everything is served on one port, `LISTEN_ADDR`.

| Path | Purpose |
| --- | --- |
| `/healthz` | Liveness. Answers 200 as soon as the process serves at all |
| `/readyz` | Readiness. 200 once bootstrap has finished and the database and GitLab are reachable right now |
| `/webhooks/gitlab` | Where GitLab's deliveries arrive. `POST` only, authenticated with the webhook secret |
| `/api/v1/...` | The read API |
| `/oauth/...` | Browser login flow, when `API_AUTH=gitlab` |

`/healthz` and `/readyz` are unauthenticated by design.

Bootstrap runs before the server listens, so nothing answers during it. On a Free-tier instance with thousands of projects that can be several minutes, so give your health checks room: a startup probe on Kubernetes, a generous `start_period` anywhere else.
