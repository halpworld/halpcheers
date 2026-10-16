package bot

import (
	"context"
	"strings"
	"sync"
	"time"
)

// PingSender provides an optional interface to forward appreciation pings directly to the API.
type PingSender interface {
	SendPing(ctx context.Context, target string) error
}

// Config configures a chat bot instance.
type Config struct {
	StreamTarget    string
	BaseURL         string
	ChannelCooldown time.Duration
	UserCooldown    time.Duration
	MaxTrackedUsers int
	ForwardPing     bool
	PingSender      PingSender
}

// Bot implements a thin API chat bot for live streaming platforms (Twitch IRC, YouTube chat).
//
// Invariants:
// - AGENTS.md Invariant 8: Bounded capacity for user cooldown tracking (MaxTrackedUsers).
// - AGENTS.md Invariant 9: No identifiers passed to logging/formatting functions.
// - docs/SHARING.md § Live streaming: Thin client of the public API with no special server support.
type Bot struct {
	StreamTarget    string
	BaseURL         string
	ChannelCooldown time.Duration
	UserCooldown    time.Duration
	MaxTrackedUsers int
	ForwardPing     bool
	PingSender      PingSender

	mu               sync.Mutex
	channelCooldowns map[string]time.Time
	userCooldowns    map[string]time.Time
}

// NewBot constructs a Bot with default fallbacks.
func NewBot(cfg Config) *Bot {
	target := strings.TrimSpace(cfg.StreamTarget)
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "https://halp.to"
	}
	chanCooldown := cfg.ChannelCooldown
	if chanCooldown <= 0 {
		chanCooldown = 15 * time.Second
	}
	userCooldown := cfg.UserCooldown
	if userCooldown <= 0 {
		userCooldown = 60 * time.Second
	}
	maxUsers := cfg.MaxTrackedUsers
	if maxUsers <= 0 {
		maxUsers = 10000
	}

	return &Bot{
		StreamTarget:     target,
		BaseURL:          baseURL,
		ChannelCooldown:  chanCooldown,
		UserCooldown:     userCooldown,
		MaxTrackedUsers:  maxUsers,
		ForwardPing:      cfg.ForwardPing,
		PingSender:       cfg.PingSender,
		channelCooldowns: make(map[string]time.Time),
		userCooldowns:    make(map[string]time.Time),
	}
}

// ParseTwitchIRC parses standard IRC PRIVMSG lines with or without IRCv3 @tags.
// Returns (channel, sender, text, ok).
func ParseTwitchIRC(line string) (channel, sender, text string, ok bool) {
	raw := strings.TrimRight(line, "\r\n")
	if raw == "" {
		return "", "", "", false
	}

	var tags string
	rest := raw
	if strings.HasPrefix(rest, "@") {
		spaceIdx := strings.Index(rest, " ")
		if spaceIdx == -1 {
			return "", "", "", false
		}
		tags = rest[1:spaceIdx]
		rest = strings.TrimLeft(rest[spaceIdx+1:], " ")
	}

	var prefix string
	if strings.HasPrefix(rest, ":") {
		spaceIdx := strings.Index(rest, " ")
		if spaceIdx == -1 {
			return "", "", "", false
		}
		prefix = rest[1:spaceIdx]
		rest = strings.TrimLeft(rest[spaceIdx+1:], " ")
	}

	spaceIdx := strings.Index(rest, " ")
	var command string
	var params string
	if spaceIdx == -1 {
		command = rest
	} else {
		command = rest[:spaceIdx]
		params = strings.TrimLeft(rest[spaceIdx+1:], " ")
	}

	if strings.ToUpper(command) != "PRIVMSG" {
		return "", "", "", false
	}

	colonIdx := strings.Index(params, " :")
	if colonIdx == -1 {
		colonIdx = strings.Index(params, ":")
		if colonIdx == -1 {
			return "", "", "", false
		}
		channel = strings.TrimSpace(params[:colonIdx])
		text = params[colonIdx+1:]
	} else {
		channel = strings.TrimSpace(params[:colonIdx])
		text = params[colonIdx+2:]
	}

	if tags != "" {
		for _, tag := range strings.Split(tags, ";") {
			if strings.HasPrefix(tag, "display-name=") {
				val := strings.TrimPrefix(tag, "display-name=")
				if val != "" {
					sender = val
					break
				}
			}
		}
	}

	if sender == "" && prefix != "" {
		if idx := strings.Index(prefix, "!"); idx != -1 {
			sender = prefix[:idx]
		} else {
			sender = prefix
		}
	}

	if channel == "" || sender == "" {
		return "", "", "", false
	}

	return channel, sender, text, true
}

