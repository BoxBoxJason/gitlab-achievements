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
| `GET` | `/api/v1/me` | Who is signed in, and their record. See [Who is signed in](#who-is-signed-in) |
| `GET` | `/api/v1/instance` | The GitLab instance this deployment mirrors, and the version serving it. See [The instance](#the-instance) |
| `GET` `POST` `DELETE` | `/api/v1/me/opt-out` | The caller's own opt-out: read it, opt out, opt back in. Needs `API_AUTH=gitlab`, see [Who takes part](participation.md#opting-out) |

Users who are [left out](participation.md) (bots, accounts the operator listed, people who opted out) are absent from every endpoint: not ranked, and a `404` when looked up.

`{ref}` is either a numeric GitLab user ID or a username. An all-digit ref is tried as an ID first and falls back to a username, so an all-numeric username still resolves unless it collides with a real user ID. Usernames resolve through whatever this app last saw, so somebody who was renamed on GitLab is still found under their current name.

A `404` means this app has recorded no activity for that user at all. A user it knows who has simply earned nothing is a `200` with `"exp_total": 0`. Those are different answers and they are reported differently.

## Responses

```console
$ curl -s https://achievements.example.com/api/v1/users/alice/exp
{"username":"alice","avatar_url":"https://gitlab.example.com/uploads/-/system/user/avatar/42/avatar.png","gitlab_user_id":42,"exp_total":1350}
```

`avatar_url` is the avatar GitLab last reported for the user on any of their activity, as GitLab gave it, and is on every response naming a user, rankings included. It is `""` until a payload carrying one has arrived. The image lives on GitLab (or wherever GitLab says, such as Gravatar), so loading it is between the browser and GitLab.

The full record adds the counters behind the total and the tiers earned off them:

```json
{
  "username": "alice",
  "avatar_url": "https://gitlab.example.com/uploads/-/system/user/avatar/42/avatar.png",
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

Ties are broken by GitLab user ID, so the order is total and pages of an unchanged ranking never overlap or skip anyone. A ranking that changes between two requests, because somebody earned a tier, can shift people across a page boundary, as any paged list over live data can.

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

A named period is recomputed from the current time on every request, so the window slides between page 1 and page 2, and an award near its start can fall out in between. To page through one fixed window, take `since` from the first page and pass it, in place of `period`, for the pages after. Those come back labelled `"period": "custom"`.

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
        { "tier": 1, "name": "Committer I", "description": "Author your first commit.", "threshold": 1, "exp_reward": 10 }
      ]
    }
  ]
}
```

Together with a user's `counters`, that is everything a page needs to show progress toward the next tier. The icon is served from the copy compiled into the binary, so a frontend never has to reach GitLab for it. It is cacheable for a day and carries an `ETag` for revalidation after that.

The icon is the one route under `/api/v1/` served without authentication, even with `API_AUTH=gitlab`. Pages show it with an `<img>` tag, which cannot send a bearer token. The images are the same in every build and already appear on GitLab profiles, so they reveal nothing about your instance.

## Authentication

Off by default (`API_AUTH=none`), which is the posture `/healthz` already has, so upgrading does not lock anything and a deployment on a private network can opt out on purpose.

Set `API_AUTH=gitlab` and the mirrored instance becomes the identity provider. A caller presents a GitLab personal access token, or an OAuth access token this app's own OAuth application issued, as `Authorization: Bearer <token>`, and it is checked against `GET /api/v4/user` before anything is served. OAuth tokens issued to any other application on the instance are refused with `401`, even though GitLab itself would accept them: otherwise every other service that signs people in with GitLab could replay their tokens here and act as them:

```console
$ curl -s -H "Authorization: Bearer $GITLAB_TOKEN" \
    https://achievements.example.com/api/v1/leaderboard
```

Browsers can log in at `/oauth/login` instead. That runs the standard authorization-code flow with PKCE against your instance and leaves an `HttpOnly`, `SameSite=Lax` session cookie. Behind an `https` `PUBLIC_URL` it is named with the `__Host-` prefix, so only this app's own origin can set it. `POST /oauth/logout` ends it and revokes the session's GitLab tokens, as does logging in again over an existing session.

Unless `OAUTH_CLIENT_ID` names an application you registered by hand, the app registers a public OAuth application for itself on startup, using the instance-admin write token it already holds. On every later start it adopts that same application rather than creating another. A public client has no secret to store; PKCE is what secures the exchange. Pass `OAUTH_CLIENT_SECRET` alongside a client ID to run as a confidential client.

### Sessions

GitLab access tokens last two hours. A session keeps the refresh token GitLab issues with the access token and renews it shortly before it runs out, so nobody is sent back to GitLab mid-visit. Requests that arrive together on an expiring session share one renewal; GitLab refresh tokens work only once. A session lasts `SESSION_LIFETIME` (seven days by default), after which the browser logs in again. If GitLab refuses a renewal (the user revoked the application, or the token was used elsewhere), the session ends with a `401`. If GitLab instead rejects this app's own OAuth client (a client secret rotated on GitLab but not here), sessions are kept: requests keep working on their current token until it expires and then get `503`, the log reports the misconfiguration, and the sessions resume once the credentials are fixed.

The session table holds neither the cookie nor a usable token. Rows are keyed by the SHA-256 of the cookie's value, and both tokens are encrypted with AES-256-GCM under a key derived from `SESSION_SECRET`, which the app refuses to start without when `API_AUTH=gitlab`. A database dump or backup therefore hands out no working credentials. Changing the secret signs everybody out.

### Coming back to your page

`/oauth/login?return_to=<destination>` sends the browser to `destination` once it is logged in. Without it, the browser lands on the user's own page in the built-in frontend, at the root of the frontend `FRONTEND_DIR` serves, or on their record in this API when the binary has no frontend. Two kinds of destination are accepted:

- a path on this app, such as `/rankings?period=week`;
- an absolute URL on `PUBLIC_URL`'s origin, or on one listed in `OAUTH_RETURN_ORIGINS`.

Anything else is refused with a `400` before the browser goes to GitLab, so the login cannot be used as an open redirect. That includes other hosts, `//host`, `/\host`, and `javascript:` URLs.

### Who is signed in

`GET /api/v1/me` is what a frontend calls on load:

| Answer | Meaning |
| --- | --- |
| `200` | Signed in. The body has `username`, `avatar_url`, `gitlab_user_id`, `record` (the same document as `/users/{ref}`, or `null` for somebody with nothing recorded yet or who is [left out](participation.md)), and `participation` |
| `401` | Not signed in: show a login link to `/oauth/login` |
| `404` | This deployment has no sign-in (`API_AUTH=none`) |

### The instance

`GET /api/v1/instance` describes the deployment rather than anybody's record, for a frontend that links names to GitLab profiles:

```json
{ "gitlab_url": "https://gitlab.example.com", "version": "v0.5.0" }
```

It sits behind the same authentication as the rest of the API.

### Restricting who may sign in

Any authenticated GitLab identity may read anything the API serves by default. The one route that needs no credential is the achievement icon, see [The catalog](#the-catalog). Achievements are already public on GitLab profiles and are social by nature. What authentication closes is an anonymous caller enumerating who exists on your instance.

Set `API_ALLOWED_GROUP` to a group's full path to admit only its members, including members inherited from parent groups. Instance administrators are always admitted. Anyone else gets a `403`, at login and on every API call. Membership is checked with the read token, so that token must be able to see the group.

### Caching

A verified credential is trusted for `API_AUTH_CACHE_TTL` (one minute by default) before GitLab is asked again, so a page making several calls costs one GitLab round-trip, not several. Only successes are cached; a rejected token is checked again every time. The cost is that a token revoked on GitLab, or a user removed from the allowed group, keeps working here for up to that long. Logging out drops the session's entry at once. Set `API_AUTH_CACHE_TTL=0s` to verify every request.

> [!NOTE]
> With `API_AUTH=gitlab`, a request can only be served if GitLab could be asked about its credential within the cache window. The database-only guarantee above applies to the data; under the default `API_AUTH=none` it applies to the whole request.

### Cross-site requests

State-changing requests (`POST`, `DELETE`) are checked for cross-site request forgery: a browser request from another origin is refused with `403`. Scripts using a bearer token send no such headers and are unaffected.

A frontend that uses the session cookie should be served from the same origin as the API, as the built-in one is. See [The frontend](frontend.md).

## The other endpoints

Everything is served on one port, `LISTEN_ADDR`.

| Path | Purpose |
| --- | --- |
| `/healthz` | Liveness. Answers 200 as soon as the process serves at all |
| `/readyz` | Readiness. 200 once bootstrap has finished and the database and GitLab are reachable right now |
| `/webhooks/gitlab` | Where GitLab's deliveries arrive. `POST` only, authenticated with the webhook secret |
| `/api/v1/...` | The read API |
| `/oauth/...` | Browser login flow, when `API_AUTH=gitlab` |
| `/` | The web frontend, or the one `FRONTEND_DIR` names. See [The frontend](frontend.md) |

`/healthz` and `/readyz` are unauthenticated by design.

Bootstrap runs before the server listens, so nothing answers during it. On a Free-tier instance with thousands of projects that can be several minutes, so give your health checks room: a startup probe on Kubernetes, a generous `start_period` anywhere else.
