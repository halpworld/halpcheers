package tui_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/tui/internal/client"
	"github.com/halpworld/halpcheers/tui/internal/ui"
)

func TestHKDFVectorsAndKeyNonLeakage(t *testing.T) {
	knownKey := "1234567890123456"
	expectedAuthSecret := "66a32d811b1ae8e728e60fcc073c1cf9996e1dce4db7ab14de6c2d73742f22b3"
	expectedContactsKey := "50c7311ecac10d3049591a1904f208c2619f87309111b15375b69f726b00fc01"

	authHex, err := client.DeriveAuthSecretHex(knownKey)
	if err != nil {
		t.Fatalf("DeriveAuthSecretHex error: %v", err)
	}
	if authHex != expectedAuthSecret {
		t.Fatalf("expected auth_secret %s, got %s", expectedAuthSecret, authHex)
	}

	contactsBytes, err := client.DeriveContactsKey(knownKey)
	if err != nil {
		t.Fatalf("DeriveContactsKey error: %v", err)
	}
	contactsHex := hex.EncodeToString(contactsBytes)
	if contactsHex != expectedContactsKey {
		t.Fatalf("expected contacts_key %s, got %s", expectedContactsKey, contactsHex)
	}

	// Verify account key never leaks on the wire during login or send
	secretKey := "6421883051974462"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.String(), secretKey) || strings.Contains(string(body), secretKey) {
			t.Errorf("account key leaked on wire in %s", r.URL.Path)
		}
		for k, vals := range r.Header {
			for _, v := range vals {
				if strings.Contains(v, secretKey) {
					t.Errorf("account key leaked in header %s", k)
				}
			}
		}
		if r.URL.Path == "/v1/session" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"sess-tok","expires_at":1999999999}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/ping/") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}))
	defer srv.Close()

	c := client.New(srv.URL, srv.Client())
	if err := c.LoginWithAccountKey(context.Background(), secretKey); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if err := c.SendPing(context.Background(), "e7k4p2m9qx3va"); err != nil {
		t.Fatalf("send ping failed: %v", err)
	}
}

func TestEncryptedKeyFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "account.enc")
	key := "6421883051974462"

	if err := client.SaveAccountKeyEncrypted(keyPath, key); err != nil {
		t.Fatalf("SaveAccountKeyEncrypted error: %v", err)
	}

	rawDisk, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if strings.Contains(string(rawDisk), key) {
		t.Fatalf("plaintext account key found on disk!")
	}

	loaded, err := client.LoadAccountKeyEncrypted(keyPath)
	if err != nil {
		t.Fatalf("LoadAccountKeyEncrypted error: %v", err)
	}
	if loaded != key {
		t.Fatalf("expected loaded key %s, got %s", key, loaded)
	}
}

func TestSendPingLatencyFloorAndUniformity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := client.New(srv.URL, srv.Client())
	start := time.Now()
	err := c.SendPing(context.Background(), "e7k4p2m9qx3va")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error on SendPing, got %v", err)
	}
	if elapsed < 280*time.Millisecond {
		t.Fatalf("expected >= 300ms perceived latency floor, got %v", elapsed)
	}
}

func TestSSEStreamAndDemoteFallback(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "event: ping\ndata: {\"n\":3}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = fmt.Fprintf(w, "event: demote\ndata: {}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if r.URL.Path == "/v1/pending" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"n":4}`))
			return
		}
	}))
	defer srv.Close()

	c := client.New(srv.URL, srv.Client())
	var counts []int
	err := c.StreamSSE(context.Background(), func(ev client.StreamEvent) {
		if ev.Type == "ping" {
			counts = append(counts, ev.Count)
		}
	})
	if err != nil {
		t.Fatalf("StreamSSE error: %v", err)
	}
	if len(counts) != 2 || counts[0] != 3 || counts[1] != 4 {
		t.Fatalf("expected SSE ping (3) + demote /v1/pending fallback (4), got %v", counts)
	}
}

func TestTUIModelKeyWallAndBurnFlow(t *testing.T) {
	m := ui.NewModel("", true)
	if m.Screen != ui.ScreenKeyWall {
		t.Fatalf("expected initial screen to be key_wall, got %s", m.Screen)
	}

	// Pressing Enter before checking checkbox must stay on key_wall
	m = m.Update(ui.KeyMsg{Key: "enter"})
	if m.Screen != ui.ScreenKeyWall {
		t.Fatalf("expected key_wall gate to block Continue before checkbox is ticked")
	}

	// Tick checkbox then press Enter -> transitions to home
	m = m.Update(ui.KeyMsg{Key: "space"})
	m = m.Update(ui.KeyMsg{Key: "enter"})
	if m.Screen != ui.ScreenHome {
		t.Fatalf("expected transition to home after checkbox + Enter, got %s", m.Screen)
	}

	// Verify arrival copy bands (1 / 2-20 / 21+)
	if client.FormatArrivalCopy(1) != "Someone appreciates you." {
		t.Fatalf("unexpected n=1 copy: %s", client.FormatArrivalCopy(1))
	}
	if client.FormatArrivalCopy(7) != "7 people appreciate you." {
		t.Fatalf("unexpected n=7 copy: %s", client.FormatArrivalCopy(7))
	}
	if client.FormatArrivalCopy(412) != "412 people appreciate you." {
		t.Fatalf("unexpected n=412 copy: %s", client.FormatArrivalCopy(412))
	}

	// Verify burn flow requires exact handle confirmation
	m.Handles = []client.HandleDTO{{Handle: "e7k4p2m9qx3va", Label: "GitHub", Kind: "social"}}
	m = m.Update(ui.KeyMsg{Key: "b"})
	if m.Screen != ui.ScreenBurn {
		t.Fatalf("expected burn screen, got %s", m.Screen)
	}
	view := m.View()
	if !strings.Contains(view, ui.BurnPrompt("e7k4p2m9qx3va")) {
		t.Fatalf("burn view missing verbatim burn copy: %s", view)
	}
	m = m.Update(ui.KeyMsg{Key: "wrong"})
	m = m.Update(ui.KeyMsg{Key: "enter"})
	if len(m.Handles) != 1 {
		t.Fatalf("handle should not burn on wrong confirmation")
	}
	m = m.Update(ui.KeyMsg{Key: "e7k4p2m9qx3va"})
	m = m.Update(ui.KeyMsg{Key: "enter"})
	if len(m.Handles) != 0 || m.Screen != ui.ScreenHome {
		t.Fatalf("expected handle to burn on matching confirmation")
	}
}