// HandleMessage evaluates a chat message across platforms. If it matches a bot trigger
// command (!halp, !appreciate, !cheers) and cooldown checks pass, it returns the appreciation
// reply string and true. Otherwise, it silently returns ("", false).
func (b *Bot) HandleMessage(ctx context.Context, platform, channel, user, text string, now time.Time) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}

	cmd := strings.ToLower(fields[0])
	if cmd != "!halp" && cmd != "!appreciate" && cmd != "!cheers" {
		return "", false
	}

	b.mu.Lock()

	if b.channelCooldowns == nil {
		b.channelCooldowns = make(map[string]time.Time)
	}
	if b.userCooldowns == nil {
		b.userCooldowns = make(map[string]time.Time)
	}

	chanCooldown := b.ChannelCooldown
	if chanCooldown <= 0 {
		chanCooldown = 15 * time.Second
	}
	userCooldown := b.UserCooldown
	if userCooldown <= 0 {
		userCooldown = 60 * time.Second
	}
	maxUsers := b.MaxTrackedUsers
	if maxUsers <= 0 {
		maxUsers = 10000
	}

	chanKey := strings.ToLower(platform + ":" + channel)
	if lastChan, ok := b.channelCooldowns[chanKey]; ok {
		if now.Sub(lastChan) < chanCooldown {
			b.mu.Unlock()
			return "", false
		}
	}

	userKey := strings.ToLower(platform + ":" + user)
	if lastUser, ok := b.userCooldowns[userKey]; ok {
		if now.Sub(lastUser) < userCooldown {
			b.mu.Unlock()
			return "", false
		}
	}

	// Invariant 8: Bounded capacity for channel tracking
	if len(b.channelCooldowns) >= maxUsers {
		for k, t := range b.channelCooldowns {
			if now.Sub(t) >= chanCooldown {
				delete(b.channelCooldowns, k)
			}
		}
		if len(b.channelCooldowns) >= maxUsers {
			for k := range b.channelCooldowns {
				delete(b.channelCooldowns, k)
				break
			}
		}
	}

	// Invariant 8: Bounded capacity for user tracking
	if len(b.userCooldowns) >= maxUsers {
		// Prune expired entries
		for k, t := range b.userCooldowns {
			if now.Sub(t) >= userCooldown {
				delete(b.userCooldowns, k)
			}
		}

		// If still at capacity, evict oldest entry
		if len(b.userCooldowns) >= maxUsers {
			var oldestKey string
			var oldestTime time.Time
			first := true
			for k, t := range b.userCooldowns {
				if first || t.Before(oldestTime) {
					oldestTime = t
					oldestKey = k
					first = false
				}
			}
			if oldestKey != "" {
				delete(b.userCooldowns, oldestKey)
			}
		}
	}

	b.channelCooldowns[chanKey] = now
	b.userCooldowns[userKey] = now

	baseURL := strings.TrimRight(b.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://halp.to"
	}
	target := strings.TrimSpace(b.StreamTarget)
	forwardPing := b.ForwardPing
	pingSender := b.PingSender
	b.mu.Unlock()

	var link string
	if strings.HasPrefix(target, "@") {
		link = baseURL + "/" + target
	} else if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		link = target
	} else {
		link = baseURL + "/h/" + strings.TrimPrefix(target, "/")
	}

	reply := "Send anonymous appreciation (no login required): " + link

	if forwardPing && pingSender != nil {
		_ = pingSender.SendPing(ctx, target)
	}

	return reply, true
}

// TrackedUsersCount returns the current count of users in the cooldown map (used for tests).
func (b *Bot) TrackedUsersCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.userCooldowns)
}
