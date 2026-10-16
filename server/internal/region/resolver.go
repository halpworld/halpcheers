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
	kind      core.HandleKind
	groupID   int64
	mode      core.PolicyMode
	paused    bool
	exists    bool
}

type groupMemberKey struct {
	groupID   int64
	accountID core.AccountID
}

// Resolver implements core.Resolver with a fixed-capacity in-process LRU cache over SQLite.
// INVARIANT 7: Handles and aliases resolve through the same call and are indistinguishable to caller.
// INVARIANT 8: Fixed-size LRU capacity prevents memory exhaustion attacks.
type Resolver struct {
	store        *store.Store
	mu           sync.Mutex
	capacity     int
	minGroupSize int
	items        map[string]*list.Element
	groupItems   map[int64]map[string]*list.Element
	memberCache  map[groupMemberKey]bool
	evictList    *list.List
}

// NewResolver creates a new Resolver with the specified LRU capacity.
func NewResolver(st *store.Store, capacity int) *Resolver {
	if capacity <= 0 {
		capacity = 10_000
	}
	return &Resolver{
		store:        st,
		capacity:     capacity,
		minGroupSize: 5,
		items:        make(map[string]*list.Element, capacity),
		groupItems:   make(map[int64]map[string]*list.Element),
		memberCache:  make(map[groupMemberKey]bool, capacity),
		evictList:    list.New(),
	}
}

