package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/store"
	"github.com/halpworld/halpcheers/server/internal/stream"
)

// PublicHandler handles public, unauthenticated routes (/h/{handle}, /@{alias}, /badge/{handle}.svg, /overlay/{token}).
// Owned by the Public pages track (#19).
//
// INVARIANT 6: Never reveal the sender.
// INVARIANT 7: Enforcement is invisible to the sender. Output bytes and timing are
// identical for real handles, missing handles, paused handles, and blocked senders.
// INVARIANT 9: No identifier in any log line or metric label.
type PublicHandler struct {
	configured bool
	store      *store.Store
	streamHub  *stream.Hub
}

// NewPublicHandler creates a new unconfigured PublicHandler stub (returns 501).
func NewPublicHandler() *PublicHandler {
	return &PublicHandler{configured: false}
}

// NewConfiguredPublicHandler creates a fully configured PublicHandler ready to serve.
func NewConfiguredPublicHandler() *PublicHandler {
	return &PublicHandler{configured: true}
}

// NewConfiguredPublicHandlerWithDeps creates a configured PublicHandler with store and streamHub dependencies.
func NewConfiguredPublicHandlerWithDeps(st *store.Store, hub *stream.Hub) *PublicHandler {
	return &PublicHandler{
		configured: true,
		store:      st,
		streamHub:  hub,
	}
}

const (
	badgeSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="136" height="20" role="img" aria-label="halp: appreciate me">
  <linearGradient id="b" x2="0" y2="100%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <clipPath id="a">
    <rect width="136" height="20" rx="3" fill="#fff"/>
  </clipPath>
  <g clip-path="url(#a)">
    <rect width="40" height="20" fill="#555"/>
    <rect x="40" width="96" height="20" fill="#0070f3"/>
    <rect width="136" height="20" fill="url(#b)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="system-ui,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif" text-rendering="geometricPrecision" font-size="110">
    <text x="210" y="140" transform="scale(.1)" fill="#fff" textLength="280">halp</text>
    <text x="870" y="140" transform="scale(.1)" fill="#fff" textLength="820">appreciate me</text>
  </g>
</svg>`

	landingHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Someone appreciates you.</title>
  <meta name="description" content="Anonymous. No message. They never learn it was you.">
  <meta property="og:title" content="Someone appreciates you.">
  <meta property="og:description" content="Anonymous. No message. They never learn it was you.">
  <meta property="og:type" content="website">
  <meta name="twitter:card" content="summary">
  <meta name="twitter:title" content="Someone appreciates you.">
  <meta name="twitter:description" content="Anonymous. No message. They never learn it was you.">
  <style>
    :root {
      --bg: #ffffff;
      --text: #111111;
      --subtext: #666666;
      --btn-bg: #111111;
      --btn-text: #ffffff;
      --btn-hover: #333333;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --bg: #121212;
        --text: #f0f0f0;
        --subtext: #888888;
        --btn-bg: #f0f0f0;
        --btn-text: #121212;
        --btn-hover: #cccccc;
      }
    }
    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }
    body {
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      min-height: 100vh;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 1.5rem;
      text-align: center;
    }
    main {
      max-width: 480px;
      width: 100%;
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 2rem;
    }
    h1 {
      font-size: 1.75rem;
      font-weight: 600;
      line-height: 1.3;
    }
    form {
      width: 100%;
      display: flex;
      justify-content: center;
    }
    button {
      background-color: var(--btn-bg);
      color: var(--btn-text);
      font-size: 1.125rem;
      font-weight: 500;
      padding: 0.875rem 2rem;
      border: none;
      border-radius: 9999px;
      cursor: pointer;
      width: 100%;
      max-width: 280px;
      transition: background-color 0.15s ease, opacity 0.15s ease;
    }
    button:hover:not(:disabled) {
      background-color: var(--btn-hover);
    }
    button:disabled {
      opacity: 0.7;
      cursor: default;
    }
    p.caption {
      font-size: 0.9375rem;
      color: var(--subtext);
      line-height: 1.5;
    }
    .sr-only {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }
    @media (prefers-reduced-motion: reduce) {
      button {
        transition: none !important;
      }
    }
  </style>
</head>
<body>
  <main>
    <h1>Someone appreciates you.</h1>
    <form method="POST" action="">
      <button type="submit" id="send-btn"{{BUTTON_ATTRS}}>{{BUTTON_TEXT}}</button>
    </form>
    <p class="caption">Anonymous. No message. They never learn it was you.</p>
    <div id="status" class="sr-only" aria-live="polite">{{STATUS_TEXT}}</div>
  </main>
  <script>
    document.addEventListener("DOMContentLoaded", function() {
      var form = document.querySelector("form");
      var btn = document.getElementById("send-btn");
      var liveRegion = document.getElementById("status");
      if (!form || !btn) return;

      form.addEventListener("submit", function(e) {
        e.preventDefault();
        if (btn.disabled) return;
        btn.disabled = true;
        btn.textContent = "Sending...";

        var start = Date.now();
        var minFloorMs = 300;
        var target = window.location.pathname.replace(/^\/(h\/|@)/, "");

        fetch("/v1/ping/" + encodeURIComponent(target), {
          method: "POST",
          headers: { "Content-Type": "application/json" }
        }).then(function() {
          var elapsed = Date.now() - start;
          var delay = Math.max(0, minFloorMs - elapsed);
          setTimeout(function() {
            btn.textContent = "Sent.";
            if (liveRegion) liveRegion.textContent = "Sent.";
          }, delay);
        }).catch(function() {
          var elapsed = Date.now() - start;
          var delay = Math.max(0, minFloorMs - elapsed);
          setTimeout(function() {
            btn.disabled = false;
            btn.textContent = "Appreciate them";
            if (liveRegion) liveRegion.textContent = "Something went wrong. Try again.";
          }, delay);
        });
      });
    });
  </script>
</body>
</html>`
)

