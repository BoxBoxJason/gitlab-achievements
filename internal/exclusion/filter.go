package exclusion

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// userBatchSize is how many users Apply and SweepBots load at a time.
const userBatchSize = 200

// ErrOptOutDisabled reports that the operator turned self-service opt-out
// off.
var ErrOptOutDisabled = errors.New("opting out is disabled on this instance")

// BotLookup asks GitLab whether an account is a bot.
type BotLookup interface {
	IsBot(ctx context.Context, gitlabUserID int64) (bool, error)
}

// Filter applies a Policy to the users this app tracks, persisting each
// decision on the user's row so the read path can filter in SQL.
type Filter struct {
	conn        *gorm.DB
	lookup      BotLookup
	policy      Policy
	allowOptOut bool
}

// NewFilter builds a Filter. lookup may be nil, in which case bots are
// recognized by username alone.
func NewFilter(conn *gorm.DB, policy Policy, lookup BotLookup, allowOptOut bool) *Filter {
	return &Filter{conn: conn, policy: policy, lookup: lookup, allowOptOut: allowOptOut}
}

// AllowsOptOut reports whether users may opt themselves out.
func (f *Filter) AllowsOptOut() bool {
	return f.allowOptOut
}

// Check decides whether the GitLab user behind an activity takes part.
//
// It must be called outside any transaction: the first time it sees an
// account it may ask GitLab whether it is a bot, and that call must not
// hold a database lock while it waits (on SQLite, the write lock every
// other writer is queued behind).
//
// A GitLab lookup that fails is not an error. The username pattern still
// applies, the account is left unchecked so the next activity asks again,
// and the activity is judged on what is known now: holding it back would
// stall every activity behind a GitLab outage.
func (f *Filter) Check(ctx context.Context, gitlabUserID int64, username string) (Reason, error) {
	if gitlabUserID <= 0 {
		// No GitLab user has such an ID, so there is no row to read or
		// write, but the username rules still apply to whatever the
		// activity claims to be: an ID is not a way past them.
		return f.policy.Decide(Subject{Username: username}), nil
	}

	user, err := f.load(ctx, gitlabUserID)
	if err != nil {
		return Included, err
	}

	subject := Subject{ID: gitlabUserID, Username: username}

	var checkedAt *time.Time

	if user != nil {
		subject.IsBot = user.IsBot
		subject.OptedOut = user.OptedOutAt != nil

		if subject.Username == "" {
			subject.Username = user.Username
		}
	}

	if f.needsLookup(user, subject) {
		isBot, lookupErr := f.lookup.IsBot(ctx, gitlabUserID)
		if lookupErr != nil {
			zap.L().Debug("could not ask gitlab whether a user is a bot, deciding by username for now",
				zap.Int64("gitlab_user_id", gitlabUserID),
				zap.Error(lookupErr),
			)
		} else {
			now := time.Now().UTC()
			checkedAt = &now
			subject.IsBot = isBot
		}
	}

	reason := f.policy.Decide(subject)

	return reason, f.record(ctx, user, subject, reason, checkedAt)
}

// Apply re-decides every tracked user against the current policy.
//
// The policy is configuration, so it can change between starts: a username
// added to the exclusion list has to drop off the leaderboard at once
// rather than at that user's next activity. Bots are judged here on what
// is already known about them; SweepBots fills in the rest.
func (f *Filter) Apply(ctx context.Context) (int, error) {
	changed := 0

	err := f.eachUser(ctx, f.conn.WithContext(ctx), func(user *db.User) error {
		reason := f.policy.Decide(subjectOf(user))
		if Reason(user.ExcludedReason) == reason {
			return nil
		}

		changed++

		return f.setReason(ctx, user.ID, reason)
	})

	return changed, err
}

