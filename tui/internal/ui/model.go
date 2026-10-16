// Package ui implements the Bubble Tea style Elm-architecture TUI model (Init / Update / View)
// for the Halp terminal client.
//
// SPECIFICATION (docs/UI.md, docs/DELIVERY.md, docs/ROADMAP.md):
// - Key wall on first run with checkbox confirmation gate ("I have written it down").
// - Home view with scaled arrival counter, per-handle list, and quick-send field.
// - Two-state send ("Sending...", "Sent.") with 300 ms latency floor.
// - Verbatim burn confirmation copy.
// - Optional terminal bell (\a) on incoming appreciation ping.
package ui

import (
	"fmt"
	"strings"

	"github.com/halpworld/halpcheers/tui/internal/client"
)

// Screen identifies the active TUI screen.
type Screen string

const (
	ScreenKeyWall Screen = "key_wall"
	ScreenLogin   Screen = "login"
	ScreenHome    Screen = "home"
	ScreenBurn    Screen = "burn"
)

// Verbatim UI copy from docs/UI.md.
const (
	KeyWallLine1    = "This is your account. There is no email, no password and no reset."
	KeyWallLine2    = "If you lose it, the account is gone — handles, settings and all."
	KeyWallCheckbox = "I have written it down"
	LoginFailureMsg = "That didn't work. Check the digits and try again."
	SendStateIdle   = "Appreciate them"
	SendStateActive = "Sending..."
	SendStateSent   = "Sent."
	SendErrorCopy   = "Something went wrong. Try again."
)

// BurnPrompt returns the verbatim burn dialog copy from docs/UI.md §5.
func BurnPrompt(target string) string {
	return "Burn " + target + "? Anyone with this link or QR code loses the ability to reach you, immediately and permanently. The handle is never reissued and this cannot be undone. Your other handles are unaffected."
}

// Msg is the interface implemented by all TUI state machine messages.
type Msg interface{}

// KeyMsg represents a keyboard press in the TUI.
type KeyMsg struct {
	Key string // e.g. "space", "enter", "q", "b", "s", or typed text
}

// ArrivalMsg represents an incoming appreciation digest over SSE or /v1/pending.
type ArrivalMsg struct {
	Count int
}

// SendStartMsg transitions the send button into "Sending...".
type SendStartMsg struct {
	Target string
}

// SendDoneMsg transitions the send button into "Sent." (or generic error on network failure).
type SendDoneMsg struct {
	Err error
}

// ConnectionStatusMsg updates the SSE connection state indicator.
type ConnectionStatusMsg struct {
	Status string // "connected", "polling", "reconnecting"
}

// Model holds the complete state of the TUI application.
type Model struct {
	Screen          Screen
	AccountKey      string
	KeySavedChecked bool
	TodayCount      int
	Handles         []client.HandleDTO
	SelectedHandle  int
	SendInput       string
	SendState       string
	SendError       string
	BurnTarget      string
	BurnConfirmText string
	ConnectionState string
	EnableBell      bool
	BellRungCount   int
	Quitting        bool
}

// NewModel initializes a new TUI Model.
// If accountKey is already stored locally, starts on ScreenHome; otherwise generates a key and shows ScreenKeyWall.
func NewModel(accountKey string, enableBell bool) Model {
	if accountKey != "" {
		return Model{
			Screen:          ScreenHome,
			AccountKey:      accountKey,
			SendState:       SendStateIdle,
			ConnectionState: "connecting",
			EnableBell:      enableBell,
		}
	}
	freshKey, _ := client.GenerateAccountKey()
	return Model{
		Screen:          ScreenKeyWall,
		AccountKey:      freshKey,
		KeySavedChecked: false,
		SendState:       SendStateIdle,
		ConnectionState: "connecting",
		EnableBell:      enableBell,
	}
}

// Init initializes the Bubble Tea style model.
func (m Model) Init() Msg {
	return nil
}

