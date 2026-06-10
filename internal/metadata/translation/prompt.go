package translation

import "fmt"

// metadataBatchSize is how many fields are translated per chat request.
// Smaller than the subtitle batch size — these are paragraphs, not cue lines —
// while still batching a series' episode overviews together so the model keeps
// terminology consistent across them.
const metadataBatchSize = 10

// systemPrompt builds the translation system prompt. title/year ground the
// model in which work the descriptions belong to so names and references stay
// untranslated and consistent.
func systemPrompt(srcName, tgtName, title string, year int) string {
	src := srcName
	if src == "" {
		src = "the source language"
	}
	work := title
	if year > 0 {
		work = fmt.Sprintf("%s (%d)", title, year)
	}
	return fmt.Sprintf(
		"You are a professional translator for a media catalog. Translate plot summaries and taglines "+
			"for %q from %s into %s. Produce natural, idiomatic %s that preserves meaning and tone, keeps "+
			"proper nouns, character names, and place names as they are conventionally rendered in %s, and "+
			"adds no information that is not in the source. "+
			"You receive a JSON object whose keys are entry numbers and whose values are the source text. "+
			"Respond with ONLY a JSON object using the exact same keys, where each value is the translation "+
			"of that entry. Do not add, remove, merge, or renumber entries, and do not output anything "+
			"except the JSON object.",
		work, src, tgtName, tgtName, tgtName,
	)
}