// SweepBots asks GitLab about every tracked user it has not yet classified,
// so accounts recorded before bot exclusion existed, or while GitLab could
// not be asked, are caught without waiting for their next activity.
//
// It is a no-op when bots are not excluded or there is no lookup.
func (f *Filter) SweepBots(ctx context.Context) (int, error) {
	if !f.policy.ExcludeBots || f.lookup == nil {
		return 0, nil
	}

	excluded := 0

	unchecked := f.conn.WithContext(ctx).Where("bot_checked_at IS NULL")

	err := f.eachUser(ctx, unchecked, func(user *db.User) error {
		reason, err := f.Check(ctx, user.GitLabUserID, user.Username)
		if err != nil {
			return err
		}

		if reason == Bot {
			excluded++
		}

		return nil
	})

	return excluded, err
}

// OptOut records gitlabUserID's request to be left out, creating their row
// if this app has not seen them yet so that their first activity is already
// skipped.
func (f *Filter) OptOut(ctx context.Context, gitlabUserID int64, username string) error {
	if !f.allowOptOut {
		return ErrOptOutDisabled
	}

	now := time.Now().UTC()

	return f.setOptOut(ctx, gitlabUserID, username, &now)
}

// OptIn withdraws an earlier opt-out. Activity from the time in between was
// never recorded, so it stays uncounted unless reconciliation's look-back
// window still reaches it.
func (f *Filter) OptIn(ctx context.Context, gitlabUserID int64, username string) error {
	return f.setOptOut(ctx, gitlabUserID, username, nil)
}

// Status reports the recorded decision for gitlabUserID, and whether they
// have opted out. A user this app has never seen is judged by the policy on
// what is known of them, so an account the operator listed is reported
// excluded before its first activity, as it will be once that arrives.
func (f *Filter) Status(ctx context.Context, gitlabUserID int64, username string) (Reason, *time.Time, error) {
	user, err := f.load(ctx, gitlabUserID)
	if err != nil {
		return Included, nil, err
	}

	if user == nil {
		return f.policy.Decide(Subject{ID: gitlabUserID, Username: username}), nil, nil
	}

	return Reason(user.ExcludedReason), user.OptedOutAt, nil
}

// needsLookup reports whether GitLab should be asked about user.
//
// A user already left out for a reason that outranks being a bot (their
// own opt-out, the operator's list) is not asked about: GitLab's answer
// could not change the decision, and the lookup is made again should that
// reason ever lift.
func (f *Filter) needsLookup(user *db.User, subject Subject) bool {
	if !f.policy.ExcludeBots || f.lookup == nil {
		return false
	}

	if user != nil && user.BotCheckedAt != nil {
		return false
	}

	reason := f.policy.Decide(subject)

	return reason != OptedOut && reason != Configured
}

