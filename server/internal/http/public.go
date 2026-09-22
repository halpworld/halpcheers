package http

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
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
}

// NewPublicHandler creates a new unconfigured PublicHandler stub (returns 501).
func NewPublicHandler() *PublicHandler {
	return &PublicHandler{configured: false}
}

// NewConfiguredPublicHandler creates a fully configured PublicHandler ready to serve.
func NewConfiguredPublicHandler() *PublicHandler {
	return &PublicHandler{configured: true}
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

// Overlay serves GET /overlay/{token} (Phase 3 SSE overlay, stubs 501 in Phase 1).
func (h *PublicHandler) Overlay(w http.ResponseWriter, r *http.Request) {
	NotImplemented(w, r)
}
