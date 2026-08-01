# GitLab achievements API: confirmed behavior

Findings from the research spike in [issue #9](https://github.com/BoxBoxJason/gitlab-achievements/issues/9), verified on a throwaway self-hosted instance rather than taken from the docs, which are ambiguous on the point that matters most.

**Verified against GitLab CE 19.2.1** (revision `f4d029d2da8`), by GraphQL introspection, live mutations from three different identities (instance admin, the recipient, anonymous), and reading GitLab's own source in the running image. Every achievement mutation is still marked **Experiment** in 19.2, so none of this is contractually stable. Re-run the checks below on a new major before assuming it still holds.

## The short version

An awarded achievement is **invisible until its recipient accepts it**, and **only the recipient can accept it**. This app can award and revoke. It cannot decide what anybody's profile shows.

## 1. Awards land hidden, and that is the acceptance flow

`achievementsAward` returns `showOnProfile: false`. It is not a default this app can override: the column is `show_on_profile boolean DEFAULT false NOT NULL`, and `AchievementsAwardInput` has no visibility field at all.

```
AchievementsAwardInput
  achievementId: AchievementsAchievementID (required)
  userId:        UserID                    (required)
  awardMessage:  String                    (optional)
  clientMutationId: String                 (optional)
```

So the "acceptance" the docs allude to is real, and it is exactly this flag. There is no separate acceptance object, no pending state, and no acceptance-related field anywhere on `UserAchievement`:

```
UserAchievement: achievement, awardMessage, awardMessageHtml, awardedByUser,
                 createdAt, id, priority, revokedAt, revokedByUser,
                 showOnProfile, updatedAt, user
```

There is nothing to poll for and nothing to track beyond `showOnProfile` itself.

## 2. Only the recipient can accept, and there is no admin override

`Achievements::UserAchievementPolicy`:

```ruby
condition(:user_is_recipient) { @subject.user == @user }

rule { user_is_recipient }.enable :update_owned_user_achievement
rule { can?(:update_owned_user_achievement) }.enable :update_user_achievement
```

Verified against the live instance, all three cases:

| Caller | `userAchievementsUpdate` | `userAchievementPrioritiesUpdate` |
| --- | --- | --- |
| Instance admin who awarded it | denied | denied |
| Instance admin with `Sudo: <recipient>` | denied (GraphQL ignores `Sudo`; `currentUser` stayed `root`) | denied |
| The recipient's own token | works | works |

This is what settles the tiering question the issue opened with. **This app cannot hide a superseded tier, cannot reveal one, and cannot order what a profile shows.** Wrapping either mutation on `WriteClient` would only offer calls the write token can never make, so neither is wrapped. See the note in `internal/gitlabclient/achievements.go`.

The knock-on effect is that the "brief flash of every tier during backfill" the issue worried about cannot happen: nothing is ever visible unless its recipient went and accepted it.

## 3. The 30 days is the email link, not the award

Awarding sends the recipient an email (unless they cleared the `achievements_enabled` user preference, which defaults on) containing an "Accept this achievement" link. That link is a signed global ID:

```ruby
token = @user_achievement.signed_id(purpose: :achievement_action, expires_in: 30.days)
```

So the award never expires and never needs re-issuing. The one-click acceptance link expires after 30 days. The `AwardedAchievementsController#accept` action it points at does nothing but `update!(show_on_profile: true)`.

After 30 days the recipient can still accept, but only by calling `userAchievementsUpdate` themselves: GitLab 19.2 ships **no UI** for it. The profile bundle only ever queries `userAchievements`, and no compiled asset references `userAchievementsUpdate` or `showOnProfile`.

## 4. Awarding is not idempotent

`Achievements::AwardService#execute` calls `UserAchievement.create` unconditionally. There is no uniqueness constraint on (achievement, user). Awarding the same achievement to the same user three times produced three separate user-achievements, and three emails.

This is the sharpest operational hazard in the whole API, because retrying a failed award is the natural thing for a reconciliation loop to do. Any award that reached GitLab but whose response this app failed to record would be awarded again on the next pass. `ReconcileAwards` therefore reads what GitLab already holds for a user (one paginated query, only when it has work for them) and matches against it instead of re-awarding blind.

## 5. What the awarding token *can* do

- **Revoke**: `achievementsRevoke` is allowed for whoever can award, needs only the user-achievement ID, sends no email, and takes the badge off the profile even when the recipient had accepted it. Revoked awards drop out of `userAchievements` entirely.
- A revoked award cannot be un-revoked, and revoking it twice errors. The way back is a fresh award, under a new ID.
- **List**: `user.userAchievements(includeHidden: true)` returns unaccepted awards to a caller entitled to see them (the recipient, a namespace maintainer or owner, or an instance admin). Without `includeHidden`, a freshly awarded achievement is not listed at all. This is what lets the app recover user-achievement IDs it never stored.
- `achievement.userAchievements` (via `ListAchievementRecipients`) is the same data keyed the other way, and is likewise admin-visible.

## What this app does with all of it

**Only the top tier a user has reached in a criteria is ever pushed to GitLab.** The catalog stacks eleven tiers across twenty-five criteria. The engine still records every tier a user reaches, and every tier still pays its EXP, but delivery pushes one per criteria. On promotion the superseded tier is revoked and its row marked `superseded`.

This is the same outcome the issue asked for ("only the highest earned tier of each criteria should be visible"), reached through what the app controls (what it awards) rather than through `showOnProfile`, which it does not control. It also settles the question the issue raised about ordering the two mutations around a crash: there is no visibility mutation to order, and award-then-revoke crashing in the middle leaves the old tier live for one reconciliation pass, never zero tiers.

The reason it matters beyond profile tidiness is the email. One award is one email, and nothing about awarding is batched, so a first backfill over a mature instance would otherwise notify every user roughly a hundred times.

Two columns on `Award` carry the rest:

- `GitLabUserAchievementID`, the ID GitLab assigned. Without it an award can never be revoked, and delivery cannot be retried safely.
- `ShownOnProfile`, read back from GitLab and never written to it. The only record of whether a recipient ever accepted anything.

`AwardStatusAccepted` predates this spike and means "GitLab took the awarding mutation", which is *not* the recipient's acceptance. `ShownOnProfile` is that.

Supersession is a decision every reconciliation pass re-makes from the awards a user holds now, not a one-way door. A catalog retune that drops a criteria's top tiers renumbers the stack in place (`catalog.Template.Expand` derives `Tier` from `index - MinTier + 1`), which can leave a tier this app already withdrew as the highest one somebody holds. That tier goes back to GitLab on the next pass, under a fresh ID, because a revoked award cannot be un-revoked.

## Open follow-ups

- Nothing surfaces a pending acceptance to the user beyond GitLab's own email. `AwardAchievementOptions.AwardMessage` is unused and would land in that email, the one place this app can say something in its own words.

## Reproducing

```bash
podman run -d --name gl-spike --hostname localhost -p 8929:8929 --shm-size 256m \
  -e GITLAB_OMNIBUS_CONFIG="external_url 'http://localhost:8929'; \
     gitlab_rails['initial_root_password']='<a random one, not a dictionary word>'; \
     puma['worker_processes']=2; prometheus_monitoring['enable']=false; \
     registry['enable']=false; gitlab_kas['enable']=false" \
  docker.io/gitlab/gitlab-ce:latest
```

It answers on `127.0.0.1:8929` after roughly five minutes (`localhost` may resolve to `::1`, which the published port does not cover). Mint an admin token with `gitlab-rails runner`, then:

```bash
LIVE_GITLAB_URL=http://127.0.0.1:8929 LIVE_GITLAB_TOKEN=<token> \
  go test ./internal/bootstrap/ -run TestLiveAwardDelivery -v
```

That test asserts the delivery behavior this document describes against the real API. The policy and mailer quoted above are readable in the running container under `/opt/gitlab/embedded/service/gitlab-rails/app/{policies,services,mailers}/`.

Note that GitLab serves a full canned schema dump for any introspection query, so `__type(name: ...)` returns the whole schema. Filter it client-side rather than expecting a narrow response.
