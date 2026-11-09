import { t } from "@/i18n";

type DeviceKind = "desktop" | "phone" | "tv" | "unknown";

function platformLabel(value: string): string {
  return /tvOS|Apple ?TV/i.test(value)
    ? "Apple TV"
    : /Android.*TV|Android TV/i.test(value)
      ? "Android TV"
      : /Android/i.test(value)
        ? "Android"
        : /iPad/i.test(value)
          ? "iPad"
          : /iPhone/i.test(value)
            ? "iPhone"
            : /iOS/i.test(value)
              ? "iOS"
              : /Windows/i.test(value)
                ? "Windows"
                : /Macintosh|macOS|Mac OS X/i.test(value)
                  ? "macOS"
                  : /Linux/i.test(value)
                    ? "Linux"
                    : "";
}

function platformKind(platform: string): DeviceKind {
  return /TV/.test(platform)
    ? "tv"
    : /Android|iPhone|iPad|iOS/.test(platform)
      ? "phone"
      : platform
        ? "desktop"
        : "unknown";
}

/**
 * The label and icon kind for a login session. A client that named itself
 * (X-Silo-Device-Name with X-Silo-Device-Platform, or a device-code sign-in)
 * keeps its name; a User-Agent is summarized as "<browser> on <platform>", in
 * English like the device names the server generates.
 */
export function loginSessionDevice(session: { device_name: string; device_platform?: string }): {
  name: string;
  kind: DeviceKind;
} {
  const raw = session.device_name.trim();
  const reported = session.device_platform?.trim() ?? "";
  if (reported) {
    return {
      name: raw || t("web.settings.sessions.device.unknown"),
      kind: platformKind(platformLabel(reported) || platformLabel(raw)),
    };
  }
  if (!raw) return { name: t("web.settings.sessions.device.unknown"), kind: "unknown" };
  const platform = platformLabel(raw);
  const app = /\bSilo\b/i.test(raw)
    ? "Silo app"
    : /Edg(?:e|A|iOS)?\//.test(raw)
      ? "Edge"
      : /(?:Firefox|FxiOS)\//.test(raw)
        ? "Firefox"
        : /(?:OPR|Opera)\//.test(raw)
          ? "Opera"
          : /(?:Chrome|CriOS|HeadlessChrome)\//.test(raw)
            ? "Chrome"
            : /Safari\//.test(raw)
              ? "Safari"
              : "";
  // Anything that isn't a recognizable User-Agent is a name; show it as is.
  const userAgent = app || /^[\w.-]+\/\S/.test(raw);
  if (!userAgent) return { name: raw, kind: platformKind(platform) };
  return {
    name: app
      ? platform
        ? `${app} on ${platform}`
        : app
      : platform
        ? `Device on ${platform}`
        : raw,
    kind: platformKind(platform),
  };
}

/** Whether a session was seen in the last two minutes ("Active now"). */
export function sessionActive(value: string | null, now = Date.now()): boolean {
  const elapsed = sessionElapsed(value, now);
  return elapsed !== null && elapsed < 120_000;
}

export function sessionLastSeen(value: string | null, now = Date.now()): string {
  const elapsed = sessionElapsed(value, now);
  if (elapsed === null) return t("web.settings.sessions.lastSeen.never");
  if (sessionActive(value, now)) return t("web.settings.sessions.lastSeen.active");
  const minutes = Math.floor(elapsed / 60_000);
  if (minutes < 60)
    return t("web.settings.sessions.lastSeen.minutes", {
      count: minutes,
      count_text: String(minutes),
    });
  if (minutes < 1440) {
    const hours = Math.floor(minutes / 60);
    return t("web.settings.sessions.lastSeen.hours", { count: hours, count_text: String(hours) });
  }
  const days = Math.floor(minutes / 1440);
  return t("web.settings.sessions.lastSeen.days", { count: days, count_text: String(days) });
}

/** Milliseconds since the session was last seen, or null when that isn't recorded. */
function sessionElapsed(value: string | null, now: number): number | null {
  if (!value) return null;
  const elapsed = Math.max(0, now - Date.parse(value));
  return Number.isFinite(elapsed) ? elapsed : null;
}
