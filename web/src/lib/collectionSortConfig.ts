import type { CollectionSortConfig, QuerySort } from "@/api/types";
import { getDefaultQuerySortOrder, getQuerySortOptions } from "@/lib/querySortOptions";

// COLLECTION_SOURCE_ORDER is the in-form sentinel for "no default sort" — the
// collection keeps the order its source supplied (MDBList list order, manual
// item order). Radix Select disallows empty-string item values, so the empty
// sort_config is mapped to this sentinel only inside the form.
export const COLLECTION_SOURCE_ORDER = "__source_order" as const;

export interface CollectionDefaultSortOption {
  value: string;
  label: string;
}

/**
 * Options for a collection's default sort. Personalized sorts (progress, date
 * viewed, plays) are per-profile and only offered on personal collections;
 * library collections resolve without a viewer in scope, so the backend rejects
 * them there.
 */
export function collectionDefaultSortOptions(
  allowPersonalized: boolean,
): CollectionDefaultSortOption[] {
  return [
    { value: COLLECTION_SOURCE_ORDER, label: "Collection order (default)" },
    ...getQuerySortOptions({ includePersonalized: allowPersonalized }).map((option) => ({
      value: `${option.value}:${option.defaultOrder}`,
      label: option.label,
    })),
  ];
}

/** Read the form's select value out of a stored sort_config. */
export function sortConfigToSelectValue(config: CollectionSortConfig | undefined | null): string {
  const field = typeof config?.field === "string" ? config.field.trim() : "";
  if (!field) return COLLECTION_SOURCE_ORDER;
  const order =
    config?.order === "asc" || config?.order === "desc"
      ? config.order
      : getDefaultQuerySortOrder(field);
  return `${field}:${order}`;
}

/**
 * Build the sort_config to send. Returns `{}` for source order so an existing
 * default is cleared rather than left in place — the backend treats `{}` and an
 * absent field identically.
 */
export function selectValueToSortConfig(value: string): CollectionSortConfig {
  if (!value || value === COLLECTION_SOURCE_ORDER) return {};
  const [field, order] = value.split(":");
  if (!field) return {};
  return {
    field,
    order: order === "asc" || order === "desc" ? order : getDefaultQuerySortOrder(field),
  };
}

/**
 * Return a sort_config update only when the form control actually changed.
 * Omitting the field preserves legacy/unknown modes (for example
 * {"mode":"manual_pins"}) while an explicit source-order selection still
 * clears the stored config with `{}`.
 */
export function changedSortConfig(
  initialValue: string,
  currentValue: string,
): CollectionSortConfig | undefined {
  if (currentValue === initialValue) return undefined;
  return selectValueToSortConfig(currentValue);
}

/** The catalog filter bar's sort state expressed as a collection sort value. */
export function querySortToSelectValue(sort: QuerySort | undefined | null): string {
  const field = typeof sort?.field === "string" ? sort.field.trim() : "";
  if (!field) return COLLECTION_SOURCE_ORDER;
  return `${field}:${sort?.order || getDefaultQuerySortOrder(field)}`;
}
