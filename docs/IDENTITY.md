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

* **It never leaves the device.** The client derives two independent values from
  it and sends only the first:

  ```
  account_key  (16 digits, device only)
    ├─ auth_secret   = HKDF-SHA256(account_key, info="halp/auth/v1")      → sent at login, Argon2id-hashed server-side
    └─ contacts_key  = HKDF-SHA256(account_key, info="halp/contacts/v1")  → never leaves the device
  ```

  Domain separation is what makes "the server cannot read your contacts" true
  rather than aspirational: a server that logged every login body would learn
  `auth_secret`, which is enough to impersonate and useless for decryption. The
  first draft sent the raw account key to `POST /v1/session`; that would have
  made every derived key derivable server-side. See
  [DISCOVERY.md](DISCOVERY.md).
* Stored server-side only as an Argon2id hash of `auth_secret`. We cannot
  recover it, print it, or reset it. Losing it loses the account — say so in
  plain language at signup, at every login, and in the settings screen.
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
* **The alias namespace is global**, not per-region — `@kenth` means one person
  everywhere. It is the only globally replicated table in the system. See
  [DISCOVERY.md](DISCOVERY.md).
* **There is no resolve endpoint.** `POST /v1/ping/{target}` takes a handle or
  an alias and resolves internally, so probing the namespace costs exactly as
  much as sending — proof-of-work, buckets and pair filter included. A
  standalone lookup would be a free enumeration oracle.
* Reserved-name list (support, admin, halp, help, abuse, security, …) and a
  squatting rule (`alias.release_months`, default 12): an alias on an account
  with no activity for 12 months is released.
* One alias per account. Revisit if there is demand.

The rules above are settled ([decision 10](OPEN-QUESTIONS.md#decided)), and so
is how the global namespace stays unique across regions: a single **central
registry** serialises claims, running as its own small service rather than as a
promoted primary region ([decisions 11 and 12](OPEN-QUESTIONS.md#decided)). It
is peer-only over mTLS, with no public read path, because a public lookup there
would be the enumeration oracle we deleted above. See
[DISCOVERY.md](DISCOVERY.md).

## Regions

We will host in the EU and may add regions if one gets popular. The design is
**regional independence for account data, with one global namespace on top**:
each region is a standalone deployment with its own SQLite file, and the only
thing replicated everywhere is the opt-in `alias → handle` directory. Account
keys, subscriptions, settings, blocks and counters never leave their home
region, which keeps the transfer analysis in [PRIVACY.md](PRIVACY.md) short.

Nobody should ever have to know or pick a region to reach someone.
[DISCOVERY.md](DISCOVERY.md) is the full account of how that holds.

The first character of a handle is its home region (`e` = eu-1, `u` = us-1,
`a` = ap-1, …). Any region can therefore route a ping without shared state or a
lookup: if the prefix is not mine, forward the already-validated job over a
persistent mTLS HTTP/2 connection to the owning region and return 202. The
forwarded message contains a handle and nothing else — no sender, no IP, no
content — so cross-region traffic carries no personal data about the sender.

An account lives in exactly one region, chosen at signup (default: nearest, user
overridable). Region migration is out of scope for v1; the honest answer is
"create a new account and repoint your alias" — which works precisely because
the alias namespace is global and the repoint is visible everywhere.

Two caveats worth stating rather than burying. A handle's prefix reveals its
owner's region to anyone holding the handle; that is the price of lookup-free
routing and it is a coarse enough signal to accept. And a group hosted in
another region stores its foreign members' display names and group handles
there, so "your data never leaves your region" is not an accurate thing to
print — "your account lives in your region" is.

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
