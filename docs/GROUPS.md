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

## Abuse considerations

A group is a list of people who can be reached, so it is an amplifier and needs
its own limits on top of the global ones in [ABUSE.md](ABUSE.md):

* Member → member: 1 ping per 24 h (the global pair limit, scoped to the group
  handle) plus a per-sender cap on *distinct* group members pinged per day, so
  nobody can spray the whole roster daily.
* Group-wide inbound cap, set by the owner.
* Max members per group (default 500) and max groups per account (default 20),
  both configurable — these are the anti-amplification ceilings, not product
  limits, and should be raised deliberately.
* Removing a member is instant and silent to them.
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
and set a minimum group size (default 5) below which group-scoped pings are
disabled with an honest explanation in the UI.