// Update advances the TUI state machine in response to Msg.
func (m Model) Update(msg Msg) Model {
	switch ev := msg.(type) {
	case ArrivalMsg:
		if ev.Count > 0 {
			m.TodayCount += ev.Count
			if m.EnableBell {
				m.BellRungCount++
			}
		}
	case ConnectionStatusMsg:
		m.ConnectionState = ev.Status
	case SendStartMsg:
		m.SendState = SendStateActive
		m.SendError = ""
	case SendDoneMsg:
		if ev.Err != nil {
			m.SendState = SendStateIdle
			m.SendError = SendErrorCopy
		} else {
			m.SendState = SendStateSent
			m.SendInput = ""
			m.SendError = ""
		}
	case KeyMsg:
		switch m.Screen {
		case ScreenKeyWall:
			switch ev.Key {
			case " ", "space", "x":
				m.KeySavedChecked = !m.KeySavedChecked
			case "enter":
				if m.KeySavedChecked {
					m.Screen = ScreenHome
				}
			case "q", "ctrl+c":
				m.Quitting = true
			}
		case ScreenHome:
			switch ev.Key {
			case "q", "ctrl+c":
				m.Quitting = true
			case "j", "down":
				if len(m.Handles) > 0 && m.SelectedHandle < len(m.Handles)-1 {
					m.SelectedHandle++
				}
			case "k", "up":
				if m.SelectedHandle > 0 {
					m.SelectedHandle--
				}
			case "b":
				if len(m.Handles) > 0 && m.SelectedHandle < len(m.Handles) {
					m.BurnTarget = m.Handles[m.SelectedHandle].Handle
					m.BurnConfirmText = ""
					m.Screen = ScreenBurn
				}
			}
		case ScreenBurn:
			switch ev.Key {
			case "esc":
				m.Screen = ScreenHome
				m.BurnTarget = ""
				m.BurnConfirmText = ""
			case "enter":
				if m.BurnConfirmText == m.BurnTarget {
					var remaining []client.HandleDTO
					for _, h := range m.Handles {
						if h.Handle != m.BurnTarget {
							remaining = append(remaining, h)
						}
					}
					m.Handles = remaining
					if m.SelectedHandle >= len(m.Handles) && m.SelectedHandle > 0 {
						m.SelectedHandle--
					}
					m.BurnTarget = ""
					m.BurnConfirmText = ""
					m.Screen = ScreenHome
				}
			default:
				m.BurnConfirmText = ev.Key
			}
		}
	}
	return m
}

// View renders the current TUI state to a string.
func (m Model) View() string {
	var b strings.Builder

	switch m.Screen {
	case ScreenKeyWall:
		b.WriteString("┌────────────────────────────────────────────────────────────────┐\n")
		b.WriteString("│  Halp — First Run & Account Key                                │\n")
		b.WriteString("└────────────────────────────────────────────────────────────────┘\n\n")
		b.WriteString("  " + client.FormatAccountKey(m.AccountKey) + "\n\n")
		b.WriteString("  " + KeyWallLine1 + "\n")
		b.WriteString("  " + KeyWallLine2 + "\n\n")
		checkMark := " "
		if m.KeySavedChecked {
			checkMark = "x"
		}
		b.WriteString(fmt.Sprintf("  [ %s ] %s\n\n", checkMark, KeyWallCheckbox))
		if m.KeySavedChecked {
			b.WriteString("  [ Continue (Press Enter) ]\n")
		} else {
			b.WriteString("  (Tick the checkbox with Space to enable Continue)\n")
		}

	case ScreenBurn:
		b.WriteString("┌────────────────────────────────────────────────────────────────┐\n")
		b.WriteString("│  Burn Handle                                                   │\n")
		b.WriteString("└────────────────────────────────────────────────────────────────┘\n\n")
		b.WriteString("  " + BurnPrompt(m.BurnTarget) + "\n\n")
		b.WriteString(fmt.Sprintf("  Type %s to confirm: %s\n", m.BurnTarget, m.BurnConfirmText))

	case ScreenHome:
		if m.EnableBell && m.BellRungCount > 0 {
			b.WriteString("\a")
		}
		b.WriteString(fmt.Sprintf("Halp [%s]\n", m.ConnectionState))
		b.WriteString("──────────────────────────────────────────────────────────────────\n")
		b.WriteString(fmt.Sprintf("Today: %s\n\n", client.FormatArrivalCopy(m.TodayCount)))
		b.WriteString("Handles:\n")
		if len(m.Handles) == 0 {
			b.WriteString("  (No handles created yet)\n")
		} else {
			for i, h := range m.Handles {
				cursor := "  "
				if i == m.SelectedHandle {
					cursor = "> "
				}
				pausedTag := ""
				if h.Paused {
					pausedTag = " [paused]"
				}
				label := h.Label
				if label == "" {
					label = h.Kind
				}
				b.WriteString(cursor + h.Handle + " (" + label + ")" + pausedTag + "\n")
			}
		}
		b.WriteString("\nSend appreciation:\n")
		b.WriteString(fmt.Sprintf("  [ %s ]\n", m.SendState))
		if m.SendError != "" {
			b.WriteString(fmt.Sprintf("  %s\n", m.SendError))
		}
		b.WriteString("\n  " + KeyWallLine1 + "\n")
	}

	return b.String()
}
