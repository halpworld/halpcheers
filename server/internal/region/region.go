package region

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/store"
)

const (
	// RegionPrefix is the mandatory first character of every minted handle ('e' for eu-1).
	RegionPrefix = "e"
	// CrockfordAlphabet is the lowercase Crockford base32 alphabet (32 chars).
	CrockfordAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	// HandleLength is the fixed length of every minted handle (1 prefix + 12 chars = 13).
	HandleLength = 13
)

var (
	ErrReservedAlias = errors.New("alias is a reserved name")
	ErrInvalidAlias  = errors.New("invalid alias format")
	ErrHandleNotFound = errors.New("handle not found")
	ErrUnauthorized   = errors.New("unauthorized account")
)

// ReservedAliases contains administrative and system keywords that cannot be claimed as aliases.
var ReservedAliases = map[string]struct{}{
	"admin":         {},
	"administrator": {},
	"root":          {},
	"support":       {},
	"halp":          {},
	"help":          {},
	"abuse":         {},
	"security":      {},
	"privacy":       {},
	"terms":         {},
	"system":        {},
	"postmaster":    {},
	"hostmaster":    {},
	"webmaster":     {},
	"api":           {},
	"null":          {},
	"undefined":     {},
	"login":         {},
	"signup":        {},
	"register":      {},
	"account":       {},
	"handles":       {},
	"stream":        {},
	"ping":          {},
	"public":        {},
	"badge":         {},
}

// IsReservedAlias checks if an alias matches a forbidden reserved identifier.
func IsReservedAlias(alias string) bool {
	clean := strings.ToLower(strings.TrimPrefix(alias, "@"))
	_, ok := ReservedAliases[clean]
	return ok
}

// Service manages handle minting, lifetime (pause/burn), and aliases over SQLite.
type Service struct {
	store         *store.Store
	resolver      *Resolver
	burnedHandles sync.Map // handle string -> true
}

// NewService creates a new handles and aliases service.
func NewService(st *store.Store, resolver *Resolver) *Service {
	return &Service{
		store:    st,
		resolver: resolver,
	}
}

// MintHandle generates a unique 13-character Crockford base32 handle with 60 bits entropy and 'e' prefix.
func (s *Service) MintHandle(ctx context.Context) (core.Handle, error) {
	for attempts := 0; attempts < 100; attempts++ {
		h, err := GenerateHandle()
		if err != nil {
			return "", err
		}

		// Ensure it was never burned
		if _, burned := s.burnedHandles.Load(h.Raw()); burned {
			continue
		}

		// Ensure it does not already exist in SQLite
		if s.store != nil {
			var dummy string
			err := s.store.ReadDB().QueryRowContext(ctx, "SELECT handle FROM handles WHERE handle = ?", h.Raw()).Scan(&dummy)
			if err == nil {
				// Handle collision, retry
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}

		return h, nil
	}
	return "", errors.New("failed to generate unique handle after 100 attempts")
}

// GenerateHandle constructs a 13-character handle: 'e' + 12 base32 characters (60 bits of entropy).
func GenerateHandle() (core.Handle, error) {
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("failed to read entropy: %w", err)
	}

	val := binaryBigEndianUint64(entropy[:])
	// Extract 60 bits (12 x 5 bits)
	var sb strings.Builder
	sb.Grow(HandleLength)
	sb.WriteString(RegionPrefix)

	for i := 0; i < 12; i++ {
		idx := (val >> (5 * (11 - i))) & 0x1F
		sb.WriteByte(CrockfordAlphabet[idx])
	}

	return core.Handle(sb.String()), nil
}

func binaryBigEndianUint64(b []byte) uint64 {
	return uint64(b[7]) | uint64(b[6])<<8 | uint64(b[5])<<16 | uint64(b[4])<<24 |
		uint64(b[3])<<32 | uint64(b[2])<<40 | uint64(b[1])<<48 | uint64(b[0])<<56
}

// BurnHandle permanently deletes a handle, ensuring it is never reissued (docs/IDENTITY.md § 2).
func (s *Service) BurnHandle(ctx context.Context, accountID core.AccountID, handle core.Handle) error {
	s.burnedHandles.Store(handle.Raw(), true)
	if s.resolver != nil {
		s.resolver.Invalidate(handle.Raw())
	}

	if s.store == nil {
		return nil
	}

	return s.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM handles WHERE handle = ? AND account_id = ?", handle.Raw(), accountID.Int64())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrHandleNotFound
		}
		return nil
	})
}

// IsBurned reports whether a handle has been permanently burned.
func (s *Service) IsBurned(handle core.Handle) bool {
	_, burned := s.burnedHandles.Load(handle.Raw())
	return burned
}