// SetMinGroupSize updates the minimum group size floor required for group-scoped handles (default 5).
func (r *Resolver) SetMinGroupSize(minSize int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if minSize >= 2 {
		r.minGroupSize = minSize
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

func (r *Resolver) removeElementLocked(elem *list.Element) {
	if elem == nil {
		return
	}
	entry := elem.Value.(*cacheEntry)
	r.evictList.Remove(elem)
	delete(r.items, entry.target)
	if entry.groupID > 0 {
		if gMap, ok := r.groupItems[entry.groupID]; ok {
			delete(gMap, entry.target)
			if len(gMap) == 0 {
				delete(r.groupItems, entry.groupID)
			}
		}
	}
}

// Invalidate removes a target from the LRU cache (called upon handle update or burn).
func (r *Resolver) Invalidate(target string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	clean := strings.TrimPrefix(target, "@")
	if elem, ok := r.items[clean]; ok {
		r.removeElementLocked(elem)
	}
	if elem, ok := r.items["@"+clean]; ok {
		r.removeElementLocked(elem)
	}
}

// InvalidateGroup removes all cached handles and membership entries belonging to the given groupID in O(group_size).
func (r *Resolver) InvalidateGroup(groupID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if gMap, ok := r.groupItems[groupID]; ok {
		for _, elem := range gMap {
			r.evictList.Remove(elem)
			delete(r.items, elem.Value.(*cacheEntry).target)
		}
		delete(r.groupItems, groupID)
	}
	for k := range r.memberCache {
		if k.groupID == groupID {
			delete(r.memberCache, k)
		}
	}
}

// Resolve maps target (Handle or Alias) to recipient AccountID and canonical Handle.
// ok is false if target does not exist, is paused, has been burned, or belongs to a group below min_size.
func (r *Resolver) Resolve(ctx context.Context, target string) (core.AccountID, core.Handle, bool) {
	accID, handle, _, _, _, ok := r.ResolveDetailed(ctx, target)
	return accID, handle, ok
}

// ResolveDetailed maps target to recipient AccountID, canonical Handle, HandleKind, GroupID, and PolicyMode.
func (r *Resolver) ResolveDetailed(ctx context.Context, target string) (core.AccountID, core.Handle, core.HandleKind, int64, core.PolicyMode, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, "", "", 0, "", false
	}

	// 1. Check LRU cache
	r.mu.Lock()
	minGroupSize := r.minGroupSize
	if elem, ok := r.items[target]; ok {
		r.evictList.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		r.mu.Unlock()

		if !entry.exists || entry.paused || entry.mode == core.PolicyModePaused {
			return 0, "", "", 0, "", false
		}
		if entry.mode == core.PolicyModeGroupsOnly && entry.kind != core.HandleKindGroup {
			return 0, "", "", 0, "", false
		}
		return entry.accountID, entry.handle, entry.kind, entry.groupID, entry.mode, true
	}
	r.mu.Unlock()

	// 2. Query SQLite
	if r.store == nil {
		return 0, "", "", 0, "", false
	}

	var accountIDInt int64
	var canonicalHandle string
	var pausedInt int
	var kindStr string
	var groupIDNull sql.NullInt64
	var modeNull sql.NullString

	isAlias := strings.HasPrefix(target, "@")
	var err error

	if isAlias {
		aliasName := strings.TrimPrefix(target, "@")
		query := `
			SELECT a.account_id, h.handle, h.paused, h.kind, h.group_id, s.mode
			FROM aliases a
			JOIN handles h ON a.handle = h.handle
			LEFT JOIN settings s ON a.account_id = s.account_id
			WHERE a.alias = ?
		`
		err = r.store.ReadDB().QueryRowContext(ctx, query, aliasName).Scan(&accountIDInt, &canonicalHandle, &pausedInt, &kindStr, &groupIDNull, &modeNull)
	} else {
		query := `
			SELECT h.account_id, h.handle, h.paused, h.kind, h.group_id, s.mode
			FROM handles h
			LEFT JOIN settings s ON h.account_id = s.account_id
			WHERE h.handle = ?
		`
		err = r.store.ReadDB().QueryRowContext(ctx, query, target).Scan(&accountIDInt, &canonicalHandle, &pausedInt, &kindStr, &groupIDNull, &modeNull)
	}

	entry := &cacheEntry{
		target: target,
		kind:   core.HandleKindPersonal,
		mode:   core.PolicyModeAll,
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			entry.exists = false
		} else {
			return 0, "", "", 0, "", false
		}
	} else {
		entry.exists = true
		entry.accountID = core.AccountID(accountIDInt)
		entry.handle = core.Handle(canonicalHandle)
		entry.paused = (pausedInt != 0)
		if k := core.HandleKind(kindStr); k.IsValid() {
			entry.kind = k
		}
		if groupIDNull.Valid {
			entry.groupID = groupIDNull.Int64
		}
		if modeNull.Valid && modeNull.String != "" {
			entry.mode = core.PolicyMode(modeNull.String)
		}

		// Enforce minimum group size (docs/GROUPS.md § Explicitly not in scope):
		// Group-scoped pings are disabled when roster count < groups.min_size (default 5).
		if entry.kind == core.HandleKindGroup && entry.groupID > 0 {
			var memberCount int
			cErr := r.store.ReadDB().QueryRowContext(ctx,
				"SELECT COUNT(1) FROM group_members WHERE group_id = ?",
				entry.groupID,
			).Scan(&memberCount)
			if cErr != nil || memberCount < minGroupSize {
				entry.paused = true
			}
		}
	}

	// 3. Cache entry in LRU with fixed capacity bound
	r.mu.Lock()
	if len(r.items) >= r.capacity {
		oldest := r.evictList.Back()
		if oldest != nil {
			r.removeElementLocked(oldest)
		}
	}
	elem := r.evictList.PushFront(entry)
	r.items[target] = elem
	if entry.groupID > 0 {
		gMap, ok := r.groupItems[entry.groupID]
		if !ok {
			gMap = make(map[string]*list.Element)
			r.groupItems[entry.groupID] = gMap
		}
		gMap[target] = elem
	}
	r.mu.Unlock()

	if !entry.exists || entry.paused || entry.mode == core.PolicyModePaused {
		return 0, "", "", 0, "", false
	}
	if entry.mode == core.PolicyModeGroupsOnly && entry.kind != core.HandleKindGroup {
		return 0, "", "", 0, "", false
	}
	return entry.accountID, entry.handle, entry.kind, entry.groupID, entry.mode, true
}

// IsGroupMember checks whether accountID is a member of groupID using an O(1) bounded in-memory cache.
func (r *Resolver) IsGroupMember(ctx context.Context, groupID int64, accountID core.AccountID) bool {
	if r.store == nil || groupID <= 0 {
		return false
	}
	key := groupMemberKey{groupID: groupID, accountID: accountID}

	r.mu.Lock()
	if isMember, ok := r.memberCache[key]; ok {
		r.mu.Unlock()
		return isMember
	}
	r.mu.Unlock()

	var count int
	err := r.store.ReadDB().QueryRowContext(ctx,
		"SELECT COUNT(1) FROM group_members WHERE group_id = ? AND account_id = ?",
		groupID, accountID.Int64(),
	).Scan(&count)
	if err != nil {
		return false
	}
	isMember := count > 0

	r.mu.Lock()
	if len(r.memberCache) >= r.capacity {
		for k := range r.memberCache {
			delete(r.memberCache, k)
			break
		}
	}
	r.memberCache[key] = isMember
	r.mu.Unlock()

	return isMember
}
