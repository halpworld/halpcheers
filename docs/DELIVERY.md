# Delivery and coalescing

## Transports

| Client | Transport | Notes |
| --- | --- | --- |
| Web app | Web Push (VAPID) + SSE when the tab is open | SW shows the notification when backgrounded |
| Chrome / Firefox extension | Web Push in the MV3 service worker | shares the web codebase |
| Safari | Web Push | Safari's support for the Push API *inside web extensions* is the weakest link; verify before committing. Fallback is Safari Web Push for the web app plus the desktop app. |
| Desktop (Tauri, Win/macOS/Linux) | SSE + native OS notification and tray | reconnect with jittered backoff |
| TUI (Go, Bubble Tea) | SSE | prints a line, optional terminal bell |
| OBS overlay | SSE, read-only | see [SHARING.md](SHARING.md) |
| Mobile | APNs / FCM | phase 2, not built |

**Web Push is payloadless by default.** The service worker already knows the only
sentence it will ever display, so a body is pure cost: RFC 8291 encryption is a
per-subscription ECDH and cannot be amortised across recipients. We attach a tiny
encrypted body (`{"n":42}`) only when a digest collapsed more than one ping and
the count is worth showing. Most deliveries therefore involve no crypto at all.

**SSE, not WebSockets.** One-directional is all we need, `EventSource` reconnects
by itself, it survives proxies, and it costs less per connection. Sends go over
ordinary POSTs on the same keep-alive connection pool.

## Coalescing

Nobody wants fifty separate notifications saying the same sentence. Collapsing
them is better product *and* it is our largest single lever on push volume and
CPU, so it is on by default for everyone.

**Mechanism.** A per-recipient accumulator plus one time wheel:

```
on ping(recipient):
    acc = accumulator[recipient]        // created on demand
    acc.count++
    if acc.flushAt == 0:
        acc.flushAt = now + policy(recipient).window
        wheel.schedule(recipient, acc.flushAt)

on flush(recipient):
    n = acc.count; delete accumulator[recipient]
    dispatch(recipient, n)
```

O(1) per ping. Memory is proportional to *recipients with pending pings*, not to
users or to traffic. The accumulator holds a count and two timestamps — no sender
list, no ping records.

**The accumulator doubles as the offline buffer.** A desktop client that
reconnects inside its digest window still gets its pings, as a number. A number
is not a message record.

**Copy scales with the count** — the client renders it, the server only ships `n`:

| n | Notification |
| --- | --- |
| 1 | Someone appreciates you. |
| 2–20 | 7 people appreciate you. |
| 21+ | 412 people appreciate you. |

## Delivery policy (the "someone famous shows up" case)

Per account, overridable per handle. Six small integers, stored in SQLite,
cached in memory:

| Setting | Default | Range |
| --- | --- | --- |
| `digest_window_s` | 60 | 0 (instant) · 60 · 900 · 3600 · 86400 |
| `max_per_hour` | 12 | 1 – 60 |
| `quiet_start` / `quiet_end` | off | local hour, accumulates instead of delivering |
| `tz` | from client | for quiet hours |
| `min_count` | 1 | suppress digests below a threshold |
| `mode` | `all` | `all` · `groups_only` · `paused` |

A streamer with 50k viewers sets their Twitch handle to a 1 h window and gets
"3,812 people appreciate you" once an hour, while their personal handle stays
instant. That is the configurability you asked for, and it lives per handle
precisely so one context going viral does not ruin the others.

**Automatic escalation overrides the user's choice upward, never downward.** If a
handle's inbound rate exceeds its cap or spikes far past its own baseline, the
server widens the window on its own and says so in the digest. A user who asked
for "instant" while receiving 4,000 pings a minute has asked for something that
is bad for them and bad for the box; we honour the intent (tell me promptly) not
the literal setting.

## Dispatch

* Bounded worker pool, each worker owning HTTP/2 clients with keep-alive to the
  push services. Per-endpoint-host connection reuse matters more than worker
  count.
* Jobs older than `dispatch.max_age` (30 s) are dropped, counted, not retried.
* One retry on 5xx/429 with the `Retry-After` honoured, then drop. We are not a
  reliable delivery system and should not behave like one.
* Deliveries fan out to every subscription on the account, plus every locally
  attached SSE connection.

## Token and subscription hygiene

Prune on authoritative rejection: Web Push `404` / `410 Gone`, and (phase 2)
APNs `410 Unregistered` / FCM `registration-token-not-registered`.

**The race that matters:** a device can re-register between our send and the
failure coming back. Delete by exact endpoint value **and** only when the
subscription row's `created_day` predates the send — otherwise a device that
just re-subscribed gets silently unsubscribed. Transient 5xx and network errors
never prune.

Also prune subscriptions with no successful delivery for 180 days, and on
account deletion.
