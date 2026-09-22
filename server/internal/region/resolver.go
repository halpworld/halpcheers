package region

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/store"
)

type cacheEntry struct {
	target    string
	accountID core.AccountID
	handle    core.Handle
	paused    bool
	exists    bool
}

// Resolver implements core.Resolver with a fixed-capacity in-process LRU cache over SQLite.
// INVARIANT 7: Handles and aliases resolve through the same call and are indistinguishable to caller.
// INVARIANT 8: Fixed-size LRU capacity prevents memory exhaustion attacks.
type Resolver struct {
	store     *store.Store
	mu        sync.Mutex
	capacity  int
	items     map[string]*list.Element
	evictList *list.List
}

// NewResolver creates a new Resolver with the specified LRU capacity.
func NewResolver(st *store.Store, capacity int) *Resolver {
	if capacity <= 0 {
		capacity = 10_000
	}
	return &Resolver{
		store:     st,
		capacity:  capacity,
		items:     make(map[string]*list.Element, capacity),
		evictList: list.New(),
	}
}

// Capacity returns the maximum number of items held in the LRU cache.
func (r *Resolver) Capacity() int {
	return r.capacity
}

// Len returns the current number of cached items.
func (r *Resolver) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

// Invalidate removes a target from the LRU cache (called upon handle update or burn).
func (r *Resolver) Invalidate(target string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	clean := strings.TrimPrefix(target, "@")
	if elem, ok := r.items[clean]; ok {
		r.evictList.Remove(elem)
		delete(r.items, clean)
	}
	if elem, ok := r.items["@"+clean]; ok {
		r.evictList.Remove(elem)
		delete(r.items, "@"+clean)
	}
}

// Resolve maps target (Handle or Alias) to recipient AccountID and canonical Handle.
// ok is false if target does not exist, is paused, or has been burned.
func (r *Resolver) Resolve(ctx context.Context, target string) (core.AccountID, core.Handle, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, "", false
	}

	// 1. Check LRU cache
	r.mu.Lock()
	if elem, ok := r.items[target]; ok {
		r.evictList.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		r.mu.Unlock()

		if !entry.exists || entry.paused {
			return 0, "", false
		}
		return entry.accountID, entry.handle, true
	}
	r.mu.Unlock()

	// 2. Query SQLite
	if r.store == nil {
		return 0, "", false
	}

	var accountIDInt int64
	var canonicalHandle string
	var pausedInt int

	isAlias := strings.HasPrefix(target, "@")
	var err error

	if isAlias {
		aliasName := strings.TrimPrefix(target, "@")
		query := `
			SELECT a.account_id, h.handle, h.paused
			FROM aliases a
			JOIN handles h ON a.handle = h.handle
			WHERE a.alias = ?
		`
		err = r.store.ReadDB().QueryRowContext(ctx, query, aliasName).Scan(&accountIDInt, &canonicalHandle, &pausedInt)
	} else {
		query := `SELECT account_id, handle, paused FROM handles WHERE handle = ?`
		err = r.store.ReadDB().QueryRowContext(ctx, query, target).Scan(&accountIDInt, &canonicalHandle, &pausedInt)
	}

	entry := &cacheEntry{
		target: target,
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			entry.exists = false
		} else {
			return 0, "", false
		}
	} else {
		entry.exists = true
		entry.accountID = core.AccountID(accountIDInt)
		entry.handle = core.Handle(canonicalHandle)
		entry.paused = (pausedInt != 0)
	}

	// 3. Cache entry in LRU with fixed capacity bound
	r.mu.Lock()
	if len(r.items) >= r.capacity {
		oldest := r.evictList.Back()
		if oldest != nil {
			r.evictList.Remove(oldest)
			delete(r.items, oldest.Value.(*cacheEntry).target)
		}
	}
	elem := r.evictList.PushFront(entry)
	r.items[target] = elem
	r.mu.Unlock()

	if !entry.exists || entry.paused {
		return 0, "", false
	}
	return entry.accountID, entry.handle, true
}
