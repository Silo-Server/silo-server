import { messageNumberingSystem, t } from "@/i18n";
import { appLanguage } from "@/lib/appLocale";

let relative: { locale: string; format: Intl.RelativeTimeFormat } | null = null;

/**
 * One formatter per interface language and the digits messages use (Arabic
 * names them); cards call this in long grids.
 */
function relativeFormat(): Intl.RelativeTimeFormat {
  const numberingSystem = messageNumberingSystem();
  const locale = numberingSystem ? `${appLanguage()}-u-nu-${numberingSystem}` : appLanguage();
  if (relative?.locale !== locale) {
    relative = { locale, format: new Intl.RelativeTimeFormat(locale, { numeric: "always" }) };
  }
  return relative.format;
}

const MINUTE = 60;
const HOUR = 3600;
const DAY = 86400;

/**
 * Returns a human-readable relative time string like "Added 3 minutes ago".
 * Returns null for dates older than 30 days.
 */
export function timeAgo(iso: string): string | null {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return null;

  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (seconds < MINUTE) return t("web.item.card.addedJustNow");

  const added = (value: number, unit: Intl.RelativeTimeFormatUnit) =>
    t("web.item.card.addedAgo", { ago: relativeFormat().format(-value, unit) });
  if (seconds < HOUR) return added(Math.floor(seconds / MINUTE), "minute");
  if (seconds < DAY) return added(Math.floor(seconds / HOUR), "hour");

  const days = Math.floor(seconds / DAY);
  return days <= 30 ? added(days, "day") : null;
}
