package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/halpworld/halpcheers/tui/internal/bot"
	"github.com/halpworld/halpcheers/tui/internal/client"
)

func main() {
	targetFlag := flag.String("target", "", "Streamer destination handle or @alias (e.g. e7k4p2m9qx3va or @streamer)")
	serverFlag := flag.String("server", "https://halp.to", "Halp server base URL")
	cooldownFlag := flag.Duration("cooldown", 15*time.Second, "Channel cooldown duration")
	userCooldownFlag := flag.Duration("user-cooldown", 60*time.Second, "Per-user cooldown duration")
	forwardPingFlag := flag.Bool("forward-ping", false, "Forward appreciation ping to server when command triggers")

	ircChannelFlag := flag.String("irc-channel", "", "Optional Twitch IRC channel to join (e.g. #streamer). If omitted, reads from stdin.")
	ircNickFlag := flag.String("irc-nick", "justinfan12345", "Twitch IRC nickname (default read-only justinfan)")
	ircPassFlag := flag.String("irc-pass", "oauth:anonymous", "Twitch IRC OAuth token / password")
	ircServerFlag := flag.String("irc-server", "irc.chat.twitch.tv:6697", "Twitch IRC TLS server host:port")

	flag.Parse()

	dest := strings.TrimSpace(*targetFlag)
	if dest == "" {
		_, _ = os.Stderr.WriteString("Error: --target is required (e.g. --target e7k4p2m9qx3va or --target @streamer)\n")
		os.Exit(1)
	}

	var sender bot.PingSender
	if *forwardPingFlag {
		sender = client.New(*serverFlag, nil)
	}

	b := bot.NewBot(bot.Config{
		StreamTarget:    dest,
		BaseURL:         *serverFlag,
		ChannelCooldown: *cooldownFlag,
		UserCooldown:    *userCooldownFlag,
		MaxTrackedUsers: 10000,
		ForwardPing:     *forwardPingFlag,
		PingSender:      sender,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if *ircChannelFlag != "" {
		runIRC(ctx, b, *ircServerFlag, *ircChannelFlag, *ircNickFlag, *ircPassFlag)
	} else {
		runStdin(ctx, b)
	}
}

func runStdin(ctx context.Context, b *bot.Bot) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		ch, ircSender, text, ok := bot.ParseTwitchIRC(line)
		if ok {
			reply, triggered := b.HandleMessage(ctx, "twitch", ch, ircSender, text, time.Now())
			if triggered && reply != "" {
				_, _ = os.Stdout.WriteString(reply + "\n")
			}
			continue
		}

		user := "chat"
		msg := line
		if colon := strings.Index(line, ": "); colon != -1 {
			user = strings.TrimSpace(line[:colon])
			msg = strings.TrimSpace(line[colon+2:])
		}

		reply, triggered := b.HandleMessage(ctx, "chat", "general", user, msg, time.Now())
		if triggered && reply != "" {
			_, _ = os.Stdout.WriteString(reply + "\n")
		}
	}
}

func runIRC(ctx context.Context, b *bot.Bot, serverAddr, channelName, nick, pass string) {
	ch := channelName
	if !strings.HasPrefix(ch, "#") {
		ch = "#" + ch
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
	}
	conn, err := dialer.DialContext(ctx, "tcp", serverAddr)
	if err != nil {
		_, _ = os.Stderr.WriteString("Failed to connect to Twitch IRC: " + err.Error() + "\n")
		return
	}
	defer conn.Close()

	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)

	_, _ = writer.WriteString("PASS " + pass + "\r\n")
	_, _ = writer.WriteString("NICK " + nick + "\r\n")
	_, _ = writer.WriteString("CAP REQ :twitch.tv/tags twitch.tv/commands\r\n")
	_, _ = writer.WriteString("JOIN " + ch + "\r\n")
	_ = writer.Flush()

	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()

	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "PING ") {
			pongMsg := "PONG " + line[5:] + "\r\n"
			_, _ = writer.WriteString(pongMsg)
			_ = writer.Flush()
			continue
		}

		chanTarget, ircSender, text, ok := bot.ParseTwitchIRC(line)
		if !ok {
			continue
		}

		reply, triggered := b.HandleMessage(ctx, "twitch", chanTarget, ircSender, text, time.Now())
		if triggered && reply != "" {
			_, _ = os.Stdout.WriteString(reply + "\n")
			ircOut := "PRIVMSG " + chanTarget + " :" + reply + "\r\n"
			_, _ = writer.WriteString(ircOut)
			_ = writer.Flush()
		}
	}
}

// Invariant 9 verification safety check
var _ = fmt.Println