// setOptOut writes optedOutAt and the decision that follows from it.
func (f *Filter) setOptOut(ctx context.Context, gitlabUserID int64, username string, optedOutAt *time.Time) error {
	err := f.conn.WithContext(ctx).Transaction(func(txn *gorm.DB) error {
		user := db.User{GitLabUserID: gitlabUserID, Username: username}

		// Opting back in creates nobody: a user this app has never seen is
		// already taking part, and a row made here would put them on the
		// leaderboard and in the API before they had done anything.
		query := txn.Where(&db.User{GitLabUserID: gitlabUserID})

		var err error
		if optedOutAt == nil {
			err = query.First(&user).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
		} else {
			err = query.Attrs(&db.User{Username: username}).FirstOrCreate(&user).Error
		}

		if err != nil {
			return fmt.Errorf("failed to resolve user %d: %w", gitlabUserID, err)
		}

		user.OptedOutAt = optedOutAt

		err = txn.Model(&db.User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"opted_out_at":    optedOutAt,
			"excluded_reason": string(f.policy.Decide(subjectOf(&user))),
		}).Error
		if err != nil {
			return fmt.Errorf("failed to record opt-out for user %d: %w", gitlabUserID, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to change opt-out for user %d: %w", gitlabUserID, err)
	}

	return nil
}

// record persists a decision made by Check.
//
// An account seen for the first time is only written when it is excluded,
// or when GitLab's verdict on it is worth caching. A user who takes part is
// created by the engine with their first counted activity, as before;
// creating them here as well would make an account known to the API before
// it had done anything.
func (f *Filter) record(ctx context.Context, user *db.User, subject Subject, reason Reason, checkedAt *time.Time) error {
	if user == nil {
		if reason == Included && checkedAt == nil {
			return nil
		}

		var row db.User

		err := f.conn.WithContext(ctx).
			Where(&db.User{GitLabUserID: subject.ID}).
			Attrs(&db.User{
				Username:       subject.Username,
				ExcludedReason: string(reason),
				IsBot:          subject.IsBot,
				BotCheckedAt:   checkedAt,
			}).
			FirstOrCreate(&row).Error
		if err != nil {
			return fmt.Errorf("failed to record exclusion for user %d: %w", subject.ID, err)
		}

		// Another worker may have created the row since it was loaded (the
		// engine, on a concurrent activity from the same account), in which
		// case Attrs was not applied and the decision still has to be
		// written over what that worker stored.
		user = &row
	}

	updates := pendingUpdates(user, subject, reason, checkedAt)
	if len(updates) == 0 {
		return nil
	}

	err := f.conn.WithContext(ctx).Model(&db.User{}).Where("id = ?", user.ID).Updates(updates).Error
	if err != nil {
		return fmt.Errorf("failed to record exclusion for user %d: %w", subject.ID, err)
	}

	return nil
}

// pendingUpdates lists the columns of user's row a decision by Check
// changes.
func pendingUpdates(user *db.User, subject Subject, reason Reason, checkedAt *time.Time) map[string]any {
	updates := map[string]any{}

	if Reason(user.ExcludedReason) != reason {
		updates["excluded_reason"] = string(reason)
	}

	if checkedAt != nil && (user.BotCheckedAt == nil || user.IsBot != subject.IsBot) {
		updates["is_bot"] = subject.IsBot
		updates["bot_checked_at"] = checkedAt
	}

	return updates
}

// setReason writes one user's decision.
func (f *Filter) setReason(ctx context.Context, userID int64, reason Reason) error {
	err := f.conn.WithContext(ctx).Model(&db.User{}).Where("id = ?", userID).
		Update("excluded_reason", string(reason)).Error
	if err != nil {
		return fmt.Errorf("failed to record exclusion for user %d: %w", userID, err)
	}

	return nil
}

// load returns the row for gitlabUserID, or nil if there is none.
func (f *Filter) load(ctx context.Context, gitlabUserID int64) (*db.User, error) {
	var user db.User

	err := f.conn.WithContext(ctx).Where(&db.User{GitLabUserID: gitlabUserID}).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil //nolint:nilnil // a miss is not an error here
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load user %d: %w", gitlabUserID, err)
	}

	return &user, nil
}

// eachUser calls visit for every user query matches, in ID order and in
// batches, so neither a large instance nor a long GitLab lookup inside
// visit holds the whole table in memory or a cursor open.
func (f *Filter) eachUser(ctx context.Context, query *gorm.DB, visit func(*db.User) error) error {
	var lastID int64

	for {
		var batch []db.User

		err := query.Session(&gorm.Session{}).
			Where("id > ?", lastID).
			Order("id ASC").
			Limit(userBatchSize).
			Find(&batch).Error
		if err != nil {
			return fmt.Errorf("failed to load users: %w", err)
		}

		for index := range batch {
			if ctx.Err() != nil {
				return ctx.Err() //nolint:wrapcheck // cancellation is reported as is
			}

			err = visit(&batch[index])
			if err != nil {
				return err
			}
		}

		if len(batch) < userBatchSize {
			return nil
		}

		lastID = batch[len(batch)-1].ID
	}
}

// subjectOf describes a stored user to the policy.
func subjectOf(user *db.User) Subject {
	return Subject{
		ID:       user.GitLabUserID,
		Username: user.Username,
		IsBot:    user.IsBot,
		OptedOut: user.OptedOutAt != nil,
	}
}