var (
	initialLandingBytes []byte
	sentLandingBytes    []byte
	landingOnce         sync.Once
)

func initLandingBytes() {
	landingOnce.Do(func() {
		initialHTML := strings.ReplaceAll(landingHTMLTemplate, "{{BUTTON_ATTRS}}", "")
		initialHTML = strings.ReplaceAll(initialHTML, "{{BUTTON_TEXT}}", "Appreciate them")
		initialHTML = strings.ReplaceAll(initialHTML, "{{STATUS_TEXT}}", "")
		initialLandingBytes = []byte(initialHTML)

		sentHTML := strings.ReplaceAll(landingHTMLTemplate, "{{BUTTON_ATTRS}}", " disabled")
		sentHTML = strings.ReplaceAll(sentHTML, "{{BUTTON_TEXT}}", "Sent.")
		sentHTML = strings.ReplaceAll(sentHTML, "{{STATUS_TEXT}}", "Sent.")
		sentLandingBytes = []byte(sentHTML)
	})
}

// CalculateLatencyFloorDelay returns the remaining time to wait to fulfill the 300 ms floor.
func CalculateLatencyFloorDelay(start time.Time, now time.Time, floor time.Duration) time.Duration {
	elapsed := now.Sub(start)
	if elapsed >= floor {
		return 0
	}
	return floor - elapsed
}

