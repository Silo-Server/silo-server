import type { QuerySort } from "@/api/types";
import { getDefaultQuerySortOrder } from "@/lib/querySortOptions";

// COLLECTION_SOURCE_ORDER is the in-form sentinel for "no default sort" — the
// collection keeps the order its source supplied (MDBList list order, manual
// item order). Radix Select disallows empty-string item values, so the empty
// sort_config is mapped to this sentinel only inside the form.
export const COLLECTION_SOURCE_ORDER = "__source_order" as const;

/** The catalog filter bar's sort state expressed as a collection sort value. */
export function querySortToSelectValue(sort: QuerySort | undefined | null): string {
  const field = typeof sort?.field === "string" ? sort.field.trim() : "";
  if (!field) return COLLECTION_SOURCE_ORDER;
  return `${field}:${sort?.order || getDefaultQuerySortOrder(field)}`;
}
