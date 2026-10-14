package db_test

import (
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// TestOpen_SQLiteConcurrentWriters guards the defaults sqliteDSN adds. With
// a bare file DSN, several goroutines running read-then-write transactions
// at once fail a sizeable share of them with SQLITE_BUSY, which is the shape
// of what the webhook workers, the backfill, and the sweeps do together.
func TestOpen_SQLiteConcurrentWriters(t *testing.T) {
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "contention.db"))
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	err = db.Migrate(conn)
	if err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	const (
		writers   = 6
		perWriter = 100
	)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures []error
	)

	for writer := range writers {
		wg.Go(func() {
			for index := range perWriter {
				txErr := conn.Transaction(func(txn *gorm.DB) error {
					var count int64

					countErr := txn.Model(&db.SyncState{}).Count(&count).Error
					if countErr != nil {
						return countErr
					}

					return txn.Create(&db.SyncState{
						Key:   strconv.Itoa(writer) + "/" + strconv.Itoa(index),
						Value: "v",
					}).Error
				})
				if txErr != nil {
					mu.Lock()
					failures = append(failures, txErr)
					mu.Unlock()
				}
			}
		})
	}

	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d of %d concurrent transactions failed, first: %v", len(failures), writers*perWriter, failures[0])
	}
}
