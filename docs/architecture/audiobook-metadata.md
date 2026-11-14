# Audiobook metadata ownership

Two writers fill an audiobook's `media_items` row. The scanner
(`internal/scanner/audiobook_scan.go`) reads the files' tags on every scan
that sees a change. The enricher (`internal/audiobooks/enrichment.go`) writes
a metadata provider's record once, when it matches the book. Each field has
one owner, so a rescan never leaves a book describing two sources.

## Matched means a provider identity

A book is provider-matched when it has a `media_item_provider_ids` row other
than `asin`, or its last enrichment outcome in `audiobook_enrichment_state` is
`success`. The scanner stores the ASIN tag as an `asin` row, so that row alone
is not a match. The enricher and an admin's Match Item write a provider's own
ID; the outcome covers a match that stored no such ID, such as a lookup by the
tagged ASIN or a match whose provider ID another item already owns.
`catalog.AudiobookProviderMatchedWhere` is the one predicate; the scanner and
the admin unmatched list both use it.

## Who owns which field

Admin field locks win over both writers.

| Field | Unmatched book | Matched book |
| --- | --- | --- |
| Title, original title, series | tags | tags (the enricher never writes them) |
| Year, genres, studios | tags; year follows the tag even when the tag is empty, genres and studios merge | provider; tags fill only an empty field |
| Authors, narrators | tags replace the stored credits when they differ | provider; tags add only a kind (author or narrator) the book has no credit for |
| Overview, release date, language | tags fill an empty field | provider; tags fill an empty field |
| Cover | the folder image or embedded art | unchanged; a provider poster fills only a missing cover |
| Runtime | the active files | the active files |

A match replaces credits per kind: only the kinds the provider names. A
provider without narrators keeps the narrator from the tags. The credits are
written in the same transaction as the rest of the match and its `success`
outcome, so a failure leaves the book unmatched and retryable. When the
enricher writes a poster it also writes that poster's thumbhash, empty for an
uncached remote URL, which image caching fills in later.

The enricher does not run again on a matched book. A book whose fields were
mixed before this rule existed stays that way until an admin runs Refresh
Metadata or Match Item.

## Unmatched audiobooks and `status`

Audiobooks keep `media_items.status` empty. Several consumers read
`'unmatched'` there with meanings that do not fit books: the audiobook skip
check re-probes such a folder on every scan, orphan cleanup deletes
provisional rows, and the movie and series match queues pick them up. The
admin unmatched-items list instead uses `catalog.AudiobookUnmatchedWhere`: a
book with no provider identity whose last enrichment outcome was `no_match`,
or which was stamped before `audiobook_enrichment_state` existed and has no
state row. A book in a library with no provider chain (`skipped`) or still
waiting for its first attempt is not listed. Rows from before the state table
recorded no outcome, so a book skipped back then is listed too: it has no
match either, and the enricher no longer revisits it. A match removes a book
from the list.
