package catalog

// AudiobookProviderMatchedWhere is true when a metadata provider or an admin
// matched the audiobook. Any one of these marks it:
//   - a provider ID other than the ASIN the scanner reads from tags;
//   - a last enrichment outcome of success, which covers an automatic match
//     that stored only the ASIN (a lookup by the tagged ASIN, or a provider
//     ID another item already owns);
//   - status 'matched', which Match Item and Refresh Metadata write through
//     the metadata pipeline in the same transaction as the match, whatever
//     IDs it carries.
//
// While it holds, the provider owns year, credits, genres and studios, and a
// rescan only fills what is empty. The scanner and the admin unmatched list
// both use this predicate.
func AudiobookProviderMatchedWhere(contentIDExpr string) string {
	return `(EXISTS (
		SELECT 1 FROM media_item_provider_ids abm
		WHERE abm.content_id = ` + contentIDExpr + `
		  AND abm.provider <> 'asin'
	) OR EXISTS (
		SELECT 1 FROM audiobook_enrichment_state abm_state
		WHERE abm_state.content_id = ` + contentIDExpr + `
		  AND abm_state.outcome = 'success'
	) OR EXISTS (
		SELECT 1 FROM media_items abm_item
		WHERE abm_item.content_id = ` + contentIDExpr + `
		  AND abm_item.status = 'matched'
	))`
}

// AudiobookUnmatchedWhere is true for an audiobook that has no provider
// identity after enrichment finished without one: its last outcome was
// no_match or failed (providers kept failing), or it was stamped before audiobook_enrichment_state existed and
// has no state row. Such old rows recorded no outcome, so a book skipped for
// want of a provider chain back then is listed too. Audiobooks keep
// media_items.status empty, because the scanner's skip check, orphan cleanup
// and the movie and series matcher read 'unmatched' there; this predicate
// stands in for it.
func AudiobookUnmatchedWhere(alias string) string {
	return alias + `.type = 'audiobook'
		AND NOT ` + AudiobookProviderMatchedWhere(alias+".content_id") + `
		AND (
			EXISTS (
				SELECT 1 FROM audiobook_enrichment_state abs_state
				WHERE abs_state.content_id = ` + alias + `.content_id
				  AND abs_state.outcome IN ('no_match', 'failed')
			)
			OR (
				` + alias + `.last_refreshed IS NOT NULL
				AND NOT EXISTS (
					SELECT 1 FROM audiobook_enrichment_state abs_state
					WHERE abs_state.content_id = ` + alias + `.content_id
				)
			)
		)`
}
