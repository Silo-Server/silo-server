import { t, type MessageKey } from "@/i18n";

/** The coarse device families the admin device views group by. */
export type PlatformKind = "tv" | "mobile" | "tablet" | "desktop" | "unknown";

const PLATFORM_KIND_LABELS: Record<PlatformKind, MessageKey> = {
  tv: "web.common.devicePlatform.kind.tv",
  mobile: "web.common.devicePlatform.kind.mobile",
  tablet: "web.common.devicePlatform.kind.tablet",
  desktop: "web.common.devicePlatform.kind.desktop",
  unknown: "web.common.devicePlatform.kind.unknown",
};

/** A device family's name in the current language. */
export function platformKindLabel(kind: PlatformKind): string {
  return t(PLATFORM_KIND_LABELS[kind]);
}

/** The platform a device reports, as sent, or a placeholder when it reports none. */
export function platformLabel(raw: string | undefined): string {
  return raw && raw.trim().length > 0 ? raw : t("web.common.devicePlatform.unknown");
}
