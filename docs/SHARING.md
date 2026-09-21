# Sharing your handle

The point of a handle is to be posted. Making that one tap is a core feature,
not a nicety.

## Link

```
https://halp.to/h/e7k4p2m9qx3v
```

Opening it in any browser gives a page with one large button. A first-time
visitor is provisioned an account in the background (proof-of-work, key kept in
`localStorage`, revealed afterwards), so sending works in a single tap with no
signup wall while still being rate-limitable per sender.

The page must render without JavaScript for the preview crawlers, and must not
leak anything about the owner: no display name, no counts, no "this handle does
not exist" distinction from "this handle is paused" (uniform response, uniform
timing).

Rich previews: OpenGraph and Twitter card tags so a pasted link looks right in
Slack, Discord and social feeds.

### Alias links

```
https://halp.to/@kenth
```

Same page, same one-tap send, but memorable enough to say out loud. Aliases are
one flat namespace, so an alias link works from
anywhere without the sender knowing or caring where the recipient's account
lives. See [DISCOVERY.md](DISCOVERY.md).

The page must render identically for an alias that does not exist, is paused,
or belongs to someone who has blocked the viewer. There is no resolve endpoint
and no validity check: a typo looks exactly like a success. That is deliberate,
because a guessable global namespace with a free existence oracle would be
scraped within a week.

## QR code

Generated client-side from the handle URL — the server never needs to be
involved. Offered as PNG and SVG, with a printable card layout (conference
badge, desk sign, sticker) and a "new handle for this QR" shortcut so a QR you
printed for one event can be burned afterwards.

## GitHub / README badge

```markdown
[![Halp](https://halp.to/badge/e7k4p2m9qx3v.svg)](https://halp.to/h/e7k4p2m9qx3v)
```

An SVG served with shields-style caching. Note the constraint: GitHub proxies
images through Camo, so the badge **cannot** be a working button and cannot see
the viewer — it is an image that links to the handle page. The click is where
the ping happens.

**The badge is plain — "appreciate me" and nothing else.** A public received
count is deferred out of phase 1
([decision 8](OPEN-QUESTIONS.md#decided)): the open question of whether it turns
appreciation into a scoreboard has not been answered, and shipping it is the
hard half to undo.

The server keeps counting anyway. `accounts.recv_total` is a single aggregate
integer, incremented from the first ping, exposed to nobody. That is not
hedging: there are no ping records, so a counter that is not maintained from day
one can never be reconstructed. Cache headers must be long enough that a popular
README does not become a traffic source of its own.

## Live streaming

Phase 3, but it shapes the design now because it is the same fan-out.

* **OBS browser source.** A URL with a read-only overlay token (not the handle,
  so it can be revoked without changing the handle) that subscribes over SSE and
  renders an animation or a counter. Configurable: pop-in, meter, subtle corner
  count. This is the same SSE hub the desktop client uses.
* **Digest-driven, always.** A streamer's handle runs a short digest window so
  the overlay animates once with "37 people appreciate you" rather than 37
  times. Directly reuses [DELIVERY.md](DELIVERY.md).
* **Chat bot.** Twitch/YouTube bot that posts the streamer's handle link on
  command. Keep it a thin client of the public API — no special server support.
* **Stream-specific handle**, burnable after a raid goes wrong, which is the
  single most likely abuse scenario in this whole product.

Cost note: a live stream is precisely the "one handle, enormous inbound"
scenario, so the automatic escalation in [DELIVERY.md](DELIVERY.md) and the
per-handle PoW escalation in [ABUSE.md](ABUSE.md) must both be in place before
this ships. Do not build the overlay first.
