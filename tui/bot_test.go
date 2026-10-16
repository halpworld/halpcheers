package tui_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/tui/internal/bot"
)

type mockPingSender struct {
	mu         sync.Mutex
	calls      int
	lastTarget string
}

func (m *mockPingSender) SendPing(ctx context.Context, target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastTarget = target
	return nil
}

func (m *mockPingSender) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *mockPingSender) Last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastTarget
}

func TestParseTwitchIRC(t *testing.T) {
	// Standard line without tags
	line1 := ":viewer123!viewer123@viewer123.tmi.twitch.tv PRIVMSG #streamer :!halp"
	ch1, sender1, text1, ok1 := bot.ParseTwitchIRC(line1)
	if !ok1 {
		t.Fatalf("expected line1 to parse successfully")
	}
	if ch1 != "#streamer" || sender1 != "viewer123" || text1 != "!halp" {
		t.Fatalf("line1 parse mismatch: ch=%s, sender=%s, text=%s", ch1, sender1, text1)
	}

	// Line with IRCv3 tags and display-name
	line2 := "@badge-info=;badges=broadcaster/1;display-name=StreamHost;emotes= :streamhost!streamhost@streamhost.tmi.twitch.tv PRIVMSG #streamhost :!appreciate you all"
	ch2, sender2, text2, ok2 := bot.ParseTwitchIRC(line2)
	if !ok2 {
		t.Fatalf("expected line2 with IRCv3 tags to parse successfully")
	}
	if ch2 != "#streamhost" || sender2 != "StreamHost" || text2 != "!appreciate you all" {
		t.Fatalf("line2 parse mismatch: ch=%s, sender=%s, text=%s", ch2, sender2, text2)
	}

	// Line with prefix without !user@host
	line3 := ":moderator PRIVMSG #general :!cheers!"
	ch3, sender3, text3, ok3 := bot.ParseTwitchIRC(line3)
	if !ok3 {
		t.Fatalf("expected line3 to parse")
	}
	if ch3 != "#general" || sender3 != "moderator" || text3 != "!cheers!" {
		t.Fatalf("line3 parse mismatch: ch=%s, sender=%s, text=%s", ch3, sender3, text3)
	}

	// Non-PRIVMSG lines
	badLines := []string{
		"PING :tmi.twitch.tv",
		":streamer!streamer@tmi.twitch.tv JOIN #channel",
		":streamer MODE #channel +o viewer",
		"",
	}
	for _, bl := range badLines {
		_, _, _, badOk := bot.ParseTwitchIRC(bl)
		if badOk {
			t.Fatalf("expected line %q to not be parsed as PRIVMSG", bl)
		}
	}
}

func TestHandleMessage_CommandsAndReplies(t *testing.T) {
	target := "e7k4p2m9qx3va"
	b := bot.NewBot(bot.Config{
		StreamTarget:    target,
		BaseURL:         "https://halp.to",
		ChannelCooldown: 10 * time.Second,
		UserCooldown:    30 * time.Second,
	})

	ctx := context.Background()
	now := time.Now()

	// !halp command
	reply, triggered := b.HandleMessage(ctx, "twitch", "#channel", "user1", "!halp", now)
	if !triggered {
		t.Fatalf("expected !halp to trigger bot")
	}
	expectedReply := "Send anonymous appreciation (no login required): https://halp.to/h/" + target
	if reply != expectedReply {
		t.Fatalf("expected reply %q, got %q", expectedReply, reply)
	}

	// Non-trigger message
	_, triggered = b.HandleMessage(ctx, "twitch", "#otherchan", "user2", "hello world", now)
	if triggered {
		t.Fatalf("expected normal message not to trigger bot")
	}
}

func TestHandleMessage_AliasTargetLink(t *testing.T) {
	aliasTarget := "@awesome_streamer"
	b := bot.NewBot(bot.Config{
		StreamTarget: aliasTarget,
		BaseURL:      "https://halp.to",
	})

	ctx := context.Background()
	reply, triggered := b.HandleMessage(ctx, "youtube", "live", "fan99", "!cheers", time.Now())
	if !triggered {
		t.Fatalf("expected !cheers to trigger")
	}
	expected := "Send anonymous appreciation (no login required): https://halp.to/@awesome_streamer"
	if reply != expected {
		t.Fatalf("expected reply %q, got %q", expected, reply)
	}
}

func TestHandleMessage_YouTubeChat(t *testing.T) {
	target := "e7k4p2m9qx3va"
	b := bot.NewBot(bot.Config{
		StreamTarget: target,
		BaseURL:      "https://halp.to",
	})

	ctx := context.Background()
	t0 := time.Now()

	// YouTube chat with case-insensitive command
	reply, triggered := b.HandleMessage(ctx, "youtube", "stream_xyz", "yt_user", "!APPRECIATE thanks for stream", t0)
	if !triggered {
		t.Fatalf("expected YouTube !APPRECIATE to trigger")
	}
	if !strings.Contains(reply, target) {
		t.Fatalf("reply missing target link: %s", reply)
	}
}

