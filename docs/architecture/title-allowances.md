# Title allowances

A title allowance lets a limited profile watch a specific movie or series above
its maturity limits (content-rating ceiling and advisory-age limit). The
household's primary profile grants it, one title at a time or through a
personal collection. The API is described in
[catalog-api.md](../catalog-api.md#allowed-titles).

## Rule

A title passes a profile's maturity limits when it is within them **or** it is
allowed. In SQL, `catalog.ApplyMaturityLimits` renders
`((limits) OR <key> = ANY(allowed))`, where `<key>` is the row's `content_id`.
Rows keyed by their series (the `episode_catalog_entries` read model) go
through `catalog.ApplyEpisodeMaturityLimits`, which keys on `series_id`, so a
series allowance covers every season and episode. Extras have no rating of
their own and follow their title.

An allowance lifts only the profile's own maturity limits. Library access,
hidden libraries and every other restriction still apply, and the API refuses
an allowance for a title the profile cannot reach through its libraries. Two
stricter limits also win over it:

- A custom scope policy that sets a content-rating override or a different
  advisory-age limit for the profile (`policy.maturityOverridden`): the
  administrator's limit applies and no allowance does.
- A Jellyfin client's `MaxOfficialRating` that narrows the ceiling
  (`jellycompat.clampMaturityLimits`): the client asked for less than the
  profile may see, so its allowed titles stop passing too.

An allowance names a title, not a rating: it survives a metadata refresh that
changes the title's rating. A limited profile never sees a title it is not
already allowed, so there is no request-to-watch flow.

## Where the list comes from

`access.MaturityLimits.AllowedTitleIDs` carries the allowed titles with the
rest of the limits, so every catalog read, cache key and cursor that already
copies the limits picks them up. Scope resolution fills it from
`userstore.Profile.AllowedTitleIDs`, which the single-profile read in `pgstore`
loads in the same statement as the profile row
(`pgstore.ProfileAllowedTitleIDsSQL`). It is set only while the limits are
active (`MaturityLimits.WithAllowedTitles`), so an unrestricted scope keeps
the same fingerprint and cache keys as before.

A code path that builds limits without the list is stricter than intended,
never looser: it hides allowed titles and exposes nothing.

The list is bounded, because every request the profile makes reads it and
passes it to the catalog SQL. The read is a `UNION ALL` with a `LIMIT` and no
`ORDER BY`, so PostgreSQL stops once it has `userstore.MaxAllowedTitles`
(10,000) rows rather than reading every member of every live collection. Each
title comes back once, so the bound counts distinct titles: the collection
branch leaves out a title the profile is allowed directly or an earlier live
collection (by id) holds, and reads only whole-title entries
(`sub_item_id = ''`), as collection membership does elsewhere. Below the bound
the list is exact. A profile allowed more sees at most 10,000, which ones is
unspecified and can change between reads, and the rest stay hidden, so the
bound fails closed. The bound caps the rows the read returns, not the
entries it scans: an entry left out as a repeat is still read, so a profile
whose live collections overlap heavily can scan up to their total membership
before it has 10,000 titles. Those are manual collections the primary
profile fills by hand, and each scanned entry costs index probes. Writes are
not capped: checking a live collection's future size at write time would be
imprecise and racy.

The list is the union of:

- `profile_title_allowances`: one row per allowed movie or series.
- The titles of each collection in `profile_collection_allowances`, read only
  while the collection is `manual` and its creator is the account's current
  primary profile. Only the household manager can then change what it holds.
  A collection that stops meeting either condition allows nothing until it
  does again.

Copying a collection (`AllowCollectionTitles`) writes individual title
allowances and does not follow later changes. A smart collection stores no
items, so its copy is what its query matches for the household manager now.

When titles merge (provider-ID merges and catalog reattribution), title
allowances move to the surviving title like favorites and ratings do.

## Shared personal collections

A shared collection shows titles both its owner and the viewer may access
(`catalog.IntersectAccess`). When only one side has limits, that side's
allowances apply. When both do, only titles both allowed pass the combined
limits. That is narrower than each side's own rule but never wider, so one
profile's allowance cannot lift another profile's limit.

## Writes

Every write that changes a row advances the account's `access_policy_revision`
in the same transaction. Membership changes to a live-allowed collection do
not; the allowed list itself is part of the access scope, cache keys
(`AccessFilter.WriteAccessScopeCacheKey`) and v2 cursor digests, so those
change with it.

Allowance rows cascade away with their profile, title or collection. Writes go
through `pgstore.TitleAllowances`, a store over the shared tables rather than
optional methods on the per-account user store, so a decorated user-store
provider (the notifications interest wrapper) cannot hide it. It is wired only
when the user store keeps accounts in the same PostgreSQL database
(`StoresAccountsIn`); otherwise the capability reports `unsupported` and no
allowance can apply.

## Backups

A full restore carries both tables like the rest of the server's data. The
restore authority check (`backup.profileLimits.looserThan`) reads each
profile's allowed titles with the same SQL as the access scope and counts a
title allowed in the backup but not on the live server as a loosening, like a
lifted ceiling. A single-account restore and a profile export leave
allowances out; the household manager grants them again.

## Playback and downloads

Item access is checked by the same SQL before playback or a download starts.
The policy engine's `content_rating_within_ceiling` input is not set by any
live caller, so allowances need nothing there.
