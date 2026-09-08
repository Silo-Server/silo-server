import notoSansArabicUrl from "@fontsource/noto-sans-arabic/files/noto-sans-arabic-arabic-400-normal.woff?url";
import notoSansArabicLatinExtUrl from "@fontsource/noto-sans-arabic/files/noto-sans-arabic-latin-ext-400-normal.woff?url";
import notoSansArabicLatinUrl from "@fontsource/noto-sans-arabic/files/noto-sans-arabic-latin-400-normal.woff?url";
import notoSansThaiLatinExtUrl from "@fontsource/noto-sans-thai/files/noto-sans-thai-latin-ext-400-normal.woff?url";
import notoSansThaiLatinUrl from "@fontsource/noto-sans-thai/files/noto-sans-thai-latin-400-normal.woff?url";
import notoSansThaiUrl from "@fontsource/noto-sans-thai/files/noto-sans-thai-thai-400-normal.woff?url";

/**
 * A fallback font for a writing system that JASSUB's built-in default
 * (Liberation Sans, Latin-only) cannot render.
 *
 * JASSUB/libass renders missing glyphs with its `defaultFont` — the font used
 * whenever a subtitle style's named font is absent OR lacks glyphs for the text
 * being drawn. This libass build does NOT search other loaded fonts for glyph
 * coverage, so merely adding a font (via `fonts`/`availableFonts`) does nothing
 * unless a style names it; the font has to be the `defaultFont`. A single font
 * can't cover every script, so we pick the default per track/script.
 */
export interface SubtitleFallbackFont {
  /** Font family key, lower-cased to match JASSUB's case-insensitive lookup. */
  family: string;
  /** Font file URLs preloaded into JASSUB for this family. */
  urls: string[];
}

const NOTO_SANS_ARABIC: SubtitleFallbackFont = {
  family: "noto sans arabic",
  urls: [notoSansArabicUrl, notoSansArabicLatinUrl, notoSansArabicLatinExtUrl],
};

const NOTO_SANS_THAI: SubtitleFallbackFont = {
  family: "noto sans thai",
  urls: [notoSansThaiUrl, notoSansThaiLatinUrl, notoSansThaiLatinExtUrl],
};

// Normalized ISO-639 language code (2- and 3-letter forms) -> fallback font
// whose script the Latin default cannot render. Prefer per-language gating like
// this over a single global default, especially for large CJK fonts.
const FALLBACK_BY_LANGUAGE: Record<string, SubtitleFallbackFont> = {
  ar: NOTO_SANS_ARABIC,
  ara: NOTO_SANS_ARABIC,
  th: NOTO_SANS_THAI,
  tha: NOTO_SANS_THAI,
};

const FALLBACK_BY_SCRIPT: Array<{ pattern: RegExp; font: SubtitleFallbackFont }> = [
  // Arabic, Arabic Supplement, Arabic Extended-A, Arabic Presentation Forms.
  {
    pattern: /[\u0600-\u06ff\u0750-\u077f\u08a0-\u08ff\ufb50-\ufdff\ufe70-\ufeff]/,
    font: NOTO_SANS_ARABIC,
  },
  { pattern: /[\u0e00-\u0e7f]/, font: NOTO_SANS_THAI },
];

/**
 * Returns the font to use as JASSUB's `defaultFont` for a subtitle track in the
 * given language, or null to keep JASSUB's built-in Latin default.
 */
export function fallbackFontForLanguage(language: string | undefined): SubtitleFallbackFont | null {
  if (!language) return null;
  return FALLBACK_BY_LANGUAGE[language.toLowerCase()] ?? null;
}

/**
 * Returns a fallback font by explicit language first, then by scanning subtitle
 * text for scripts that need a non-Latin default.
 */
export function fallbackFontForSubtitle(
  language: string | undefined,
  subtitleContent: string,
): SubtitleFallbackFont | null {
  const languageFont = fallbackFontForLanguage(language);
  if (languageFont) return languageFont;

  return FALLBACK_BY_SCRIPT.find(({ pattern }) => pattern.test(subtitleContent))?.font ?? null;
}

const fontDataCache = new Map<string, Promise<Uint8Array[]>>();
const MAX_FONT_BUNDLE_CACHE_ENTRIES = 4;
const MAX_FONT_BUNDLE_CACHE_BYTES = 64 * 1024 * 1024;

