import { formatLocale } from "./appLocale.ts";

const formatters = new Map<string, Intl.NumberFormat>();

/**
 * A number to show, with its fraction digits, in the app's format locale and
 * without digit grouping: `toFixed(maxFractionDigits)` in US English ("1234.5"),
 * "1234,5" in Spanish. With a smaller `minFractionDigits`, trailing zeros drop
 * as `String(Number(value.toFixed(max)))` drops them. Keep plain `toFixed` for
 * values that are stored, sent, compared or put in a URL.
 */
export function formatDecimal(
  value: number,
  maxFractionDigits: number,
  minFractionDigits = maxFractionDigits,
): string {
  const locale = formatLocale();
  const cacheKey = `${locale ?? ""}|${minFractionDigits}|${maxFractionDigits}`;
  let formatter = formatters.get(cacheKey);
  if (!formatter) {
    formatter = new Intl.NumberFormat(locale, {
      useGrouping: false,
      minimumFractionDigits: minFractionDigits,
      maximumFractionDigits: maxFractionDigits,
    });
    formatters.set(cacheKey, formatter);
  }
  // Round with toFixed, which rounds the exact binary value (1.005 → "1.00");
  // Intl rounds the shortest decimal form (→ "1.01"). The rounded number has no
  // more digits than the formatter keeps, so it only localizes.
  return formatter.format(Number(value.toFixed(maxFractionDigits)));
}
