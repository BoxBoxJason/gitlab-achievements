---
title: The frontend
description: "The built-in GitLab Achievements web frontend: leaderboards, personal progress toward the next tier, the catalog, and sign-in with GitLab."
weight: 40
---

The binary serves a web frontend at `/`: the leaderboard (all time, or over the last day, week, 30 or 365 days), each person's achievements and their progress toward the next tier, the catalog with a ranking for every achievement, and a page to opt out or back in. It looks and behaves like GitLab, in light and dark, because the people using it spend their day in GitLab.

It is compiled into the binary rather than shipped beside it, so a release always serves the frontend made for its own API. There is nothing to deploy or configure: open `PUBLIC_URL` in a browser.

## Signing in

With `API_AUTH=gitlab`, the frontend shows a "Sign in with GitLab" page until the browser has a session, and sends the person back to the page they asked for afterwards. With the default `API_AUTH=none`, there is no sign-in and everything is readable by whoever can reach the app. The participation page then explains that opting out goes through the operator.

`API_ALLOWED_GROUP` applies to the frontend as it does to the API: somebody outside the group signs in, is told the instance is restricted, and can sign out again.

## What it shows that GitLab does not

Achievements land on GitLab hidden until their recipient accepts them from the email GitLab sends, and [GitLab offers no other way to do that]({{< relref "/docs/achievements-api-behavior" >}}). On your own page, the frontend marks every top tier GitLab still holds hidden, so people know why their profile looks emptier than this page.

Avatars come from GitLab: the app records each person's avatar URL from the activity it already receives (webhooks, the historical backfill, reconciliation), so nobody's face costs a GitLab call. Somebody whose activity predates this feature shows GitLab's initial-on-a-tint placeholder until their next activity carries their avatar.

## Security headers

Every response from the built-in frontend carries a Content Security Policy that allows the app's own scripts, styles and fonts and nothing inline. Images may also come from the GitLab instance, or from any HTTPS host, because that is where GitLab says avatars live (Gravatar, a CDN). Framing is refused.

Its hashed assets (`/assets/*`) are cached for a year as immutable; `index.html` is never cached, so an upgrade is picked up on the next load.

## Working on it

The source is in [`web/`](https://github.com/BoxBoxJason/gitlab-achievements/tree/main/web): Vue 3, TypeScript, Tailwind CSS, built with Vite. GitLab's own fonts (`@gitlab/fonts`) and icons (`@gitlab/svgs`) come from npm and are bundled, so nothing loads from a third party at runtime.

```bash
make web/dev    # http://localhost:5173, against fixture data, no GitLab needed
```

The fixtures (`web/mock/`) serve the real catalog, the binary's own icons, and 57 made-up people. `MOCK_AUTH=signed-out` or `MOCK_AUTH=none` shows the other two ways a deployment can answer `/api/v1/me`.

Against a running app instead, `cd web && npm run dev` proxies `/api` and `/oauth` to `http://localhost:8080` (or `API_TARGET`), so the session cookie works as it does in production.

```bash
make web/test   # type check and unit tests
make build/all  # the frontend, then the binary embedding it
```

`make build` alone compiles whatever `web/dist` holds. On a fresh checkout that is only a placeholder, and the binary serves the API alone, logging that it was built without its frontend. That keeps `go install` and the Go-only CI jobs working without Node. The container image always builds both.

## Serving a different frontend

`FRONTEND_DIR` replaces the built-in frontend with a directory of your own: built static files with `index.html` at the root, the output of `npm run build` in most toolchains. It is served the same way:

- Files are served as they are; `index.html` is sent with `Cache-Control: no-cache` so a new build is picked up on the next load.
- Any other path with no file extension (`/users/alice`), or any path a browser navigates to as a page (`/users/first.last`), is answered with `index.html`, so client-side routing works on a deep link or a reload. A missing asset (`/assets/missing.js`) is a real `404`.
- Directories are never listed. Only `GET` and `HEAD` are answered.
- Nothing outside the directory is served: a symlink leading out of it is a `404`, and so is any path with a segment starting with a dot (`.env`, `.git/`).
- Responses carry `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` and `Referrer-Policy: strict-origin-when-cross-origin`. They also carry the built-in frontend's Content Security Policy (everything from this origin, images also from the GitLab instance and any `https:` source), which a build of the same frontend runs under unchanged. A frontend that needs more sets its own in `FRONTEND_CSP`, or `FRONTEND_CSP=off` to send none and set it at the ingress instead.
- The frontend can never shadow `/api/`, `/oauth/`, `/webhooks/gitlab`, `/healthz` or `/readyz`. Those always win, so do not give client-side routes those prefixes.

The app refuses to start if `FRONTEND_DIR` has no `index.html`, rather than serving 404s at `/`.

Your frontend should be served from the **same origin** as the API, which `FRONTEND_DIR` does for you. The session cookie is `HttpOnly` and `SameSite=Lax`, so a page on the same origin sends it with every `fetch`; a page on another origin would need CORS with credentials, which this app deliberately does not offer. State-changing calls (`POST`/`DELETE`, such as opting out) are refused when they come from another origin, which is the protection against cross-site request forgery.

You can also serve it yourself on the same hostname and route by path at the ingress: `/api/`, `/oauth/` and `/webhooks/` to this app, everything else to your frontend. Ingress controllers prefer the longest matching prefix, so the two do not fight.

### Signing in from your own frontend

```js
// On load: who is signed in?
const res = await fetch("/api/v1/me");
if (res.status === 401) showLoginLink("/oauth/login?return_to=" + encodeURIComponent(location.pathname));
if (res.status === 404) hideSignInEntirely(); // the deployment runs with API_AUTH=none
if (res.ok) render(await res.json());

// Signing out
await fetch("/oauth/logout", { method: "POST" });
```

After logging in, the browser comes back to `return_to`. Only paths on this app and URLs on allowed origins are accepted; see [Coming back to your page]({{< relref "/docs/api#coming-back-to-your-page" >}}). If your page is served from another host, list its origin in `OAUTH_RETURN_ORIGINS` so the login can return there. API calls from that host still will not carry the cookie, which is why the options above keep everything on one origin.

Sessions renew their GitLab token on their own and last `SESSION_LIFETIME`, a week by default. A frontend never deals with tokens, only with `/me` answering `401` once a session has ended.

For what to render, see [the API reference]({{< relref "/docs/api#rankings" >}}). `/api/v1/instance` gives the GitLab URL to link profiles to.