func (h *PublicHandler) renderLanding(w http.ResponseWriter, r *http.Request) {
	if !h.configured {
		NotImplemented(w, r)
		return
	}

	initLandingBytes()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	if r.Method == http.MethodPost || r.URL.Query().Get("sent") == "1" {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(sentLandingBytes)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(sentLandingBytes)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(initialLandingBytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(initialLandingBytes)
}

// HandleLanding serves GET and POST for /h/{handle}.
func (h *PublicHandler) HandleLanding(w http.ResponseWriter, r *http.Request) {
	h.renderLanding(w, r)
}

// AliasLanding serves GET and POST for /@{alias}.
func (h *PublicHandler) AliasLanding(w http.ResponseWriter, r *http.Request) {
	h.renderLanding(w, r)
}

// BadgeSVG serves GET /badge/{handle}.svg as a static Shields-style SVG image.
func (h *PublicHandler) BadgeSVG(w http.ResponseWriter, r *http.Request) {
	if !h.configured {
		NotImplemented(w, r)
		return
	}

	rawSVG := strings.TrimSpace(badgeSVG) + "\n"

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400, s-maxage=604800, immutable")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(rawSVG)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(rawSVG))
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func findFlusher(w http.ResponseWriter) http.Flusher {
	if f, ok := w.(http.Flusher); ok {
		return f
	}
	val := reflect.ValueOf(w)
	for val.Kind() == reflect.Pointer || val.Kind() == reflect.Interface {
		val = val.Elem()
	}
	if val.Kind() == reflect.Struct {
		field := val.FieldByName("ResponseWriter")
		if field.IsValid() && field.CanInterface() {
			if inner, ok := field.Interface().(http.ResponseWriter); ok {
				return findFlusher(inner)
			}
		}
	}
	return nil
}

type overlaySSEWriter struct {
	http.ResponseWriter
	flusher   http.Flusher
	wroteInit bool
}

func (o *overlaySSEWriter) Flush() {
	if o.flusher != nil {
		o.flusher.Flush()
	}
}

func (o *overlaySSEWriter) Write(p []byte) (int, error) {
	if !o.wroteInit {
		o.wroteInit = true
		_, _ = o.ResponseWriter.Write([]byte(": ok\n\n"))
	}
	return o.ResponseWriter.Write(p)
}

const overlayHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Halp Overlay</title>
  <style>
    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }
    html, body {
      background: transparent !important;
      background-color: transparent !important;
      color: #ffffff;
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      width: 100vw;
      height: 100vh;
      overflow: hidden;
      position: relative;
    }

    .halp-card {
      display: inline-flex;
      align-items: center;
      gap: 0.75rem;
      background: rgba(18, 18, 18, 0.9);
      border: 1px solid rgba(255, 255, 255, 0.15);
      backdrop-filter: blur(8px);
      -webkit-backdrop-filter: blur(8px);
      box-shadow: 0 4px 24px rgba(0, 0, 0, 0.4);
      color: #ffffff;
      font-weight: 500;
      opacity: 0;
      pointer-events: none;
      transition: opacity 0.3s cubic-bezier(0.16, 1, 0.3, 1), transform 0.3s cubic-bezier(0.16, 1, 0.3, 1);
    }

    .style-pop-in {
      position: fixed;
      bottom: 40px;
      left: 50%;
      transform: translate(-50%, 20px) scale(0.95);
      border-radius: 9999px;
      padding: 0.875rem 2rem;
      font-size: 1.25rem;
    }
    .style-pop-in.visible {
      opacity: 1;
      transform: translate(-50%, 0) scale(1);
    }

    .style-corner {
      position: fixed;
      bottom: 24px;
      right: 24px;
      transform: translateY(12px);
      border-radius: 12px;
      padding: 0.75rem 1.25rem;
      font-size: 1rem;
    }
    .style-corner.visible {
      opacity: 1;
      transform: translateY(0);
    }

    .style-meter {
      position: fixed;
      bottom: 32px;
      left: 50%;
      transform: translate(-50%, 16px);
      border-radius: 16px;
      padding: 1rem 1.5rem;
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 0.5rem;
      min-width: 280px;
      font-size: 1.125rem;
    }
    .style-meter.visible {
      opacity: 1;
      transform: translate(-50%, 0);
    }
    .meter-track {
      width: 100%;
      height: 6px;
      background: rgba(255, 255, 255, 0.2);
      border-radius: 9999px;
      overflow: hidden;
    }
    .meter-fill {
      width: 0%;
      height: 100%;
      background: #0070f3;
      border-radius: 9999px;
      transition: width 0.3s ease;
    }

    @media (prefers-reduced-motion: reduce) {
      .halp-card {
        transition: opacity 0.1s ease !important;
        transform: none !important;
      }
      .style-pop-in {
        transform: translateX(-50%) !important;
      }
      .style-meter {
        transform: translateX(-50%) !important;
      }
      .meter-fill {
        transition: none !important;
      }
    }
  </style>
</head>
<body>
  <div id="overlay-widget" class="halp-card style-{{STYLE}}">
    <div id="overlay-text"></div>
    {{METER_HTML}}
  </div>
  <script>
    (function() {
      var widget = document.getElementById("overlay-widget");
      var textEl = document.getElementById("overlay-text");
      var meterFill = document.getElementById("meter-fill");
      var hideTimer = null;
      var totalCount = 0;

      var streamUrl = window.location.pathname + "?stream=1";
      var es = new EventSource(streamUrl);

      es.addEventListener("ping", function(e) {
        try {
          var payload = JSON.parse(e.data);
          var n = parseInt(payload.n, 10) || 1;
          totalCount += n;

          var label = (n === 1) ? "Someone appreciates you." : (n + " people appreciate you.");
          textEl.textContent = label;

          if (meterFill) {
            var pct = Math.min(100, totalCount * 10);
            meterFill.style.width = pct + "%";
          }

          widget.classList.add("visible");
          if (hideTimer) clearTimeout(hideTimer);
          hideTimer = setTimeout(function() {
            widget.classList.remove("visible");
          }, 5000);
        } catch (err) {}
      });
    })();
  </script>
</body>
</html>`

// Overlay serves GET /overlay/{token} (Phase 3 SSE overlay).
func (h *PublicHandler) Overlay(w http.ResponseWriter, r *http.Request) {
	if !h.configured {
		NotImplemented(w, r)
		return
	}

	rawTok := strings.TrimSpace(chi.URLParam(r, "token"))
	if !isHex32(rawTok) {
		WriteUniformNotFound(w)
		return
	}

	if h.store == nil {
		WriteUniformNotFound(w)
		return
	}

	rawTokLower := strings.ToLower(rawTok)
	var rawID int64
	var policyStr string
	err := h.store.ReadDB().QueryRowContext(
		r.Context(),
		"SELECT account_id, policy_json FROM handles WHERE kind = 'stream' AND paused = 0 AND LOWER(json_extract(policy_json, '$.overlay_token')) = ?",
		rawTokLower,
	).Scan(&rawID, &policyStr)
	if err != nil {
		WriteUniformNotFound(w)
		return
	}

	var pol map[string]any
	if err := json.Unmarshal([]byte(policyStr), &pol); err != nil {
		WriteUniformNotFound(w)
		return
	}
	tok, ok := pol["overlay_token"].(string)
	if !ok || subtle.ConstantTimeCompare([]byte(strings.ToLower(tok)), []byte(rawTokLower)) != 1 {
		WriteUniformNotFound(w)
		return
	}

	targetAccountID := core.AccountID(rawID)
	overlayStyle := "pop-in"
	if s, ok := pol["overlay_style"].(string); ok && s != "" {
		overlayStyle = s
	}

	if reqStyle := r.URL.Query().Get("style"); reqStyle != "" {
		switch reqStyle {
		case "pop-in", "meter", "corner":
			overlayStyle = reqStyle
		}
	}

	if r.URL.Query().Get("stream") == "1" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		flusher := findFlusher(w)
		if flusher == nil {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		if h.streamHub != nil {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")

			streamCtx, cancelStream := context.WithCancel(r.Context())
			defer cancelStream()

			// Watchdog: terminate SSE stream if the overlay token is rotated/revoked or handle is paused
			go func() {
				checkTicker := time.NewTicker(2 * time.Second)
				defer checkTicker.Stop()
				for {
					select {
					case <-streamCtx.Done():
						return
					case <-checkTicker.C:
						var count int
						qErr := h.store.ReadDB().QueryRowContext(
							streamCtx,
							"SELECT COUNT(1) FROM handles WHERE kind = 'stream' AND paused = 0 AND LOWER(json_extract(policy_json, '$.overlay_token')) = ?",
							rawTokLower,
						).Scan(&count)
						if qErr != nil || count == 0 {
							cancelStream()
							return
						}
					}
				}
			}()

			wrapped := &overlaySSEWriter{
				ResponseWriter: w,
				flusher:        flusher,
			}
			h.streamHub.ServeHTTP(wrapped, r.WithContext(streamCtx), targetAccountID)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, ": ok\n\n")
		flusher.Flush()

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = fmt.Fprintf(w, "event: ka\ndata: {}\n\n")
				flusher.Flush()
			}
		}
	}

	meterHTML := ""
	if overlayStyle == "meter" {
		meterHTML = `<div class="meter-track"><div id="meter-fill" class="meter-fill"></div></div>`
	}
	html := strings.ReplaceAll(overlayHTMLTemplate, "{{STYLE}}", overlayStyle)
	html = strings.ReplaceAll(html, "{{METER_HTML}}", meterHTML)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}
