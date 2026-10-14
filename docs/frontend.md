# Serving a frontend

This app has no web interface of its own, only the [API](api.md). A rankings page, or any other frontend you build on it, should be served from the **same origin** as the API, for example `https://achievements.example.com/` with the API under `/api/v1/` beside it.

Same origin is what lets the frontend use the login session as it is:

- The session cookie is `HttpOnly` and `SameSite=Lax`. A page on the same origin sends it with every `fetch` automatically. A page on another origin would need CORS with credentials, which this app deliberately does not offer.
- State-changing calls (`POST`/`DELETE`, such as opting out) are refused when they come from another origin. That is the protection against cross-site request forgery.

There are two ways to get there. Pick whichever fits how you deploy.

## Option 1: let the app serve it

Point `FRONTEND_DIR` at a directory of built static files with `index.html` at its root, the output of `npm run build` in most toolchains. The app serves it at `/`:

- Files are served as they are. Content-hashed assets are cacheable; `index.html` is sent with `Cache-Control: no-cache` so a new build is picked up on the next load.
- Any other path with no file extension (`/users/alice`, `/leaderboard/week`), or any path a browser navigates to as a page (`/users/first.last`), is answered with `index.html`, so client-side routing works on a deep link or a reload. A missing asset (`/assets/missing.js`) is a real `404`.
- Directories are never listed. Only `GET` and `HEAD` are answered.
- Responses carry `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` and `Referrer-Policy: strict-origin-when-cross-origin`. No Content Security Policy is set, because a sensible one depends on your frontend. Set it in your build or at the ingress.
- The frontend can never shadow `/api/`, `/oauth/`, `/webhooks/gitlab`, `/healthz` or `/readyz`. Those always win, so do not give client-side routes those prefixes.

The app refuses to start if `FRONTEND_DIR` has no `index.html`, rather than serving 404s at `/`.

On Kubernetes, the chart copies the files out of an image you build for the frontend, using an init container. Updating the frontend is then a matter of changing its tag:

```yaml
frontend:
  enabled: true
  image:
    repository: registry.example.com/achievements-rankings
    tag: "1.4.0"
  # Where index.html is inside that image. The default suits an image
  # built FROM nginx; the image only needs sh and cp.
  sourcePath: /usr/share/nginx/html
```

## Option 2: route paths at the ingress

Serve the frontend however you like (its own Deployment, an nginx, a static host behind a proxy) and route by path on one hostname. The API and the login flow go to this app, everything else to the frontend:

```yaml
# values.yaml for this chart
ingress:
  enabled: true
  hosts:
    - host: achievements.example.com
      paths:
        - { path: /api/, pathType: Prefix }
        - { path: /oauth/, pathType: Prefix }
        - { path: /webhooks/, pathType: Prefix }
```

Your frontend's own Ingress then claims `path: /` on the same host. Ingress controllers prefer the longest matching prefix, so the two do not fight.

## Signing in from the frontend

```js
// On load: who is signed in?
const res = await fetch("/api/v1/me");
if (res.status === 401) showLoginLink("/oauth/login?return_to=" + encodeURIComponent(location.pathname));
if (res.status === 404) hideSignInEntirely(); // the deployment runs with API_AUTH=none
if (res.ok) render(await res.json());

// Signing out
await fetch("/oauth/logout", { method: "POST" });
```

After logging in, the browser comes back to `return_to`. Only paths on this app and URLs on allowed origins are accepted; see [Coming back to your page](api.md#coming-back-to-your-page). If the frontend is served from another host, list its origin in `OAUTH_RETURN_ORIGINS` so the login can return there. API calls from that host still will not carry the cookie, which is why the options above put everything on one origin.

Sessions renew their GitLab token on their own and last `SESSION_LIFETIME`, a week by default. A frontend never deals with tokens, only with `/me` answering `401` once a session has ended.

For what to render, `/api/v1/leaderboard` (paged, all-time or `?period=week`), `/api/v1/leaderboard/criteria/{criteria}` and `/api/v1/achievements` (the catalog, with icons) are described in [the API reference](api.md#rankings).
