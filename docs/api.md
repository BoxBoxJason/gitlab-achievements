# The API

GitLab shows which achievements somebody holds but has no notion of EXP, so this app is the only place that number exists. It is served read-only under `/api/v1/`, from the local database alone. No GitLab call sits on the data path, which means the API keeps answering while the instance it mirrors is down or rate-limiting.

## Endpoints

| Method | Path | Returns |
| --- | --- | --- |
| `GET` | `/api/v1/users/{ref}` | EXP total, criteria counters, and every tier earned |
| `GET` | `/api/v1/users/{ref}/exp` | The EXP total alone |
| `GET` | `/api/v1/leaderboard?limit=N` | Top N users by EXP (default 10, max 100) |

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

The leaderboard is the same summary shape with a rank:

```json
{
  "limit": 10,
  "entries": [
    { "rank": 1, "username": "alice", "gitlab_user_id": 42, "exp_total": 1350 },
    { "rank": 2, "username": "bob", "gitlab_user_id": 17, "exp_total": 940 }
  ]
}
```

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