func TestHandleMessage_ChannelAndUserCooldowns(t *testing.T) {
	b := bot.NewBot(bot.Config{
		StreamTarget:    "e7k4p2m9qx3va",
		ChannelCooldown: 10 * time.Second,
		UserCooldown:    60 * time.Second,
	})

	ctx := context.Background()
	t0 := time.Now()

	// user1 in #main at t0 -> triggers
	_, ok := b.HandleMessage(ctx, "twitch", "#main", "user1", "!halp", t0)
	if !ok {
		t.Fatalf("initial message should trigger")
	}

	// user2 in #main at t0 + 5s -> blocked by channel cooldown
	_, ok = b.HandleMessage(ctx, "twitch", "#main", "user2", "!halp", t0.Add(5*time.Second))
	if ok {
		t.Fatalf("user2 should be blocked by channel cooldown (10s)")
	}

	// user2 in #main at t0 + 11s -> channel cooldown elapsed, triggers
	_, ok = b.HandleMessage(ctx, "twitch", "#main", "user2", "!halp", t0.Add(11*time.Second))
	if !ok {
		t.Fatalf("user2 at t0+11s should trigger after channel cooldown")
	}

	// user1 in #main at t0 + 25s -> channel cooldown elapsed, but user1 on 60s cooldown -> blocked
	_, ok = b.HandleMessage(ctx, "twitch", "#main", "user1", "!halp", t0.Add(25*time.Second))
	if ok {
		t.Fatalf("user1 should be blocked by user cooldown (60s)")
	}

	// user1 in #main at t0 + 65s -> user cooldown elapsed -> triggers
	_, ok = b.HandleMessage(ctx, "twitch", "#main", "user1", "!halp", t0.Add(65*time.Second))
	if !ok {
		t.Fatalf("user1 at t0+65s should trigger after user cooldown")
	}
}

func TestHandleMessage_BoundedUserMapEviction(t *testing.T) {
	// Invariant 8: fixed-size allocations for anything keyed by user input
	maxTracked := 3
	userCooldown := 10 * time.Second
	b := bot.NewBot(bot.Config{
		StreamTarget:    "e7k4p2m9qx3va",
		ChannelCooldown: 1 * time.Nanosecond, // disable channel cooldown for this test
		UserCooldown:    userCooldown,
		MaxTrackedUsers: maxTracked,
	})

	ctx := context.Background()
	t0 := time.Now()

	// Add 3 users at t0, t0+1s, t0+2s
	_, _ = b.HandleMessage(ctx, "twitch", "#c1", "userA", "!halp", t0)
	_, _ = b.HandleMessage(ctx, "twitch", "#c2", "userB", "!halp", t0.Add(1*time.Second))
	_, _ = b.HandleMessage(ctx, "twitch", "#c3", "userC", "!halp", t0.Add(2*time.Second))

	if count := b.TrackedUsersCount(); count != 3 {
		t.Fatalf("expected 3 tracked users, got %d", count)
	}

	// Add 4th user at t0+3s (all 3 still active/non-expired)
	// Bounded map must evict oldest (userA) so count does not exceed maxTracked (3)
	_, _ = b.HandleMessage(ctx, "twitch", "#c4", "userD", "!halp", t0.Add(3*time.Second))
	if count := b.TrackedUsersCount(); count > maxTracked {
		t.Fatalf("Invariant 8 violation: tracked users %d exceeded max %d", count, maxTracked)
	}

	// Check that evicted userA can trigger again immediately (not in cooldown map)
	_, ok := b.HandleMessage(ctx, "twitch", "#c5", "userA", "!halp", t0.Add(4*time.Second))
	if !ok {
		t.Fatalf("userA should have been evicted and able to trigger")
	}

	// Check expired entry pruning at t0 + 20s
	tLate := t0.Add(20 * time.Second)
	_, _ = b.HandleMessage(ctx, "twitch", "#c6", "userE", "!halp", tLate)
	if count := b.TrackedUsersCount(); count > maxTracked {
		t.Fatalf("Invariant 8 violation after expiry pruning: count %d > max %d", count, maxTracked)
	}
}

func TestHandleMessage_ForwardPing(t *testing.T) {
	mock := &mockPingSender{}
	target := "e7k4p2m9qx3va"

	// With ForwardPing disabled
	bNoForward := bot.NewBot(bot.Config{
		StreamTarget: target,
		ForwardPing:  false,
		PingSender:   mock,
	})
	ctx := context.Background()
	_, _ = bNoForward.HandleMessage(ctx, "twitch", "#chan", "u1", "!halp", time.Now())
	if mock.CallCount() != 0 {
		t.Fatalf("expected 0 calls when ForwardPing=false, got %d", mock.CallCount())
	}

	// With ForwardPing enabled
	bForward := bot.NewBot(bot.Config{
		StreamTarget: target,
		ForwardPing:  true,
		PingSender:   mock,
	})
	_, _ = bForward.HandleMessage(ctx, "twitch", "#chan", "u2", "!appreciate", time.Now())
	if mock.CallCount() != 1 {
		t.Fatalf("expected 1 call when ForwardPing=true, got %d", mock.CallCount())
	}
	if mock.Last() != target {
		t.Fatalf("expected target %s, got %s", target, mock.Last())
	}
}
