# How it works

One process, one database, and a GitLab instance it mirrors. Here is what it does from the moment you start it.

## The lifecycle

### 1. Bootstrap

Every start begins the same way. The app checks that both tokens work and have the access they need, creates or updates the 275 achievement definitions in your achievements group, and registers the webhooks that will feed it, pointed at `PUBLIC_URL` plus its ingestion path.

Bootstrap is all-or-nothing. If a token has lost a permission or GitLab rejects a mutation, startup fails rather than serving in a half-working state. Every problem it finds is reported in one pass, so you fix them together instead of one restart at a time. Nothing answers on the HTTP port until bootstrap finishes, which on a Free/CE instance with thousands of projects can take several minutes.

Achievements the group already holds are adopted rather than created again. Which GitLab achievement belongs to which catalog entry is recorded in the database and nowhere else, so a database rebuilt from nothing — or restored from a backup older than the achievements it describes — has no idea that 275 of them are already sitting in the group. Bootstrap lists what is there and matches by name, takes over what it finds, and pushes anything that has since drifted from the catalog. Without that, every one of those entries would be created a second time and GitLab would refuse the lot as names already taken.

Only one process bootstraps a database at a time. Two passes running together would both conclude that nothing had been created yet and both try to create it. A lease row makes the second one wait, and by the time it runs it finds the first one's work and adopts it. That is what lets a deployment run its historical walk as a separate workload without timing its start against the server's.

### 2. Historical backfill

Activity happened before you installed this. The app walks every group-owned project it can see and replays that history through the same rules live events go through, so a five-year-old instance does not start everyone at zero.

By default this runs in the background after bootstrap, once, and resumes where it left off if the process restarts. See [Historical backfill](backfill.md).

### 3. Live events

From then on it reacts to webhook deliveries as they arrive. Each one is authenticated against your webhook secret, acknowledged immediately, and evaluated by background workers, so a slow database never makes GitLab mark the hook as failing.

### 4. Self-healing, hourly

Bootstrap's checks are not a one-time thing. Roughly every hour the app re-applies every hook's configuration, picks up groups and projects created since the last sweep, re-checks every achievement against GitLab's own record of it — recreating one that was deleted, adopting one whose ID the database has lost track of, and pushing back a name, description or avatar that was edited out of band — and retries awards GitLab has not confirmed. Failures are logged and retried on the next tick rather than crashing the process.

### 5. Activity reconciliation, daily

Webhooks are best-effort. GitLab retries a delivery a few times and then gives up, and nothing in the live path notices, because a delivery that never arrives leaves no trace. Once a day the app re-reads the last couple of days of activity through the Events API and replays it. Anything already counted is discarded. See [Reconciliation](reconciliation.md).

### 6. Serving

Everything it works out is available over a read-only HTTP API: a user's EXP, the progress behind it, and a leaderboard. See [The API](api.md).

## Why two tokens

GitLab's token scopes are coarse. You get `read_api` (read everything, write nothing) or `api` (read and write everything). There is no "read everything, plus create these two kinds of object" in between, so one token cannot be both broadly read-only and narrowly write-capable.

This app splits the difference by role rather than by scope:

- **The read token** (`read_api`) belongs to an account with read access across whatever you want tracked. Every data fetch goes through it: the backfill, event enrichment, the reconciliation passes.
- **The write token** (`api`) belongs to a service account that is Maintainer or Owner on the achievements group and an instance Administrator. It is only ever used for a small set of calls: creating and awarding achievements, and managing webhooks.

The instance-admin requirement is not about achievements. It is what lets the app enumerate every group and project and manage hooks on ones it is not otherwise a member of. That token is as sensitive as your instance's root password, and [GitLab-side setup](gitlab-setup.md#4-create-the-write-credential) spells out what trusting it to a deployment means.

## Where state lives

All of it goes in the app's own SQL database: achievement progress, award history, backfill cursors, reconciliation watermarks, the processed-event log. PostgreSQL, SQLite, MySQL/MariaDB and SQL Server all work, selected from the DSN scheme.

Nothing is stored on the GitLab side except the achievements and the awards themselves. Your instance never does extra work to serve this app beyond normal API and webhook traffic, and the read API keeps answering while GitLab is down or rate-limiting, because no GitLab call sits on the data path.

The schema is created and migrated at startup, so an empty database is all you have to provide.

## Run one of them

The app is a singleton. One process registers the instance's webhooks, runs the hourly sweeps, walks history, and re-reads recent activity daily. A second replica would do all four again against the same GitLab, with two writers racing over the same rows for no benefit.

It is not a high-availability workload and does not need to be. Deliveries GitLab cannot make are retried on its side, and anything lost for good is what the daily reconciliation is for. Restarting costs you nothing.

## What GitLab does not let it do

Two limits shape more of this design than anything else, and both are GitLab's, not ours:

**An award is invisible until its recipient accepts it, and only the recipient can accept it.** This app can award and revoke. It cannot decide what anybody's profile shows, and it cannot suppress the email each award sends. That is why only the top tier of a criteria is ever pushed, instead of all eleven.

**Awarding is not idempotent.** GitLab has no uniqueness constraint on (achievement, user), so awarding the same badge three times produces three awards and three emails. Award delivery reads back what GitLab already holds for a user instead of retrying blind.

Both were verified against a live instance rather than read off the docs. The findings, and how to re-run them, are in [GitLab achievements API behavior](achievements-api-behavior.md).
