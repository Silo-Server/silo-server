package catalog

// AudiobookProviderMatchedWhere is true when the audiobook has an identity
// from a metadata provider: a provider ID other than the ASIN the scanner
// reads from tags. The audiobook enricher and an admin's Match Item both write
// one. While it holds, the provider owns year, credits, genres and studios,
// and a rescan only fills what is empty.
func AudiobookProviderMatchedWhere(contentIDExpr string) string {
	return `EXISTS (
		SELECT 1 FROM media_item_provider_ids abm
		WHERE abm.content_id = ` + contentIDExpr + `
		  AND abm.provider <> 'asin'
	)`
}

// AudiobookUnmatchedWhere is true for an audiobook that has no provider
// identity after enrichment finished without one: its last outcome was
// no_match, or it was stamped before audiobook_enrichment_state existed and
// has no state row. Such old rows recorded no outcome, so a book skipped for
// want of a provider chain back then is listed too. Audiobooks keep media_items.status empty, because the
// scanner's skip check, orphan cleanup and the movie and series matcher read
// 'unmatched' there; this predicate stands in for it.
func AudiobookUnmatchedWhere(alias string) string {
	return alias + `.type = 'audiobook'
		AND NOT ` + AudiobookProviderMatchedWhere(alias+".content_id") + `
		AND (
			EXISTS (
				SELECT 1 FROM audiobook_enrichment_state abs_state
				WHERE abs_state.content_id = ` + alias + `.content_id
				  AND abs_state.outcome = 'no_match'
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
