# Groups

**Goal:** join your dev team, find a colleague easily, appreciate them. The ping
stays anonymous — the group only solves *discovery*, never attribution.

## Model

A group is a small directory. Joining it mints you a **group-scoped handle**
that exists only inside that group.

```
Kenth joins  #acme-dev
  → mints handle  e5t9w2k7m4xq   visible only to #acme-dev members
  → listed in the roster as       "Kenth (backend)"
Kenth leaves #acme-dev
  → that handle is burned; nobody from the group can reach him again
```

This is the whole reason groups are safe to build. Membership *is* reachability,
leaving *is* revocation, and neither touches your personal handles. It also means
a group roster leak exposes handles that die the moment their owners leave.

## Roster

Members see: display name, optional role/note, and the group-scoped handle.
Members never see: account keys, other handles, aliases, who pinged whom, or
per-member received counts. A roster lookup is a lookup, not a subscription —
there is no presence, no last-seen, no activity feed.

## Joining

* Group owner generates an **invite code** (rotatable, expirable, optionally
  single-use, stored hashed). No open directory of groups, no discovery, no
  search across groups. If you did not get an invite you cannot find the group.
* Joining requires an account and costs proof-of-work.
* Display name is chosen by the member, subject to a per-group uniqueness check
  and the same reserved-word list as aliases.

## Administration

| Capability | Owner | Admin | Member |
| --- | --- | --- | --- |
| Rotate/revoke invite code | ✓ | ✓ | |
| Remove a member (burns their group handle) | ✓ | ✓ | |
| Rename group | ✓ | ✓ | |
| Set group-wide rate policy | ✓ | ✓ | |
| Transfer ownership | ✓ | | |
| Delete group (burns all group handles) | ✓ | | |
| View roster | ✓ | ✓ | ✓ |
| Leave | | ✓ | ✓ |

Owner departure with no transfer promotes the longest-tenured admin, else the
group is archived after 30 days.

## Groups span regions

A group lives in one region; its members do not have to. Your dev team can be
half in eu-1 and half in ap-1 and nobody has to know that.

* The group record and roster live in the **group's** home region.
* Joining mints your group-scoped handle at **your own** region, so it carries
  your prefix and self-routes like any other handle. Pinging a teammate needs
  no directory and no cross-region lookup.
* Your account, subscriptions and settings never leave your region. The group's
  region holds only what a roster needs: display name, note, and the
  group-scoped handle.
* Roster reads are a small, cacheable cross-region read.
* Leaving burns the handle at your region and drops the roster row at theirs;
  both sides are reconciled so a partitioned region cannot resurrect a
  departed member.

**Say this at the join screen:** *"This group is hosted in ap-1. Your display
name and group handle will be stored there."* It is user-initiated and minimal,
but it is real, and it is the reason the marketing line is "your account lives
in your region" rather than "your data never leaves your region". It belongs in
the data inventory in [PRIVACY.md](PRIVACY.md).

## Abuse considerations

A group is a list of people who can be reached, so it is an amplifier and needs
its own limits on top of the global ones in [ABUSE.md](ABUSE.md):

* Member → member: the global pair limit, scoped to the group handle
  (`guard.pair.max`, default 3 per 24 h — see [ABUSE.md](ABUSE.md)), plus a
  per-sender cap on *distinct* group members pinged per day, so nobody can spray
  the whole roster daily.
* Group-wide inbound cap, set by the owner.
* Max members per group (default 500) and max groups per account (default 20),
  both configurable — these are the anti-amplification ceilings, not product
  limits, and should be raised deliberately.
* Removing a member is instant and silent to them.
* Cross-region groups do not get a bigger budget. Rate limits apply at the
  recipient's own region, which sees all traffic to its own handles no matter
  where it entered the system.
* Report-abuse inside a group additionally offers "mute this group", which is
  just pausing the group-scoped handle.

## Explicitly not in scope

No group chat, no reactions, no leaderboards, no "most appreciated teammate of
the week". Every one of those turns an anonymous kindness tool into a
popularity contest, and a leaderboard reintroduces attribution by inference in a
small team. If a group has four people and one gets a ping, a leaderboard tells
everyone almost exactly who sent it.

**Small-group deanonymisation is a real risk even without leaderboards.** In a
three-person group, a recipient can often guess the sender. Mitigations: never
expose per-member received counts, never expose timing beyond the digest window,
and set a minimum group size below which group-scoped pings are disabled with an
honest explanation in the UI.

That floor is `groups.min_size`, default **5**. It is deliberately **operator**
configuration and not a per-group admin setting: it exists to protect members
from deanonymisation by inference, and the three-person group whose admin would
want to switch it off is exactly the case it is there for.
