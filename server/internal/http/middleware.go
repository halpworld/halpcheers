package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/halpworld/halpcheers/server/internal/obs"
)

type contextKey string

const (
	sessionKey contextKey = "halp_session"
	powKey     contextKey = "halp_pow"
)

// SessionFromContext returns the parsed Bearer session token from request context.
func SessionFromContext(ctx context.Context) (string, bool) {
	tok, ok := ctx.Value(sessionKey).(string)
	return tok, ok
}

// PoWFromContext returns the parsed X-Halp-PoW token from request context.
func PoWFromContext(ctx context.Context) (string, bool) {
	pow, ok := ctx.Value(powKey).(string)
	return pow, ok
}

// RecoveryMiddleware catches panics and returns a uniform internal error without crashing.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequestSizeLimitMiddleware limits incoming request body sizes to maxBytes.
func RequestSizeLimitMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if maxBytes > 0 && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TimeoutMiddleware sets a request processing deadline on the context.
func TimeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// BearerAuthParseMiddleware parses the Authorization header and stores the raw session token in context.
// Authentication verification is performed downstream by the Accounts track.
func BearerAuthParseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimSpace(authHeader[7:])
			ctx := context.WithValue(r.Context(), sessionKey, token)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// PoWHeaderParseMiddleware parses X-Halp-PoW (<epoch>.<nonce>) and stores it in context.
// Verification is performed downstream by proof-of-work track.
func PoWHeaderParseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		powToken := r.Header.Get("X-Halp-PoW")
		if powToken != "" {
			ctx := context.WithValue(r.Context(), powKey, strings.TrimSpace(powToken))
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// BuildMiddlewareChain returns the full standard Halp middleware stack in exact execution order:
// 1. Recovery
// 2. Access Log (obs)
// 3. Request Size Limit
// 4. Timeout
// 5. Bearer Auth Parse
// 6. PoW Header Parse
func BuildMiddlewareChain(metrics *obs.Metrics, maxBodyBytes int64, timeout time.Duration) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		RecoveryMiddleware,
		obs.AccessLogMiddleware(nil),
		RequestSizeLimitMiddleware(maxBodyBytes),
		TimeoutMiddleware(timeout),
		BearerAuthParseMiddleware,
		PoWHeaderParseMiddleware,
	}
}
