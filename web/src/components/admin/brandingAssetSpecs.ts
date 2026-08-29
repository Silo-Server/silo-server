import type { BrandingAssetKind } from "@/hooks/queries/admin/branding";

/** How the server treats one uploaded branding asset, and what it serves without one. */
export interface BrandingAssetSpec {
  /** Longest edge (px) the stored copy is capped at, or null when stored as uploaded. */
  storedPx: number | null;
  /** Server-side upload cap in bytes. */
  maxUploadBytes: number;
  /** One line of upload guidance, shown under the slot description. */
  guidance: string;
  /** Bundled asset served while the slot is empty, or null when there is none. */
  defaultUrl: string | null;
  /** Caption under the preview while the slot is empty. */
  emptyCaption: string;
}

const MB = 1024 * 1024;
const WORDMARK_PX = 640;
const MARK_PX = 512;
const LOGIN_BG_PX = 2560;
const WORDMARK_MAX_BYTES = 8 * MB;
const MARK_MAX_BYTES = 8 * MB;
const FAVICON_MAX_BYTES = 1 * MB;
const LOGIN_BG_MAX_BYTES = 12 * MB;

const megabytes = (bytes: number) => `${Math.round(bytes / MB)} MB`;

/**
 * What actually happens to each uploaded asset, mirrored from
 * `internal/branding/assets.go` (per-kind caps and processing) and
 * `internal/imageutil/imageutil.go` (width cap vs. square center-crop). Every
 * number the UI quotes is written once here so the guidance cannot drift away
 * from the pipeline one slot at a time. The same numbers are documented for API
 * clients in `docs/admin-api.md`.
 *
 * `defaultUrl` is the bundled asset served while a slot is empty — the same
 * files `SiloBrand` and the favicon link fall back to. The login background has
 * none: the auth pages just keep their theme gradient.
 */
export const BRANDING_ASSET_SPECS: Record<BrandingAssetKind, BrandingAssetSpec> = {
  wordmark: {
    storedPx: WORDMARK_PX,
    maxUploadBytes: WORDMARK_MAX_BYTES,
    guidance: `Wide artwork, ${WORDMARK_PX}px or wider. Stored as WebP capped at ${WORDMARK_PX}px wide; narrower images are not enlarged. PNG, JPEG, or WebP up to ${megabytes(WORDMARK_MAX_BYTES)}.`,
    defaultUrl: "/silo-wordmark-sidebar.png",
    emptyCaption: "Default",
  },
  // Light variants share their base kind's pipeline. With no upload they
  // inherit the main asset instead of using a separate bundled default.
  wordmark_light: {
    storedPx: WORDMARK_PX,
    maxUploadBytes: WORDMARK_MAX_BYTES,
    guidance: `Wide artwork, ${WORDMARK_PX}px or wider. Stored as WebP capped at ${WORDMARK_PX}px wide; narrower images are not enlarged. PNG, JPEG, or WebP up to ${megabytes(WORDMARK_MAX_BYTES)}.`,
    defaultUrl: null,
    emptyCaption: "Falls back to the main logo",
  },
  mark: {
    storedPx: MARK_PX,
    maxUploadBytes: MARK_MAX_BYTES,
    guidance: `Square artwork, ${MARK_PX}×${MARK_PX} or larger. Anything else is center-cropped to a square, then stored as WebP at exactly ${MARK_PX}×${MARK_PX} — smaller art is upscaled. PNG, JPEG, or WebP up to ${megabytes(MARK_MAX_BYTES)}.`,
    defaultUrl: "/silo-icon-1024.png",
    emptyCaption: "Default",
  },
  mark_light: {
    storedPx: MARK_PX,
    maxUploadBytes: MARK_MAX_BYTES,
    guidance: `Square artwork, ${MARK_PX}×${MARK_PX} or larger. Anything else is center-cropped to a square, then stored as WebP at exactly ${MARK_PX}×${MARK_PX} — smaller art is upscaled. PNG, JPEG, or WebP up to ${megabytes(MARK_MAX_BYTES)}.`,
    defaultUrl: null,
    emptyCaption: "Falls back to the main icon",
  },
  favicon: {
    storedPx: null,
    maxUploadBytes: FAVICON_MAX_BYTES,
    guidance: `Square PNG, ICO, or SVG up to ${megabytes(FAVICON_MAX_BYTES)}. Stored exactly as uploaded — never resized or re-encoded.`,
    defaultUrl: "/favicon.ico",
    emptyCaption: "Default",
  },
  login_bg: {
    storedPx: LOGIN_BG_PX,
    maxUploadBytes: LOGIN_BG_MAX_BYTES,
    guidance: `Wide photo, ${LOGIN_BG_PX}px or wider. Stored as WebP capped at ${LOGIN_BG_PX}px wide and shown cover-cropped, so keep the subject centered. PNG, JPEG, or WebP up to ${megabytes(LOGIN_BG_MAX_BYTES)}.`,
    defaultUrl: null,
    emptyCaption: "Theme gradient",
  },
};
