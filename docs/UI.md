# UI and UX

The shared reference for every client surface in phase 1: web app, the Chrome
and Firefox extensions, the Tauri desktop app, and the public pages the server
renders itself.

**This document is the spec, not a suggestion.** Copy, states and affordances
here are product decisions with privacy consequences behind them — several of
them are the *only* thing standing between an implementation and a violation of
invariant 6 or 7. An agent building a surface implements what is written here.
If something is missing or wrong, change this file in the same PR and say why
([AGENTS.md](../AGENTS.md), "Before you change the spec").

## Principles

1. **One sentence, one button.** The product is a single verb. Every screen that
   is not the send button is settings, and settings should look like it.
2. **The sender learns nothing.** Not from copy, not from a spinner that runs
   longer, not from an error state that only appears sometimes. See
   [Uniformity](#uniformity-is-a-ui-requirement).
3. **The recipient never sees a person.** No names, no avatars, no "from", no
   inference. Counts and channels only.
4. **Public pages work without JavaScript.** `/h/{handle}` is opened by link
   previewers, hardened browsers and people on trains.
5. **No account furniture.** There is no email field, no password field, no
   profile, no display name outside a group roster. Do not add one "for
   convenience".
6. **Nothing is recoverable, so say so early and in plain words.** The key wall
   is the most important screen in the product.

## Uniformity is a UI requirement

Invariant 7 says enforcement is invisible to the sender. That is mostly a client
concern, because the server already returns an identical `202`:

* The send button has **exactly two** end states: *sending* and **`Sent.`**
  ([decision 7](OPEN-QUESTIONS.md#decided)). There is no "delivered", no
  checkmark that means more than another, no "you have sent 2 of 3 today".
* Do not surface the pair limit, the rate limit, a block, a paused handle, a
  burned handle or an unknown handle. The client cannot distinguish them and
  must not invent a distinction.
* Do not render a client-side quota, cooldown timer or "you already appreciated
  this person today" hint. A local counter is an oracle with extra steps.
* **Fix the perceived latency.** Proof-of-work takes ~10 ms and the POST takes
  under 3 ms, but an unknown handle, a blocked sender and a delivered ping must
  *look* identical. Hold the sending state for a fixed floor (300 ms) and show
  `Sent.` on completion of the POST regardless of what the POST returned, errors
  included. A network failure is the one exception and says so plainly.
* Error copy is generic: **"Something went wrong. Try again."** Never echo a
  status code, never distinguish 4xx from 5xx to the user.

## The surfaces

### 1. Handle page — `GET /h/{handle}`, `GET /@{alias}`

Server-rendered, no framework, no JS required for the layout. See
[SHARING.md](SHARING.md).

```
                Someone appreciates you.

              ┌───────────────────────────┐
              │      Appreciate them      │
              └───────────────────────────┘

         Anonymous. No message. They never learn it was you.
```

* Nothing identifies the owner: no label, no count, no created date, no
  "this handle is busy".
* A handle that does not exist, is paused, or has blocked the viewer renders
  **byte-identical** output with the same timing. There is no 404 page for a
  handle.
* With JS: the button provisions an account in the background on first visit
  (proof-of-work with a progress bar, key into `localStorage`), then sends.
  After the first send, reveal the key once — non-blocking, dismissible, with
  "keep this if you want your settings to follow you".
* Without JS: the button is a plain `POST` form. The signup PoW cannot run, so
  the no-JS path renders the same page with the button linking to the web app.
  Do not fall back to a sender-less send.
* OpenGraph and Twitter card tags, fixed for every handle — the preview must not
  differ between a real handle and a typo.

### 2. First run and the key wall

Shown once, after signup, and reachable forever from settings.

```
  6421  8830  5197  4462

  [ Copy ]   [ Download as text ]

  This is your account. There is no email, no password and no reset.
  If you lose it, the account is gone — handles, settings and all.

  [ x ] I have written it down
                               [ Continue ]
```

* `Continue` is disabled until the checkbox is ticked. This is the one place in
  the product where a deliberate speed bump is correct.
* 16 digits, grouped in four, in a monospaced face. Never in a URL, a QR code, a
  share sheet or a screenshot-friendly toast. See [IDENTITY.md](IDENTITY.md).
* Repeat the "there is no recovery" sentence at login and in settings. Three
  times is not too many.

### 3. Login

One field accepting 16 digits with whitespace ignored and auto-grouping as
typed. A PoW progress bar on every attempt. A wrong key and a correct key that
hit the rate limiter return the **same** message after the same delay:

> **That didn't work.** Check the digits and try again.

No "account not found". No attempt counter. No lockout notice.

### 4. Receiving — the home view

The only number the recipient ever sees is a count.

* **Today:** `7 people appreciated you.` Copy scales with `n` exactly as in
  [DELIVERY.md](DELIVERY.md) (1 / 2–20 / 21+).
* Per-handle list: label, kind, its counters, paused state. This is where the
  product earns the multi-handle design — *"your Twitch handle is taking 4,000
  an hour and your GitHub one is taking six"*.
* When the server has auto-escalated a handle
  ([ABUSE.md](ABUSE.md) layer 4), show it as an explanation and not an alarm:

  > **This handle is unusually busy.** We've widened its digest window so your
  > notifications stay reasonable. You can change it below.

* There is no feed, no timeline, no per-ping list. There is nothing to list.

### 5. Handles

| Action | UI |
| --- | --- |
| Create | Label (free text, local) + kind (`personal` / `social` / `stream`) |
| Share | Copy link, QR (PNG + SVG, generated client-side), badge markdown |
| Pause | A toggle. Instant, reversible, no confirm |
| Burn | Destructive dialog, confirm by typing the handle |

Burn copy, verbatim:

> **Burn `e7k4p2m9qx3v`?** Anyone with this link or QR code loses the ability to
> reach you, immediately and permanently. The handle is never reissued and this
> cannot be undone. Your other handles are unaffected.

Kind selection changes the pair limit
([ABUSE.md](ABUSE.md)), so label it in user terms — *"a stream handle expects a
crowd"* — not by the config key.

### 6. Settings

Six controls, per account, each overridable per handle
([DELIVERY.md](DELIVERY.md)). Present them as presets, not numbers:

| Setting | UI |
| --- | --- |
| `digest_window_s` | Instant · Every minute · Every 15 min · Hourly · Daily |
| `max_per_hour` | Slider 1–60, default 12 |
| Quiet hours | Two times + timezone, off by default |
| `min_count` | "Only tell me when at least N people have" |
| `mode` | Everything · Groups only · Paused |

State plainly that escalation only ever goes upward: *"If a handle gets flooded
we may widen its window on its own. We will never make it narrower than you
asked."*

### 7. Report abuse

One button on a handle, a confirm dialog, then a terminal state. It must never
report what it did, because what it did names people
([ABUSE.md](ABUSE.md) layer 3).

> **Done.** It should quieten down. If it doesn't, pause or burn this handle —
> that always works.

No count of blocked senders. No "we blocked 3 accounts". No undo list.

### 8. Notifications

The service worker, the desktop app and the TUI render the same strings from the
same `n`. The server ships a number, never a string.

| n | Title |
| --- | --- |
| 1 | Someone appreciates you. |
| 2–20 | 7 people appreciate you. |
| 21+ | 412 people appreciate you. |

No body text, no action buttons, no reply affordance, no sender line. Tapping
opens the app to the home view.

### 9. Desktop app

Tray icon, native OS notification, a window that is the home view. Reconnect
silently with jittered backoff — a connection state indicator is fine, a
reconnect toast is noise. Launch-at-login is offered, never default-on.

### 10. Extension popup

The smallest surface: today's count, the send field (paste a handle or alias),
and a link into the web app for everything else. It shares the web codebase and
must not grow its own settings screen.

## Visual language

* System font stack. No web fonts on the public page — a font request is a third
  party watching who opened a handle link.
* Light and dark from `prefers-color-scheme`. One accent colour.
* No illustration of people, no avatars, no initials-in-a-circle. The product's
  whole claim is that there is no person to draw.
* **Motion respects `prefers-reduced-motion`.** The send confirmation has a
  fade; that is the budget.
* **Accessibility is not optional**: the send button is a real `<button>`,
  `Sent.` is announced via an ARIA live region, contrast meets WCAG AA, and
  every flow works from the keyboard. The handle page must be usable at 200%
  zoom on a phone.
* No analytics, no tag manager, no font CDN, no error-reporting SDK on any
  client surface. This is invariant-adjacent and it is not negotiable
  ([PRIVACY.md](PRIVACY.md)).

## What the UI must never show

A checklist to run against any screen before it ships:

- [ ] Anything that identifies or hints at a sender — name, count per sender,
      timing, ordering, "you have been appreciated by someone you know".
- [ ] Any distinction between accepted, rate-limited, deduped, blocked, paused
      and nonexistent.
- [ ] A ping list, history, feed or export of individual pings. There is no
      such data; a UI that implies it is a promise we will be asked to keep.
- [ ] A public received counter (deferred, [decision 8](OPEN-QUESTIONS.md#decided)).
- [ ] The account key anywhere it can be shared by accident.
- [ ] A leaderboard, streak, badge-for-sending or any other reason to send more.
