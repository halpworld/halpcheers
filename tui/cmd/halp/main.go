package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/halpworld/halpcheers/tui/internal/client"
	"github.com/halpworld/halpcheers/tui/internal/ui"
)

func defaultKeyFilePath() string {
	cfgDir, err := os.UserConfigDir()
	if err != nil || cfgDir == "" {
		cfgDir = os.TempDir()
	}
	return filepath.Join(cfgDir, "halp", "tui_account.enc")
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("halp", flag.ContinueOnError)
	serverURL := fs.String("server", envOrDefault("HALP_SERVER", "https://halp.to"), "Halp server base URL")
	keyFile := fs.String("key-file", envOrDefault("HALP_KEY_FILE", defaultKeyFilePath()), "Encrypted account key path")
	bell := fs.Bool("bell", true, "Ring terminal bell on incoming appreciation ping")

	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	c := client.New(*serverURL, nil)
	rest := fs.Args()

	// Subcommand: `halp send <handle-or-alias>`
	if len(rest) >= 1 && rest[0] == "send" {
		if len(rest) < 2 {
			return fmt.Errorf("usage: halp send <handle-or-alias>")
		}
		target := rest[1]
		if key, err := client.LoadAccountKeyEncrypted(*keyFile); err == nil && key != "" {
			_ = c.LoginWithAccountKey(ctx, key)
		}
		if err := c.SendPing(ctx, target); err != nil {
			fmt.Println(ui.SendErrorCopy)
			return nil
		}
		fmt.Println(ui.SendStateSent)
		return nil
	}

	// Interactive TUI / SSE Listener mode
	existingKey, _ := client.LoadAccountKeyEncrypted(*keyFile)
	isNewAccount := existingKey == ""
	if isNewAccount {
		if serverKey, err := c.CreateAccount(ctx, 0); err == nil && serverKey != "" {
			existingKey = serverKey
		}
	}
	model := ui.NewModel(existingKey, *bell)
	if isNewAccount {
		model.Screen = ui.ScreenKeyWall
	}

	if model.Screen == ui.ScreenKeyWall {
		fmt.Print(model.View())
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("\nType 'yes' once you have written down your 16-digit key: ")
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) != "yes" {
			return fmt.Errorf("key confirmation required before continuing")
		}
		model = model.Update(ui.KeyMsg{Key: "space"})
		model = model.Update(ui.KeyMsg{Key: "enter"})
		_ = client.SaveAccountKeyEncrypted(*keyFile, model.AccountKey)
	}

	_ = c.LoginWithAccountKey(ctx, model.AccountKey)
	handles, err := c.ListHandles(ctx)
	if err == nil && len(handles) == 0 {
		if h, createErr := c.CreateHandle(ctx, "Terminal", "personal"); createErr == nil && h.Handle != "" {
			handles = []client.HandleDTO{h}
		}
	}
	model.Handles = handles
	if pending, err := c.GetPending(ctx); err == nil && pending > 0 {
		model = model.Update(ui.ArrivalMsg{Count: pending})
	}

	model = model.Update(ui.ConnectionStatusMsg{Status: "connected"})
	fmt.Print(model.View())

	// Stream SSE events with jittered exponential backoff
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		err := c.StreamSSE(ctx, func(ev client.StreamEvent) {
			attempt = 0
			if ev.Type == "ping" && ev.Count > 0 {
				model = model.Update(ui.ArrivalMsg{Count: ev.Count})
				bellPrefix := ""
				if *bell {
					bellPrefix = "\a"
				}
				fmt.Printf("%s%s\n", bellPrefix, client.FormatArrivalCopy(ev.Count))
			}
		})
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			delay := client.CalculateJitteredBackoff(attempt, 1*time.Second, 30*time.Second)
			attempt++
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
		}
	}
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
