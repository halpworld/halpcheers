Output docs/assets/halp-tui-demo.gif

Set FontSize 16
Set Width 960
Set Height 520
Set Theme "Catppuccin Mocha"
Set Padding 24
Set WindowBar Colorful

Hide
Type "source /tmp/setup_demo.sh"
Enter
Sleep 1s
Type "clear"
Enter
Show

Sleep 800ms

# Scene 1: Send anonymous appreciation ping from terminal
Type "halp send e7k4p2m9qx3va"
Sleep 600ms
Enter
Sleep 2s

# Scene 2: Twitch/YouTube chat bot responding to !cheers
Type "echo ':viewer!viewer@tmi.twitch.tv PRIVMSG #stream :!cheers' | halp-bot --target @streamer"
Sleep 600ms
Enter
Sleep 2.5s

# Scene 3: Interactive TUI & Live SSE Ping Arrival
Hide
Type "(sleep 2.5 && halp send e7k4p2m9qx3va > /dev/null 2>&1) &"
Enter
Show
Type "halp"
Sleep 600ms
Enter
Sleep 5s
Ctrl+C
Sleep 1.5s
