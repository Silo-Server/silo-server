import type { TriggerConfig } from "@/api/types";

const SHORT_DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const LONG_DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

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
      const units = [
        { size: 86_400_000, shortLabel: "d", longLabel: "day(s)" },
        { size: 3_600_000, shortLabel: "h", longLabel: "hour(s)" },
        { size: 60_000, shortLabel: "m", longLabel: "minute(s)" },
        { size: 1000, shortLabel: "s", longLabel: "second(s)" },
      ];
      const unit = units.find(
        ({ size }) => ms >= size && ms % size === 0 && (short || size < 86_400_000),
      );
      if (unit)
        return `Every ${ms / unit.size}${short ? "" : " "}${short ? unit.shortLabel : unit.longLabel}`;
      return ms === 0
        ? short
          ? "Every 0s"
          : "Every 0 second(s)"
        : `Every ${ms}${short ? "ms" : " millisecond(s)"}`;
    }
    case "daily":
      return `Daily at ${t.time_of_day ?? "00:00"}`;
    case "weekly": {
      const days = short ? SHORT_DAYS : LONG_DAYS;
      const day = days[t.day_of_week ?? 0] ?? `Day ${t.day_of_week}`;
      return `${day} at ${t.time_of_day ?? "00:00"}`;
    }
    case "startup":
      return short ? "On startup" : "On server startup";
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
