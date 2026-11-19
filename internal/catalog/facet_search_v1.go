package catalog

// The /api/v1/catalog/filters/search answer is frozen: every facet returns
// names starting with the prefix, read live from the database and ordered
// by LOWER(name) under the database collation. The prefix is a LIKE
// pattern, so % and _ in it match any text. The queries below keep that
// answer; /api/v2 searches cached value lists instead (facet_values.go).
