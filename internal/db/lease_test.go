package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// openMigratedTestDB opens a file-backed SQLite database rather than an
// in-memory one, because leases are about contention: ":memory:" gives
// every connection in the pool a private database of its own, so two
// callers would never see each other's rows and every test here would pass
// for the wrong reason.
func openMigratedTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "leases.db"))
	if err != nil {
		t.Fatalf("failed to open the test database: %v", err)
	}

	err = db.Migrate(conn)
	if err != nil {
		t.Fatalf("failed to migrate the test database: %v", err)
	}

	return conn
}

func TestAcquireLease_HeldContextStaysLiveUntilReleased(t *testing.T) {
	conn := openMigratedTestDB(t)

	held, release, err := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{})
	if err != nil {
		t.Fatalf("expected to take an uncontended lease, got: %v", err)
	}

	if held.Err() != nil {
		t.Fatalf("expected the held context to be live, got: %v", held.Err())
	}

	release()

	if held.Err() == nil {
		t.Error("expected releasing the lease to cancel the held context")
	}
}

func TestAcquireLease_ReleaseIsRepeatable(t *testing.T) {
	conn := openMigratedTestDB(t)

	_, release, err := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{})
	if err != nil {
		t.Fatalf("expected to take an uncontended lease, got: %v", err)
	}

	release()
	release()
}

// TestAcquireLease_SecondCallerWaits is the property the bootstrap race
// needs: while one process holds the lease, another does not get it.
func TestAcquireLease_SecondCallerWaits(t *testing.T) {
	conn := openMigratedTestDB(t)

	_, release, err := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{})
	if err != nil {
		t.Fatalf("expected to take an uncontended lease, got: %v", err)
	}

	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, _, err = db.AcquireLease(ctx, conn, db.BootstrapLease, db.LeaseOptions{Retry: 10 * time.Millisecond})
	if err == nil {
		t.Fatal("expected the second caller to be kept waiting while the lease is held")
	}
}

// TestAcquireLease_HandedOverOnRelease checks the waiter is not merely
// blocked but actually gets the lease once the holder gives it up.
func TestAcquireLease_HandedOverOnRelease(t *testing.T) {
	conn := openMigratedTestDB(t)

	_, release, err := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{})
	if err != nil {
		t.Fatalf("expected to take an uncontended lease, got: %v", err)
	}

	taken := make(chan error, 1)

	go func() {
		_, secondRelease, secondErr := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{
			Retry: 10 * time.Millisecond,
		})
		if secondErr == nil {
			secondRelease()
		}

		taken <- secondErr
	}()

	release()

	select {
	case secondErr := <-taken:
		if secondErr != nil {
			t.Fatalf("expected the waiter to take the released lease, got: %v", secondErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected the waiter to take the lease once it was released")
	}
}

// TestAcquireLease_ExpiredClaimIsTakenOver covers the holder that died
// without releasing: nothing renews its claim any more, so it has to run
// out rather than deadlock every process that starts afterwards.
//
// The dead holder is written directly rather than acquired and abandoned,
// because a live holder keeps renewing and would never expire.
func TestAcquireLease_ExpiredClaimIsTakenOver(t *testing.T) {
	conn := openMigratedTestDB(t)

	err := conn.Create(&db.Lease{
		Name:      db.BootstrapLease,
		Owner:     "a-process-that-died",
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}).Error
	if err != nil {
		t.Fatalf("failed to plant an expired lease: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, release, err := db.AcquireLease(ctx, conn, db.BootstrapLease, db.LeaseOptions{Retry: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("expected an expired claim to be taken over, got: %v", err)
	}

	release()
}

// TestAcquireLease_LiveHolderIsNotExpiredOutFromUnderItself checks the
// renewal actually renews: a holder whose work outlasts a single TTL must
// keep its claim rather than have a waiter take it away mid-bootstrap.
func TestAcquireLease_LiveHolderIsNotExpiredOutFromUnderItself(t *testing.T) {
	conn := openMigratedTestDB(t)

	held, release, err := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{
		TTL:   60 * time.Millisecond,
		Retry: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("expected to take an uncontended lease, got: %v", err)
	}

	defer release()

	// Several TTLs' worth of work, which only survives if the renewal
	// loop is pushing the claim out behind it.
	time.Sleep(300 * time.Millisecond)

	if held.Err() != nil {
		t.Fatalf("expected the holder to keep its lease while working, got: %v", held.Err())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, _, err = db.AcquireLease(ctx, conn, db.BootstrapLease, db.LeaseOptions{Retry: 10 * time.Millisecond})
	if err == nil {
		t.Error("expected a waiter to be kept out by the renewed claim")
	}
}

// TestAcquireLease_TakeoverCancelsTheOriginalHolder is what makes the
// lease worth taking: a holder that stalled past its claim must stop
// working rather than run alongside whoever holds it now.
func TestAcquireLease_TakeoverCancelsTheOriginalHolder(t *testing.T) {
	conn := openMigratedTestDB(t)

	held, release, err := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{
		TTL:   60 * time.Millisecond,
		Retry: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("expected to take an uncontended lease, got: %v", err)
	}

	defer release()

	// Another process takes the row over directly, which is what it would
	// do once this holder's claim ran out.
	err = conn.Model(&db.Lease{}).
		Where("name = ?", db.BootstrapLease).
		Updates(map[string]any{
			"owner":      "somebody-else",
			"expires_at": time.Now().UTC().Add(time.Hour),
		}).Error
	if err != nil {
		t.Fatalf("failed to simulate a takeover: %v", err)
	}

	select {
	case <-held.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("expected the displaced holder's context to be cancelled")
	}
}

func TestAcquireLease_SeparateNamesDoNotContend(t *testing.T) {
	conn := openMigratedTestDB(t)

	_, release, err := db.AcquireLease(t.Context(), conn, db.BootstrapLease, db.LeaseOptions{})
	if err != nil {
		t.Fatalf("expected to take an uncontended lease, got: %v", err)
	}

	defer release()

	_, otherRelease, err := db.AcquireLease(t.Context(), conn, "something-else", db.LeaseOptions{
		Retry: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("expected an unrelated lease to be free, got: %v", err)
	}

	otherRelease()
}
