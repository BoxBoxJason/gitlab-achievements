package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	// BootstrapLease names the lease the bootstrap sequence takes. It is
	// the only one this app uses; the table is keyed by name so that a
	// second singleton pass would not need a second table.
	BootstrapLease = "bootstrap"

	// DefaultLeaseTTL is how long a claim stands without renewal. It is
	// short on purpose: a holder that dies blocks every other process for
	// at most this long, and a live holder simply keeps renewing, so the
	// value bounds recovery rather than the work.
	DefaultLeaseTTL = 2 * time.Minute

	// DefaultLeaseRetry is how long a waiter sleeps between attempts.
	DefaultLeaseRetry = 5 * time.Second

	// renewalsPerTTL is how many renewals a holder attempts per lease
	// period. More than one means a single failed renewal, or one slow
	// query, does not cost the holder its lease.
	renewalsPerTTL = 3

	// ownerBytes is the length of the random per-process owner ID. It only
	// has to be unique among the processes contending for one lease.
	ownerBytes = 16
)

// freedAt is the timestamp written to a released lease. It is the Unix
// epoch rather than the zero Time because MySQL's DATETIME cannot store a
// year below 1000, and any timestamp in the past frees the lease equally
// well.
func freedAt() time.Time {
	return time.Unix(0, 0).UTC()
}

// LeaseOptions tunes how long a claim stands and how often a waiter retries.
// The zero value is usable and means the defaults.
type LeaseOptions struct {
	// TTL is how long this process's claim stands without renewal.
	TTL time.Duration
	// Retry is how long a waiter sleeps between attempts.
	Retry time.Duration
}

func (o LeaseOptions) withDefaults() LeaseOptions {
	if o.TTL <= 0 {
		o.TTL = DefaultLeaseTTL
	}

	if o.Retry <= 0 {
		o.Retry = DefaultLeaseRetry
	}

	return o
}

// AcquireLease blocks until it holds the named lease, and returns a context
// that stays live only for as long as this process still holds it.
//
// Callers must do their work under the returned context rather than the one
// they passed in. That is what makes the lease worth taking: if the holder
// stalls long enough for another process to take the lease over, or loses
// the database for longer than the lease stands, the returned context is
// cancelled and the work stops instead of running alongside whoever holds
// it now.
//
// The returned release func gives the lease up and stops renewing it. It is
// safe to call more than once, and it releases even when the caller's
// context has already been cancelled, so a shutdown does not leave the
// lease standing until it expires.
//
// Contention is expected and is not an error: a second process simply waits
// its turn. Only a database failure or the caller's context ending returns
// one.
func AcquireLease(ctx context.Context, conn *gorm.DB, name string, opts LeaseOptions) (context.Context, func(), error) {
	opts = opts.withDefaults()

	owner, err := newOwnerID()
	if err != nil {
		return nil, nil, err
	}

	err = waitForLease(ctx, conn, name, owner, opts)
	if err != nil {
		return nil, nil, err
	}

	held, lost := context.WithCancel(ctx)
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		renewLease(ctx, held, conn, name, owner, opts, lost)
	}()

	var once bool

	release := func() {
		if once {
			return
		}

		once = true

		lost()
		<-stopped

		releaseLease(ctx, conn, name, owner)
	}

	return held, release, nil
}

