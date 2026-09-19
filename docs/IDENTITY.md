# Identity

Modelled on Mullvad: no email, no password, no profile. You get a number.

The one thing Mullvad's model does not cover is that a Halp identifier is meant
to be **posted in public**. Mullvad's account number is a secret credential; a
Halp handle goes on your GitHub README. Those cannot be the same string. So
there are three kinds of identifier, with three different jobs.

## 1. Account key — secret, permanent, never shared

```
6421 8830 5197 4462
```

16 digits, cryptographically random, displayed in groups of four. This is the
login credential and the only proof that the account is yours. It is generated
client-side-visible at signup and shown once, with a copy button and a "write
this down" wall the user must acknowledge.

* Stored server-side only as an Argon2id hash. We cannot recover it, print it,
  or reset it. Losing it loses the account — say so in plain language at signup,
  at every login, and in the settings screen.
* Never appears in a URL, a QR code, a share link, a log line or a metric label.
* Logging in with it is rate-limited hard and costs proof-of-work on every
  attempt. 16 digits is ~53 bits, which is ample offline but thin against a
  determined online guesser, so the online path is where the protection lives:
  per-IP leaky bucket, global failed-attempt budget, escalating PoW difficulty,
  and no oracle in the error message (a bad key and a good key with a tripped
  bucket return the same response after the same delay).

Signup costs a one-off proof-of-work of ~1–2 s. The user experiences it as a
progress bar on first launch. It is the main brake on mass account creation.

## 2. Handle — public, shareable, disposable

```
e7k4p2m9qx3v          →   https://halp.to/h/e7k4p2m9qx3v
```

13 characters of Crockford base32. The first character encodes the home region
(see below); the remaining 12 carry 60 bits of entropy, which makes the space
unscannable in practice. Handles are the *only* thing a sender ever sees.

**An account may hold many handles, and this is the point.** A handle is a
posting context, not an identity:

| Handle | Where it lives |
| --- | --- |
| `e7k4p2m9qx3v` | GitHub README |
| `e9m2x7t4k8wp` | Twitch panel |
| `e3v8q5n2j7rd` | the `#dev-team` group (auto-created on join) |

Each one can be **paused** (stops accepting pings, keeps existing) or **burned**
(deleted permanently, immediately 404s, never reissued). Because handles are
per-context, burning the one you posted on a forum that turned hostile costs you
that forum and nothing else. This is the product's real block button: you cannot
block an anonymous sender, but you can revoke the channel they reached you
through.

Each handle carries its own delivery policy override and its own counters, so
the UI can tell you *"your Twitch handle is taking 4,000 pings an hour and your
GitHub one is taking six"* without ever recording who sent them.

## 3. Alias — optional, human-readable, guessable

```
@kenth               →   resolves to a handle you nominate
```

A vanity label so people can find you without a QR code. Aliases are opt-in and
carry more risk than handles precisely because they are guessable — anyone can
try `@john`. Therefore:

* An alias **points at a handle**, it does not replace one. Repoint it at a
  fresh handle and every previously-harvested reference dies with the old one.
* Aliases get their own, stricter inbound rate limit and their own pause switch,
  independent of the handle behind them.
* Resolution (`GET /v1/resolve/{alias}`) is heavily rate-limited per sender and
  per IP, and returns the same timing and response shape for "no such alias" and
  "alias paused", so it cannot be used to enumerate who exists.
* Reserved-name list (support, admin, halp, help, abuse, security, …) and a
  squatting rule: an alias on an account with no activity for 12 months is
  released.
* One alias per account to start. Revisit if there is demand.

## Regions

We will host in the EU and may add regions if one gets popular. The design is
**regional independence, not replication**: each region is a complete standalone
deployment with its own SQLite file. No user data crosses a border at rest,
which keeps the transfer analysis in [PRIVACY.md](PRIVACY.md) short.

The first character of a handle is its home region (`e` = eu-1, `u` = us-1,
`a` = ap-1, …). Any region can therefore route a ping without shared state or a
lookup: if the prefix is not mine, forward the already-validated job over a
persistent mTLS HTTP/2 connection to the owning region and return 202. The
forwarded message contains a handle and nothing else — no sender, no IP, no
content — so cross-region traffic carries no personal data about the sender.

An account lives in exactly one region, chosen at signup (default: nearest, user
overridable). Region migration is out of scope for v1; the honest answer is
"create a new account and repoint your alias".

## Sender identity

**Sending requires an account.** This is not visible friction — a first-time
visitor who lands on `halp.to/h/<handle>` gets an account provisioned in the
background (PoW, key stored in `localStorage`, shown to them afterwards with
"here's your key, keep it if you want to keep your settings"). But it means
every ping has a stable server-side sender for the duration of the rate-limit
window, which is what makes [ABUSE.md](ABUSE.md) possible at all.

Be precise in the marketing and the privacy policy: Halp is **anonymous to the
recipient**, not anonymous to the service. The server transiently knows that
account A pinged handle H. It does not write that down.
