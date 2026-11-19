import { appLanguage } from "@/lib/appLocale";

/** "Movies", "Movies and Kids", "Movies, Kids and 4K Movies", in the interface language. */
export function joinNames(names: readonly string[]): string {
  if (names.length <= 1) return names[0] ?? "";
  return nameListFormat().format(names);
}

/** joinNames in parts: each name ("element") and the words between them ("literal"). */
export function joinNameParts(
  names: readonly string[],
): ReadonlyArray<{ type: "element" | "literal"; value: string }> {
  if (names.length <= 1) return names.map((value) => ({ type: "element", value }));
  return nameListFormat().formatToParts(names);
}

function nameListFormat(): Intl.ListFormat {
  const language = appLanguage();
  let format = listFormats.get(language);
  if (!format) {
    // English keeps its list without the serial comma, as en-GB writes it.
    format = new Intl.ListFormat(language === "en" ? "en-GB" : language, {
      style: "long",
      type: "conjunction",
    });
    listFormats.set(language, format);
  }
  return format;
}

const listFormats = new Map<string, Intl.ListFormat>();