// waitForLease retries until the lease is this process's, the caller's
// context ends, or the database stops answering.
func waitForLease(ctx context.Context, conn *gorm.DB, name, owner string, opts LeaseOptions) error {
	ticker := time.NewTicker(opts.Retry)
	defer ticker.Stop()

	var waited bool

	for {
		taken, err := takeLease(ctx, conn, name, owner, opts.TTL)
		if err != nil {
			return err
		}

		if taken {
			if waited {
				zap.L().Info("acquired the lease held by another process",
					zap.String("lease", name),
				)
			}

			return nil
		}

		if !waited {
			waited = true

			zap.L().Info("another process holds the lease, waiting for it",
				zap.String("lease", name),
				zap.Duration("retry_every", opts.Retry),
			)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("gave up waiting for the %q lease: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

// takeLease makes one attempt at the lease, reporting whether this process
// now holds it.
//
// Both statements are single, self-guarded writes rather than a read
// followed by a write, because nothing here holds a row lock: the UPDATE
// only matches a lease whose claim has run out, and the INSERT only
// succeeds where the unique index on Name finds no row. Two processes
// racing therefore produce exactly one winner on either path, without
// needing SELECT ... FOR UPDATE, which SQLite does not have.
func takeLease(ctx context.Context, conn *gorm.DB, name, owner string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()

	result := conn.WithContext(ctx).
		Model(&Lease{}).
		Where("name = ? AND expires_at < ?", name, now).
		Updates(map[string]any{"owner": owner, "expires_at": now.Add(ttl)})
	if result.Error != nil {
		return false, fmt.Errorf("failed to take the %q lease: %w", name, result.Error)
	}

	if result.RowsAffected > 0 {
		return true, nil
	}

	// Nothing to take over: either somebody holds it, or no process has
	// ever taken this lease and there is no row yet. Creating the row is
	// the same thing as holding it, and a collision on the unique index
	// means another process created it first, which is a lost race rather
	// than a failure worth reporting.
	//
	// The statement runs with the logger off because that collision is the
	// expected outcome whenever several processes start against a fresh
	// database at once, and gorm logs a failed insert at error level. It
	// would put a constraint violation in the logs of a perfectly ordinary
	// start, which is exactly the kind of noise that teaches people to
	// ignore the logs. ON CONFLICT DO NOTHING would express it better, but
	// not on every database this app supports.
	err := conn.WithContext(ctx).
		Session(&gorm.Session{Logger: logger.Discard}).
		Create(&Lease{
			Name:      name,
			Owner:     owner,
			ExpiresAt: now.Add(ttl),
		}).Error

	return err == nil, nil
}

// renewLease keeps this process's claim standing until it is released, and
// cancels the held context if the claim is ever lost.
//
// It renews against ctx, the caller's context, rather than against held:
// held is what renewal failure cancels, so renewing under it would leave
// the last attempt cancelling the very context it needs.
func renewLease(ctx context.Context, held context.Context, conn *gorm.DB, name, owner string, opts LeaseOptions, lost context.CancelFunc) {
	ticker := time.NewTicker(opts.TTL / renewalsPerTTL)
	defer ticker.Stop()

	renewed := time.Now()

	for {
		select {
		case <-held.Done():
			return
		case <-ticker.C:
		}

		stillHeld, err := touchLease(ctx, conn, name, owner, opts.TTL)

		switch {
		case err != nil:
			// A database blip costs nothing until it has lasted long
			// enough for the claim to run out, at which point another
			// process is entitled to take over and this one has to stop.
			if time.Since(renewed) < opts.TTL {
				zap.L().Warn("failed to renew the lease, will retry",
					zap.String("lease", name),
					zap.Error(err),
				)

				continue
			}

			zap.L().Error("lost the lease: could not renew it before it ran out",
				zap.String("lease", name),
				zap.Duration("ttl", opts.TTL),
				zap.Error(err),
			)
		case !stillHeld:
			zap.L().Error("lost the lease: another process has taken it over",
				zap.String("lease", name),
			)
		default:
			renewed = time.Now()

			continue
		}

		lost()

		return
	}
}

// touchLease pushes this process's claim out by another TTL, reporting
// whether it still had one to push. The owner guard is what makes a lost
// lease detectable: once another process has taken it over, this update
// matches nothing.
func touchLease(ctx context.Context, conn *gorm.DB, name, owner string, ttl time.Duration) (bool, error) {
	result := conn.WithContext(ctx).
		Model(&Lease{}).
		Where("name = ? AND owner = ?", name, owner).
		Update("expires_at", time.Now().UTC().Add(ttl))
	if result.Error != nil {
		return false, fmt.Errorf("failed to renew the %q lease: %w", name, result.Error)
	}

	return result.RowsAffected > 0, nil
}

// releaseLease frees the lease for the next process.
//
// It runs detached from the caller's context, because the usual reason to
// release is that the context just ended: a shutdown that skipped this
// would leave every other process waiting out the remaining TTL for
// nothing. The owner guard means a lease already taken over by somebody
// else is left alone. Failing to release is logged rather than returned;
// the lease expires on its own, so there is nothing for a caller to do
// about it.
func releaseLease(ctx context.Context, conn *gorm.DB, name, owner string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultLeaseRetry)
	defer cancel()

	err := conn.WithContext(ctx).
		Model(&Lease{}).
		Where("name = ? AND owner = ?", name, owner).
		Update("expires_at", freedAt()).Error
	if err != nil {
		zap.L().Warn("failed to release the lease, it will expire on its own",
			zap.String("lease", name),
			zap.Error(err),
		)
	}
}

// newOwnerID mints the random identifier this process claims leases under.
func newOwnerID() (string, error) {
	buf := make([]byte, ownerBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("failed to generate a lease owner id: %w", err)
	}

	return hex.EncodeToString(buf), nil
}
