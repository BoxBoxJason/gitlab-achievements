# End-to-end catalog check

Unit tests pin the normalizers against payloads written from GitLab's documentation. They cannot tell you when GitLab starts sending something else, and that is how a criteria goes quiet on a newer release without a single test failing. `test/e2e` closes that gap: it boots a real GitLab, performs one of every activity the catalog tracks as two ordinary users, and reads each counter back through the app's own API.

## What a run checks

Each criteria is checked on four paths, every one against a database of its own:

| Phase | What fills the database | What it proves |
| --- | --- | --- |
| `webhook` | The server, receiving the hooks it registered itself | The live path parses and credits what GitLab actually delivers |
| `webhook+backfill` | A forced backfill over the `webhook` database | The two producers spell every dedup key the same way, so a read path re-reading a window webhooks already covered counts nothing twice |
| `backfill` | The `backfill` subcommand, on a fresh database | The Events API and Pipelines API translation |
| `reconcile` | The `reconcile` subcommand, after a backfill floored in 2099 that reads nothing | The reconciliation pass on its own |

Most expectations are exact, so a criteria that counts one action twice fails as surely as one that does not count it at all. Pushes and commits are floors, because GitLab adds pushes of its own (a merge is one, carrying every commit it brings in).

The criteria the Events API has no record of (jobs, deployments, emoji, resolved discussions, fast merges) are reported as `n/a` on the read paths. Night owl and early bird are not asserted, because whether they move depends on when the suite runs.

The run prints a criteria-by-phase table and writes it to `report.md`, and to the job summary in CI.

## Running it

Locally, with podman or docker and about 4 GiB of free memory:

```bash
test/e2e/run.sh                                               # gitlab-ee:19.5.1-ee.0
GITLAB_IMAGE=docker.io/gitlab/gitlab-ce:latest test/e2e/run.sh
KEEP=1 test/e2e/run.sh     # leave the container up afterwards
REUSE=1 test/e2e/run.sh    # run again against the container KEEP left
```

GitLab takes about five minutes to boot, and the suite about five more. The app's logs, the four databases and the report land in `e2e-artifacts/`.

In CI, the **End-to-end** workflow runs the same script. It is manual only (Actions → End-to-end → Run workflow), and it takes the GitLab image as an input, so checking a new release before you upgrade to it means typing its tag.

## How it gets an instance this far

- **One admin token.** `run.sh` mints it with one `gitlab-rails runner` call. The suite acts as each user through that token's `sudo` scope, which is what puts the user, not the admin, on every payload. It mints a second, `read_api`-only token for the app, which refuses to be given one credential for both roles.
- **Project hooks.** `HOOK_SCOPE=project` works on every tier, so the run does not depend on a license. The flip side is that group hooks, which a Premium/Ultimate instance gets by default, are not exercised.
- **A runner played by the suite.** Pipelines get their outcomes from the runner API (`POST /jobs/request`, `PUT /jobs/:id`), which keeps the run to one container and makes the pass and the failure exact.
- **Application settings are not cached.** The suite allows hooks to the local network through the API, and GitLab otherwise keeps serving the old value for a minute, long enough for every delivery of the run to be blocked.
- **GitLab's memory watchdog is off.** In the single-process Puma mode used to fit a CI runner, the watchdog restarts all of GitLab once it passes ~1.6 GB, which a run does.

## Resolved discussions on GitLab 19.5

Resolving a thread on 19.5 sends no note hook. When it resolves the merge request's last open thread, it sends a single merge request hook with `action: update` and empty `changes`, which names neither the thread nor who resolved it. When other threads stay open, it sends nothing at all.

So the app reads the merge request's discussions through the API, and counts every resolved one for its resolver, on two occasions: such a bare update, and the merge request being merged or closed. A resolution that left threads open is counted at the latest when the merge request ends, dated by when it happened. The suite resolves a thread while another stays open and asserts it on the webhook phases, which is what keeps this honest if a later release changes what it sends.
