/**
 * Whether the server reports this field ("overview" by default, or "tagline")
 * as machine-translated. A label names the field it sits on, so an AI tagline
 * never marks a provider or human overview.
 */
export function hasMachineTranslation(
  fields: readonly string[] | undefined,
  field: "overview" | "tagline" = "overview",
): boolean {
  return fields?.includes(field) ?? false;
}
