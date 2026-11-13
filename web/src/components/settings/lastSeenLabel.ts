import { t } from "@/i18n";

/** Plain relative time; the exact timestamp is not what anyone is asking. */
export function lastSeenLabel(iso: string, now: number): string {
  const seen = Date.parse(iso);
  if (Number.isNaN(seen)) return t("web.settings.deviceList.lastSeen.never");
  const minutes = Math.max(0, Math.round((now - seen) / 60000));
  if (minutes < 60) return t("web.settings.deviceList.lastSeen.underAnHour");
  const hours = Math.round(minutes / 60);
  if (hours < 24)
    return t("web.settings.deviceList.lastSeen.hours", { count: hours, count_text: hours });
  const days = Math.round(hours / 24);
  if (days === 1) return t("web.settings.deviceList.lastSeen.yesterday");
  if (days < 30)
    return t("web.settings.deviceList.lastSeen.days", { count: days, count_text: days });
  const months = Math.round(days / 30);
  if (months < 12)
    return t("web.settings.deviceList.lastSeen.months", {
      count: months,
      count_text: months,
    });
  const years = Math.round(months / 12);
  return t("web.settings.deviceList.lastSeen.years", { count: years, count_text: years });
}
