package models

// EbookFinishedProgressThreshold is the reading-progress ratio (0..1) at or
// above which an ebook counts as finished/read. Progress strictly between 0
// and this threshold counts as in progress. Shared by the reader API,
// catalog query builder, sections, and recommendation signals so the
// definition of "finished" cannot drift between subsystems.
const EbookFinishedProgressThreshold = 0.9