interface FontBundleCacheEntry {
  promise: Promise<Uint8Array[]>;
  bytes: number;
}

const fontBundleCache = new Map<string, FontBundleCacheEntry>();

interface SubtitleFontBundleItem {
  name: string;
  data: string;
}

export function loadSubtitleFallbackFontData(font: SubtitleFallbackFont): Promise<Uint8Array[]> {
  const cached = fontDataCache.get(font.family);
  if (cached) return cached;

  const promise = Promise.all(
    font.urls.map(async (url) => {
      const response = await fetch(url);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      return new Uint8Array(await response.arrayBuffer());
    }),
  ).catch((err) => {
    fontDataCache.delete(font.family);
    throw err;
  });
  fontDataCache.set(font.family, promise);
  return promise;
}

export function loadSubtitleFontBundle(url: string, signal?: AbortSignal): Promise<Uint8Array[]> {
  const cached = fontBundleCache.get(url);
  if (cached) {
    fontBundleCache.delete(url);
    fontBundleCache.set(url, cached);
    return cached.promise;
  }

  const entry: FontBundleCacheEntry = {
    promise: Promise.resolve([]),
    bytes: 0,
  };
  const promise = fetch(url, { signal })
    .then(async (response) => {
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      return (await response.json()) as SubtitleFontBundleItem[];
    })
    .then((items) => items.map((item) => base64ToBytes(item.data)))
    .then((fonts) => {
      entry.bytes = totalByteLength(fonts);
      if (entry.bytes > MAX_FONT_BUNDLE_CACHE_BYTES) {
        fontBundleCache.delete(url);
      } else {
        evictFontBundleCache();
      }
      return fonts;
    });

  // Do not poison the cache with transient network errors or aborted requests.
  const cachedPromise = promise.catch((err) => {
    fontBundleCache.delete(url);
    throw err;
  });
  entry.promise = cachedPromise;
  fontBundleCache.set(url, entry);
  evictFontBundleCache();
  return cachedPromise;
}

function totalByteLength(chunks: Uint8Array[]): number {
  return chunks.reduce((total, chunk) => total + chunk.byteLength, 0);
}

function evictFontBundleCache(): void {
  while (fontBundleCache.size > MAX_FONT_BUNDLE_CACHE_ENTRIES) {
    const oldest = fontBundleCache.keys().next().value;
    if (!oldest) return;
    fontBundleCache.delete(oldest);
  }

  let total = 0;
  for (const entry of fontBundleCache.values()) {
    total += entry.bytes;
  }

  while (total > MAX_FONT_BUNDLE_CACHE_BYTES) {
    const oldest = fontBundleCache.entries().next().value;
    if (!oldest) return;
    const [url, entry] = oldest;
    fontBundleCache.delete(url);
    total -= entry.bytes;
  }
}

function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/**
 * Forces ASS style font names and inline \fn overrides to the selected fallback
 * family. Without this, libass repeatedly asks for unavailable style fonts such
 * as Trebuchet MS before falling back, which both logs noisily and can leave
 * non-Latin glyphs boxed if fallback fonts are not loaded yet.
 */
export function forceASSFontFamily(content: string, family: string): string {
  const normalized = content.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  let inStyleSection = false;
  let fontNameIndex = -1;

  return normalized
    .split("\n")
    .map((line) => {
      const section = line.match(/^\s*\[([^\]]+)]\s*$/);
      if (section) {
        const sectionName = section[1]!.trim().toLowerCase();
        inStyleSection = sectionName === "v4+ styles" || sectionName === "v4 styles";
        fontNameIndex = -1;
        return line;
      }

      if (inStyleSection) {
        const format = line.match(/^(\s*Format\s*:\s*)(.*)$/i);
        if (format) {
          const fields = format[2]!.split(",").map((field) => field.trim().toLowerCase());
          fontNameIndex = fields.indexOf("fontname");
          return line;
        }

        const style = line.match(/^(\s*Style\s*:\s*)(.*)$/i);
        if (style && fontNameIndex >= 0) {
          const fields = style[2]!.split(",");
          if (fontNameIndex < fields.length) {
            fields[fontNameIndex] = family;
          }
          return `${style[1]}${fields.join(",")}`;
        }
      }

      return line.replace(/\\fn[^\\}]*/g, `\\fn${family}`);
    })
    .join("\n");
}
