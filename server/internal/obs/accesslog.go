package obs

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
)

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	bytesWritten int64
}

func (r *responseRecorder) WriteHeader(status int) {
	r.statusCode = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += int64(n)
	return n, err
}

// AccessLogMiddleware returns HTTP middleware that logs request execution
// using ONLY the route pattern, method, status, latency, and response size.
//
// INVARIANT 9 ENFORCEMENT (docs/ARCHITECTURE.md § Observability without records):
// - The raw URL path is NEVER logged, because path parameters carry handles (e.g. /v1/ping/{target}).
// - Query strings, IP addresses, and User-Agent headers are NEVER logged.
// - Emits only chi.RouteContext RoutePattern() (e.g. "POST /v1/ping/{target}").
func AccessLogMiddleware(out io.Writer) func(http.Handler) http.Handler {
	if out == nil {
		out = os.Stdout
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(rec, r)

			duration := time.Since(start)

			// Extract matched route pattern from chi context
			routePattern := "unmatched"
			rctx := chi.RouteContext(r.Context())
			if rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					routePattern = pattern
				}
			}

			// Format: access: method=POST route=/v1/ping/{target} status=202 latency_us=450 size=0
			_, _ = fmt.Fprintf(out, "access: method=%s route=%s status=%d latency_us=%d size=%d\n",
				r.Method, routePattern, rec.statusCode, duration.Microseconds(), rec.bytesWritten)
		})
	}
}
