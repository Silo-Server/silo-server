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
      if (short) {
        if (ms >= 86_400_000) return `Every ${Math.round(ms / 86_400_000)}d`;
        if (ms >= 3_600_000) return `Every ${Math.round(ms / 3_600_000)}h`;
        if (ms >= 60_000) return `Every ${Math.round(ms / 60_000)}m`;
        return `Every ${Math.round(ms / 1000)}s`;
      }
      if (ms >= 3_600_000) return `Every ${Math.round(ms / 3_600_000)} hour(s)`;
      if (ms >= 60_000) return `Every ${Math.round(ms / 60_000)} minute(s)`;
      return `Every ${Math.round(ms / 1000)} second(s)`;
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
  if (raw && typeof raw.type === "string" && raw.type !== "") {
    return raw as unknown as TriggerConfig;
  }
  return { type: "startup" } as TriggerConfig;
}

/** Task-page path for a plugin's scheduled task (`plugin:<installation>:<capability>`). */
export function pluginTaskPath(installationId: number, capabilityId: string): string {
  return `/admin/tasks/${encodeURIComponent(`plugin:${installationId}:${capabilityId}`)}`;
}
