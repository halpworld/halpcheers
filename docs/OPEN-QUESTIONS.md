# Open questions

Decisions still outstanding, and assumptions made in the plan that are worth
challenging. Resolve the blocking ones before Phase 1 code.

## Blocking Phase 1

1. **Safari Web Push inside web extensions.** Support for the Push API in Safari
   web extensions is the weakest assumption in the whole platform plan. Verify
   against current Apple documentation and a real device before promising Safari
   extension support. Fallback: Safari Web Push for the web app plus the desktop
   app, and no Safari extension.
2. **RFC 8291 ephemeral key reuse.** If an application server may reuse its ECDH
   keypair per subscription, the shared secret can be cached and the per-message
   cost collapses to AES-GCM. If it may not, the payloadless-by-default strategy
   carries the CPU budget on its own. Read the RFC and decide; do not guess.
3. **Pair-limit window.** 1 ping per person per 24 h is proposed because it
   makes the signal mean something. It also means a team of five can send you at
   most five pings a day, which may be too tight, and a conference QR code may
   want a different rule. Confirm 24 h, or make it per-handle-kind.
4. **`expected_daily_pings` at launch.** Sizes the Bloom filter allocation. A
   guess is fine; an unbounded structure is not.
5. **Domain.** `halp.to` is a placeholder used throughout the docs.

## Product

6. **Digest defaults.** 60 s window / 12 per hour is a guess. It should probably
   be validated with a handful of real users before it becomes the default
   everyone inherits.
7. **Does the sender see anything at all?** Currently a uniform 202 and a
   "sent!" animation, whether or not it was delivered. Honest alternative: "sent
   — they may receive it as part of a digest". Silent-drop-on-block is a
   deliberate anti-abuse choice; be comfortable that it also means an
   occasional honest sender is lied to.
8. **Public received counter.** Opt-in on the badge. Does it turn appreciation
   into a scoreboard, which the non-goals list explicitly rejects elsewhere?
9. **Minimum group size of 5** to prevent deanonymisation by inference. Is that
   the right number, and is disabling group pings below it too blunt?
10. **Alias policy.** One per account, 12-month squatting release, reserved
    list. All guesses.

## Operational

11. **Funding.** There is no revenue model here and the non-goals forbid the
    usual ones. A $10–20 VPS is cheap, but decide now whether this is a hobby
    project, donation-funded, or something else — it changes how much the
    100M/day ceiling is worth engineering for.
12. **Region set and hosting provider.** EU-first is decided; which provider,
    and what the backup/restore story is for the SQLite file (it holds the only
    copy of every account's handles).
13. **Legal review.** Push-service transfer analysis, age policy, and the ToS.
    Named in [PRIVACY.md](PRIVACY.md), not yet done.
14. **Abuse escalation path.** Everything is automatic by design. Who looks at
    the dashboard when the automatic response is not enough, and what manual
    levers exist?

## Deferred

15. Region migration for an existing account. Out of scope for v1; current
    answer is "make a new account and repoint your alias".
16. Account recovery. There is none, by design. Revisit only if user research
    says lost keys are killing retention — and if so, the fix is better key
    backup UX, not a recovery backdoor.
