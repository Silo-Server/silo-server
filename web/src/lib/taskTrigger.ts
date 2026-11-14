import type { TriggerConfig } from "@/api/types";
import { t as appT, type MessageKey } from "@/i18n";

/** Interval units, largest first; each names its short and long message. */
const UNITS: { size: number; short: MessageKey; long: MessageKey }[] = [
  {
    size: 86_400_000,
    short: "web.admin.tasks.triggerText.everyDaysShort",
    long: "web.admin.tasks.triggerText.everyDays",
  },
  {
    size: 3_600_000,
    short: "web.admin.tasks.triggerText.everyHoursShort",
    long: "web.admin.tasks.triggerText.everyHours",
  },
  {
    size: 60_000,
    short: "web.admin.tasks.triggerText.everyMinutesShort",
    long: "web.admin.tasks.triggerText.everyMinutes",
  },
  {
    size: 1000,
    short: "web.admin.tasks.triggerText.everySecondsShort",
    long: "web.admin.tasks.triggerText.everySeconds",
  },
];

/**
 * Describes one task trigger. The task list uses the short form ("Every 3h",
 * "On startup"); detail pages spell it out ("Every 3 hour(s)", "On server
 * startup").
 */
export function describeTrigger(t: TriggerConfig, style: "long" | "short" = "long"): string {
  const short = style === "short";
  switch (t.type) {
    case "interval": {
      const ms = t.interval_ms ?? 0;
      const unit = UNITS.find(
        ({ size }) => ms >= size && ms % size === 0 && (short || size < 86_400_000),
      );
      // Zero reads as seconds; an interval no unit divides stays in milliseconds.
      const [key, count]: [MessageKey, number] = unit
        ? [short ? unit.short : unit.long, ms / unit.size]
        : ms === 0
          ? [
              short
                ? "web.admin.tasks.triggerText.everySecondsShort"
                : "web.admin.tasks.triggerText.everySeconds",
              0,
            ]
          : [
              short
                ? "web.admin.tasks.triggerText.everyMillisecondsShort"
                : "web.admin.tasks.triggerText.everyMilliseconds",
              ms,
            ];
      return appT(key, { count, count_text: count });
    }
    case "daily":
      return appT("web.admin.tasks.triggerText.daily", { time: t.time_of_day ?? "00:00" });
    case "weekly":
      return appT(
        short ? "web.admin.tasks.triggerText.weeklyShort" : "web.admin.tasks.triggerText.weekly",
        { day: String(t.day_of_week ?? 0), time: t.time_of_day ?? "00:00" },
      );
    case "startup":
      return appT(
        short ? "web.admin.tasks.triggerText.startupShort" : "web.admin.tasks.triggerText.startup",
      );
    default:
      return t.type;
  }
}

/**
 * The default trigger a plugin task binding asks for. Mirrors the server's
 * defaultPluginTaskTriggers: only a missing or typeless trigger means the task
 * runs at startup. A type the server doesn't know is kept as-is (the server
 * builds no live trigger for it), so the page shows it rather than claiming
 * startup. Once the task exists, its schedule on the Tasks page wins.
 */
export function pluginTaskTrigger(raw?: Record<string, unknown> | null): TriggerConfig {
  const startup = { type: "startup" } as TriggerConfig;
  if (!raw || typeof raw.type !== "string" || raw.type === "") return startup;
  // The server unmarshals this map into TriggerConfig and falls back to startup
  // if a known field has the wrong JSON type.
  for (const field of ["interval_ms", "day_of_week", "max_runtime_ms"]) {
    const value = raw[field];
    if (value != null && !Number.isSafeInteger(value)) return startup;
  }
  if (raw.time_of_day != null && typeof raw.time_of_day !== "string") return startup;
  return raw as unknown as TriggerConfig;
}

/** Task-page path for a plugin's scheduled task (`plugin:<installation>:<capability>`). */
export function pluginTaskPath(installationId: number, capabilityId: string): string {
  return `/admin/tasks/${encodeURIComponent(`plugin:${installationId}:${capabilityId}`)}`;
}
