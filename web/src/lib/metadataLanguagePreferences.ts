import { normalizeLanguageCode } from "@/lib/languageNames";

/** Valid private-use BCP 47 tag understood by the server as item-original. */
export const ORIGINAL_METADATA_LANGUAGE = "x-silo-original";

export type MetadataLanguageOverrides = Record<string, string>;

/**
 * Treat the settings response as untrusted JSON at the component boundary.
 * The server validates writes, but this also keeps a stale/broken cached value
 * from making the editor unusable.
 */
export function normalizeMetadataLanguageOverrides(value: unknown): MetadataLanguageOverrides {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return {};

  const normalized: MetadataLanguageOverrides = {};
  for (const [source, target] of Object.entries(value)) {
    const sourceCode = normalizeLanguageCode(source);
    if (!/^[a-z]{2,3}$/.test(sourceCode) || typeof target !== "string" || !target.trim()) {
      continue;
    }
    normalized[sourceCode] = target.trim();
  }
  return normalized;
}

export function withMetadataLanguageOverride(
  overrides: MetadataLanguageOverrides,
  source: string,
  target: string,
): MetadataLanguageOverrides {
  const sourceCode = normalizeLanguageCode(source);
  if (!/^[a-z]{2,3}$/.test(sourceCode) || !target.trim()) return overrides;
  return { ...overrides, [sourceCode]: target.trim() };
}

export function withoutMetadataLanguageOverride(
  overrides: MetadataLanguageOverrides,
  source: string,
): MetadataLanguageOverrides {
  const next = { ...overrides };
  delete next[normalizeLanguageCode(source)];
  return next;
}
