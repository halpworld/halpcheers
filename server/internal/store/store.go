package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/halpworld/halpcheers/server/internal/core"
)

type writeTask struct {
	fn    func(tx *sql.Tx) error
	errCh chan error
	ctx   context.Context
}

// Store provides typed, hand-written SQL storage access over SQLite.
//
// CONCURRENCY ARCHITECTURE (docs/ARCHITECTURE.md § Capacity budget):
// SQLite in WAL mode allows multiple concurrent readers but only one writer.
// To guarantee zero lock contention and eliminate SQLITE_BUSY, all write
// operations are funneled through a single dedicated writer goroutine.
// Reads are executed against a separate read connection pool.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
	writeCh chan writeTask
	closeWg sync.WaitGroup
	closed  chan struct{}
}

// Open initializes the Store, opens separate write and read connections with
// the documented pragmas (WAL, synchronous=NORMAL, foreign_keys=ON, busy_timeout=5000),
// runs migrations, and starts the single writer goroutine.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	// Pragma string for both connections
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", dbPath)

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open write db: %w", err)
	}
	writeDB.SetMaxOpenConns(1) // Single writer connection

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open read db: %w", err)
	}
	readDB.SetMaxOpenConns(16) // Concurrent readers

	// Run migrations using write connection
	if err := Migrate(ctx, writeDB); err != nil {
		_ = writeDB.Close()
		_ = readDB.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	s := &Store{
		writeDB: writeDB,
		readDB:  readDB,
		writeCh: make(chan writeTask, 256),
		closed:  make(chan struct{}),
	}

	s.closeWg.Add(1)
	go s.writerLoop()

	return s, nil
}

// Close gracefully stops the writer goroutine and closes database connections.
func (s *Store) Close() error {
	close(s.closed)
	s.closeWg.Wait()

	wErr := s.writeDB.Close()
	rErr := s.readDB.Close()
	if wErr != nil {
		return wErr
	}
	return rErr
}

// writerLoop is the dedicated single writer goroutine that serializes all database mutations.
func (s *Store) writerLoop() {
	defer s.closeWg.Done()

	for {
		select {
		case <-s.closed:
			// Drain remaining tasks
			for {
				select {
				case task := <-s.writeCh:
					task.errCh <- s.execWrite(task.ctx, task.fn)
				default:
					return
				}
			}
		case task := <-s.writeCh:
			task.errCh <- s.execWrite(task.ctx, task.fn)
		}
	}
}

func (s *Store) execWrite(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Write schedules a write transaction onto the single writer goroutine and awaits completion.
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	task := writeTask{
		fn:    fn,
		errCh: make(chan error, 1),
		ctx:   ctx,
	}

	select {
	case <-s.closed:
		return fmt.Errorf("store closed")
	case <-ctx.Done():
		return ctx.Err()
	case s.writeCh <- task:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-task.errCh:
		return err
	}
}

// ReadDB returns the read-only connection pool for select queries.
func (s *Store) ReadDB() *sql.DB {
	return s.readDB
}

// DeleteAccount performs immediate, synchronous, and complete account erasure in one local transaction.
//
// In accordance with docs/PRIVACY.md § Erasure:
// Cascading foreign keys from `accounts` remove handles, aliases, subscriptions,
// settings, group rows, the contacts blob, and blocks in one go.
// Zero records of the account survive in any table.
func (s *Store) DeleteAccount(ctx context.Context, id core.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		// First explicitly delete blocks initiated by this account if not caught by FK
		if _, err := tx.ExecContext(ctx, "DELETE FROM blocks WHERE sender_account_id = ?", id.Int64()); err != nil {
			return fmt.Errorf("delete blocks: %w", err)
		}

		// Delete account - foreign keys with ON DELETE CASCADE handle the rest
		res, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id.Int64())
		if err != nil {
			return fmt.Errorf("delete account: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("account not found")
		}
		return nil
	})
}

// CreateAccount persists a new account row.
func (s *Store) CreateAccount(ctx context.Context, keyHash []byte, region string) (core.AccountID, error) {
	var id int64
	today := core.Today().Int()

	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO accounts (key_hash, region, created_day, last_seen_day, send_tier, suspended, recv_total)
			VALUES (?, ?, ?, ?, 0, 0, 0)
		`, keyHash, region, today, today)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})

	return core.AccountID(id), err
}

// GetAccountByID retrieves an account by ID.
func (s *Store) GetAccountByID(ctx context.Context, id core.AccountID) (*core.Account, error) {
	row := s.readDB.QueryRowContext(ctx, `
		SELECT id, key_hash, region, created_day, last_seen_day, send_tier, suspended, recv_total
		FROM accounts WHERE id = ?
	`, id.Int64())

	var acc core.Account
	var keyHash []byte
	var suspended int
	var createdDay, lastSeenDay int64

	err := row.Scan(&acc.ID, &keyHash, &acc.Region, &createdDay, &lastSeenDay, &acc.SendTier, &suspended, &acc.RecvTotal)
	if err != nil {
		return nil, err
	}

	acc.KeyHash = keyHash
	acc.Suspended = (suspended != 0)
	acc.CreatedDay = core.DayFromInt(createdDay)
	acc.LastSeenDay = core.DayFromInt(lastSeenDay)

	return &acc, nil
}

// CreateHandle registers a new handle under an account.
func (s *Store) CreateHandle(ctx context.Context, h core.Handle, accID core.AccountID, kind core.HandleKind, label string) error {
	today := core.Today().Int()
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handles (handle, account_id, label, kind, paused, created_day)
			VALUES (?, ?, ?, ?, 0, ?)
		`, h.Raw(), accID.Int64(), label, kind.String(), today)
		return err
	})
}

// ResolveTarget implements core.Resolver against SQLite handles and aliases tables.
func (s *Store) ResolveTarget(ctx context.Context, target string) (core.AccountID, core.Handle, bool) {
	// First check if target is an alias
	if len(target) > 0 && target[0] == '@' {
		var accID int64
		var hStr string
		err := s.readDB.QueryRowContext(ctx, `
			SELECT a.account_id, a.handle
			FROM aliases a
			JOIN handles h ON a.handle = h.handle
			WHERE a.alias = ? AND h.paused = 0
		`, target).Scan(&accID, &hStr)
		if err == nil {
			return core.AccountID(accID), core.Handle(hStr), true
		}
		return 0, "", false
	}

	// Target is a handle
	var accID int64
	var paused int
	err := s.readDB.QueryRowContext(ctx, `
		SELECT account_id, paused
		FROM handles WHERE handle = ?
	`, target).Scan(&accID, &paused)
	if err == nil && paused == 0 {
		return core.AccountID(accID), core.Handle(target), true
	}

	return 0, "", false
}

// IsBlocked reports whether sender is blocked from sending to handle.
func (s *Store) IsBlocked(ctx context.Context, sender core.AccountID, handle core.Handle) (bool, error) {
	var exists int
	err := s.readDB.QueryRowContext(ctx, `
		SELECT 1 FROM blocks WHERE sender_account_id = ? AND handle = ?
	`, sender.Int64(), handle.Raw()).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}
