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
 * keeps its name; a User-Agent is summarized as "<browser> on <platform>".
 */
export function loginSessionDevice(session: { device_name: string; device_platform?: string }): {
  name: string;
  kind: DeviceKind;
} {
  const raw = session.device_name.trim();
  const reported = session.device_platform?.trim() ?? "";
  if (reported) {
    return {
      name: raw || "Unknown device",
      kind: platformKind(platformLabel(reported) || platformLabel(raw)),
    };
  }
  if (!raw) return { name: "Unknown device", kind: "unknown" };
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

export function sessionLastSeen(value: string | null, now = Date.now()): string {
  if (!value) return "Last seen not recorded yet";
  const elapsed = Math.max(0, now - Date.parse(value));
  if (!Number.isFinite(elapsed)) return "Last seen not recorded yet";
  if (elapsed < 120_000) return "Active now";
  const minutes = Math.floor(elapsed / 60_000);
  return minutes < 60
    ? `Last seen ${minutes} min ago`
    : minutes < 1440
      ? `Last seen ${Math.floor(minutes / 60)} hr ago`
      : `Last seen ${Math.floor(minutes / 1440)} days ago`;
}
