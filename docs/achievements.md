# Achievements and EXP

The catalog is 25 criteria, eleven tiers each, 275 GitLab achievements in total. It is generated from templates in `internal/catalog` rather than written out by hand, so adding a criteria is one template and nothing else.

## The catalog

| Category | Criteria |
| --- | --- |
| **Git** | commits, pushes, branches created, tags created |
| **Merge requests & review** | opened, merged, merged within an hour of opening, approved, closed without merging, comments left, review discussions resolved |
| **Issues** | opened, closed |
| **CI/CD** | pipelines run, passed, failed, jobs run, deployments, deployments succeeded |
| **Collaboration** | emoji reactions given, wiki pages created |
| **Engagement** | days active, longest activity streak, night owl days, early bird days |

Tiers are numbered in Roman numerals off a shared title: `Committer I` through `Committer XI`, `Firefighter I` through `Firefighter XI`. Every tier of a criteria shares one avatar, because eleven pieces of art per criteria would be eleven reasons never to ship a new one. Icons come from the VS Code extension's set where a criteria matches; the rest ship without one for now.

## Thresholds

Four difficulty curves, all built from powers of 2, 5 and 10 so tiers land on numbers people can read:

| Curve | Thresholds | Used for |
| --- | --- | --- |
| Hard | 1, 5, 10, 50, 100, 500, ... 100,000 | Things you rack up steadily over months (commits, issues, deployments) |
| Medium | 2, 10, 20, 100, 200, ... 200,000 | Things that come in bursts (comments, pipelines) |
| Infernal | 1, 3, 5, 10, 50, 100, ... 50,000 | Things most people do a handful of times and a few people do constantly (tags, approvals, wiki pages) |
| Steps | 1, 2, 3, 5, 7, 14, 30, 60, 100, 180, 365 | The activity streak, where the meaningful numbers are a week, a month, a year |

The top tiers are out of reach on most instances, and that is the point. They exist so the one person who has been committing to the same GitLab since 2015 still has something left to earn.

The catalog is compiled in rather than configurable per deployment. Thresholds are only half the contract: the other half is 275 achievement objects that already exist on your instance with awards pointing at them, so a configurable catalog is really a question about renaming and deleting live GitLab records. Worth its own issue, not v1's.

## EXP

Every tier is worth EXP on one curve shared by the whole catalog: **1, 3, 5, 10, 50, 100, 500, 1,000, 5,000, 10,000, 50,000**. The same tier pays the same whatever criteria earned it, so the easiest curve to climb is not also the most rewarding to farm.

A user's total is written to their row in the same transaction that records the award, so a crash cannot leave somebody holding a tier they were not paid for.

The total is derived, not accumulated. It is always recomputed as the sum of what the tiers a user holds are worth, which is what makes three otherwise awkward things safe: a backfill awarding tiers in arbitrary order, a catalog retune changing what an old tier pays, and withdrawing a superseded tier from GitLab. None of them can drift the number. When a bootstrap or reconciliation pass finds a tier's reward has changed, it re-derives the totals of everyone holding it on the spot.

EXP exists only in this app's database. A GitLab achievement is a flat object with a name, a description and an avatar. No tier field, no points, nowhere to put it.

## Keeping the catalog and GitLab in step

Every start, and again on the hourly sweep, the app lists the achievements in your achievements group and makes them match the catalog. Each catalog entry ends the pass bound to exactly one GitLab achievement, whichever route it takes to get there:

| What is found | What happens |
| --- | --- |
| No achievement under the entry's name | It is created, and the pairing recorded. |
| One under the entry's name the database has no row for | It is adopted: the pairing is recorded against the achievement that is already there. |
| The recorded achievement is gone, but one under the entry's name is there | The row is pointed back at it. |
| The recorded achievement is gone entirely | It is created again, under a new ID. |
| It is there, but its name, description or avatar has drifted | The catalog's version is pushed back over it. |

Adoption is what makes the app survive losing its database. Which GitLab achievement belongs to which catalog entry lives in that database and nowhere else — a GitLab achievement carries no field this app could stamp its own identity into — so a store rebuilt from scratch, or restored from a backup older than the achievements it describes, would otherwise try to create all 275 again and be refused: **an achievement's name is unique within a namespace**. Matching by name instead turns that from a wedged startup into a no-op.

It also keeps the badges. Deleting an achievement deletes every award of it, so an achievement recreated rather than adopted takes everyone's earned badge down with the original. Adoption never touches an award.

The one thing it cannot verify is an avatar's contents: GitLab exposes an avatar's URL but not its bytes. An adopted achievement that already shows one is taken to be showing the catalog's; one showing none gets the catalog's uploaded.

## What reaches GitLab

Only the top tier a user has reached in each criteria is ever pushed. Every tier below it stays recorded here and keeps paying its EXP. When a user is promoted, the new tier is awarded and the one it replaces is revoked, so a profile carries one badge per criteria rather than a run of eleven near-identical ones.

That split exists because GitLab draws the line somewhere unexpected. An awarded achievement is invisible until its recipient accepts it, only the recipient can accept it, and every award emails them. This app can award and revoke; it cannot decide what anybody's profile shows, and it cannot batch the notifications. Pushing every reached tier would mean emailing a long-serving user roughly a hundred times on the first backfill, to no visible end.

Awarding is also not idempotent on GitLab's side, so delivery matches against what GitLab already holds for a user instead of retrying blind. The full findings, and how to re-verify them on a throwaway instance, are in [GitLab achievements API behavior](achievements-api-behavior.md).

## Engagement criteria

Days active, streaks, night owl and early bird are derived from a per-user record of which days somebody was active, not from a running total. Two commits in one afternoon are one active day, and a streak can be extended by a day that arrives between two already-known ones. That also makes them independent of the order activity is observed in, which matters because the backfill walks project by project rather than in date order.

The streak awarded is the longest run a user ever managed, not their current one. GitLab awards are not revoked, so a criteria that can fall as well as rise would mean nothing after the first time it was reached.

## What is missing, and why

**Six criteria advance from live webhook deliveries only.** The Events API the backfill reads reports pushes, merge requests, issues and notes, but not deployments, jobs, emoji, wiki pages, resolved discussions or fast merges. Those start at zero on a freshly bootstrapped instance however long it has existed.

**Anything that needs to watch an editor is gone.** Lines of code, files created, per-language breakdowns, tabs, extensions, themes, debugger sessions, terminal tasks, time spent: the VS Code extension has all of them and a server has no equivalent for any of them.

**Two git criteria are missing for subtler reasons.** An amend is indistinguishable from an ordinary commit once pushed, and the Events API does not report whether a push was forced.

**Four event types the hooks subscribe to earn nothing**, for want of anyone to credit. Release and vulnerability payloads carry no user at all. Member payloads name the member rather than whoever added them. Feature flag payloads carry a user but no identifier for the change, so a second toggle of one flag cannot be told apart from a redelivery of the first. Milestone events have no payload type in the API client to parse.

The hooks stay subscribed to all of them, so adding a criteria later is a code change rather than a re-registration across every project on your instance.
