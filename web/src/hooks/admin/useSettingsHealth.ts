import { useMemo } from "react";

import type { AdminServerStatus } from "@/api/types";
import { emailReady } from "@/lib/emailReadiness";
import { useBranding } from "@/hooks/useBranding";
import {
  useAdminSensitiveStatus,
  useAdminServerSettings,
  useAdminServerStatus,
  useCatalogSearchStatus,
  type CatalogSearchStatus,
} from "@/hooks/queries/admin/settings";

/**
 * The settings pages a health item can send the admin to. The ids here are
 * stable route segments and have to match the pages the settings layout mounts.
 */
export const ADMIN_SETTINGS_PAGE_IDS = [
  "general",
  "storage",
  "search",
  "logging",
  "appearance",
  "security",
  "sign-in",
  "library",
  "autoscan",
  "playback",
  "downloads",
  "requests",
  "providers",
  "ai",
  "email",
  "notifications",
  "compatibility",
  "network-access",
] as const;

export type AdminSettingsPageID = (typeof ADMIN_SETTINGS_PAGE_IDS)[number];

/** How a health item reads: healthy, needs fixing, not set up, or informational. */
export type SettingsHealthState = "ok" | "warn" | "off" | "info";

export interface SettingsHealthAction {
  label: string;
  page: AdminSettingsPageID;
}

export interface SettingsHealthItem {
  id: string;
  label: string;
  state: SettingsHealthState;
  /** One or two words, e.g. "Healthy" or "Restart pending". */
  stateText: string;
  /** Single supporting line under the state. */
  detail: string;
  action?: SettingsHealthAction;
}

/**
 * Everything the model is derived from. Kept as plain data (rather than read
 * off hooks inside the builder) so the derivation is testable without a query
 * client, and so a missing/failed query is just `undefined` here.
 */
export interface SettingsHealthInput {
  settings?: Record<string, string>;
  sensitiveConfigured?: readonly string[];
  storageAvailable?: boolean;
  serverStatus?: AdminServerStatus;
  search?: CatalogSearchStatus;
}

const TRUE_VALUES = new Set(["true", "1", "yes", "on"]);

function readText(settings: Record<string, string> | undefined, key: string): string {
  return (settings?.[key] ?? "").trim();
}

function readBool(settings: Record<string, string> | undefined, key: string): boolean {
  return TRUE_VALUES.has(readText(settings, key).toLowerCase());
}

function readInt(settings: Record<string, string> | undefined, key: string): number | null {
  const parsed = Number.parseInt(readText(settings, key), 10);
  return Number.isFinite(parsed) ? parsed : null;
}

function join(parts: Array<string | null | undefined>): string {
  return parts.filter((part): part is string => Boolean(part && part.trim())).join(" · ");
}

function sentenceCase(value: string): string {
  if (!value) return "";
  return value.charAt(0).toUpperCase() + value.slice(1);
}

const HW_ACCEL_LABELS: Record<string, string> = {
  auto: "Auto",
  qsv: "Quick Sync",
  vaapi: "VA-API",
  nvenc: "NVENC",
  videotoolbox: "VideoToolbox",
  none: "Software",
};

function hwAccelLabel(value: string): string {
  return HW_ACCEL_LABELS[value] ?? value.toUpperCase();
}

/** The configured transcoding mode, in one word. */
function transcodeModeLabel(configured: string): string {
  return hwAccelLabel(configured || "auto");
}

const SEARCH_PROVIDER_LABELS: Record<string, string> = {
  postgres: "Postgres",
  meilisearch: "Meilisearch",
};

function searchProviderLabel(value: string): string {
  return SEARCH_PROVIDER_LABELS[value] ?? sentenceCase(value);
}

/**
 * Restart reasons that name a subsystem other than the settings batch. Only
 * used against servers that predate the accumulated per-key reasons list,
 * where the single last-writer reason is the closest signal there is.
 */
const NON_SETTINGS_RESTART_REASONS = new Set([
  "jellyfin_compat",
  "ratelimit_backend",
  "plugin_auth_binding",
  "plugin_task_binding",
]);

function legacySettingsRestartPending(status: AdminServerStatus): boolean {
  const reason = (status.restart_required_reason ?? "").trim();
  return reason === "" || !NON_SETTINGS_RESTART_REASONS.has(reason);
}

/**
 * Whether a saved setting under one of `prefixes` is waiting for a restart.
 *
 * The server accumulates one "setting:<key>" reason per restart-required save,
 * so an item can warn about its own keys and stay quiet for everyone else's —
 * and a later unrelated save cannot overwrite the evidence. An older server
 * without the list falls back to the coarse last-reason heuristic, which warns
 * for any settings save rather than missing a real one.
 */
function settingsRestartPending(
  status: AdminServerStatus | undefined,
  prefixes: readonly string[],
): boolean {
  if (!status?.restart_required) return false;
  const reasons = status.restart_required_reasons;
  if (!reasons) return legacySettingsRestartPending(status);
  return reasons.some((reason) => {
    if (!reason.startsWith("setting:")) return false;
    const key = reason.slice("setting:".length);
    return prefixes.some((prefix) => key.startsWith(prefix));
  });
}

// ---------------------------------------------------------------------------
// Health items
// ---------------------------------------------------------------------------

/**
 * The link an item carries. Only an item the admin has to do something about
 * gets one — a healthy item is a fact, not a task.
 */
function tileAction(
  state: SettingsHealthState,
  page: AdminSettingsPageID,
): SettingsHealthAction | undefined {
  if (state === "warn") return { label: "Fix", page };
  if (state === "off") return { label: "Set up", page };
  return undefined;
}

/** Every health item, healthy or not, derived from already-fetched data. */
export function buildSettingsHealth(input: SettingsHealthInput): SettingsHealthItem[] {
  const { settings } = input;
  const configured = new Set(input.sensitiveConfigured ?? []);

  const publicBucket = readText(settings, "s3.public_bucket");
  const privateBucket = readText(settings, "s3.private_bucket");
  const storageReady = input.storageAvailable === true;
  const bucketDetail = privateBucket
    ? "S3 · public + private"
    : publicBucket
      ? `S3 · ${publicBucket}`
      : "No bucket configured";
  // `storage_available` is decided when the S3 client is built at boot, so the
  // save that first configures a bucket cannot flip it — only the restart that
  // save already asked for can. Reporting "Not set up" in that window tells an
  // admin who just did the work that it did not take.
  const storageRestartPending =
    !storageReady && publicBucket !== "" && settingsRestartPending(input.serverStatus, ["s3."]);
  const storageState: SettingsHealthState = storageReady
    ? "ok"
    : storageRestartPending
      ? "warn"
      : "off";

  const maxConnections = readInt(settings, "database.max_connections");
  const redisConfigured = configured.has("redis.url") || readText(settings, "redis.url") !== "";

  const transcodeEnabled = readBool(settings, "playback.transcode_enabled");
  const transcodeMode = transcodeModeLabel(readText(settings, "playback.hw_accel"));
  const playbackRestartPending = settingsRestartPending(input.serverStatus, ["playback."]);
  const transcodeState: SettingsHealthState = playbackRestartPending
    ? "warn"
    : transcodeEnabled
      ? "ok"
      : "off";

  const activeSearch =
    input.search?.active_provider || readText(settings, "catalog.search.provider") || "postgres";
  const searchStatusResolved = input.search != null;
  const meiliConfigured = input.search?.meilisearch.configured ?? false;
  const meiliHealthy = input.search?.meilisearch.healthy ?? false;
  const searchDegraded = input.search?.degraded ?? false;
  const searchState: SettingsHealthState =
    activeSearch === "meilisearch"
      ? !searchStatusResolved
        ? "info"
        : meiliHealthy && !searchDegraded
          ? "ok"
          : "warn"
      : meiliConfigured
        ? "warn"
        : "info";

  const emailHost = readText(settings, "email.smtp_host");
  const mailReady = emailReady(
    readBool(settings, "email.enabled"),
    emailHost,
    readText(settings, "email.from_address"),
  );
  const emailState: SettingsHealthState = mailReady ? "ok" : "off";

  // Invitation and reset links, sign-in provider redirects, and links in
  // email and Discord messages are all built from it, and Silo cannot guess it.
  const publicUrl = readText(settings, "server.public_url");
  const publicUrlState: SettingsHealthState = publicUrl ? "ok" : "off";

  return [
    {
      id: "storage",
      label: "Storage",
      state: storageState,
      stateText: storageReady
        ? "Healthy"
        : storageRestartPending
          ? "Restart pending"
          : "Not set up",
      detail: storageReady
        ? bucketDetail
        : storageRestartPending
          ? `${bucketDetail} · applies after a restart`
          : "Artwork and uploads have nowhere to go",
      action: tileAction(storageState, "storage"),
    },
    {
      id: "database",
      label: "Database",
      state: "ok",
      stateText: "Healthy",
      detail: join([
        "Postgres",
        maxConnections ? `max ${maxConnections} connections` : null,
        redisConfigured ? "Redis" : null,
      ]),
    },
    {
      id: "transcoding",
      label: "Transcoding",
      state: transcodeState,
      stateText: playbackRestartPending ? "Restart pending" : transcodeEnabled ? "Ready" : "Off",
      detail: playbackRestartPending
        ? "Saved changes apply after a restart"
        : transcodeEnabled
          ? transcodeMode
          : "Video plays as-is; remux and audio conversion still run",
      action: tileAction(transcodeState, "playback"),
    },
    {
      id: "search",
      label: "Search",
      state: searchState,
      stateText: searchProviderLabel(activeSearch),
      detail:
        activeSearch === "meilisearch"
          ? !searchStatusResolved
            ? "Checking connection"
            : searchDegraded
              ? (input.search?.degraded_reason ?? "Search is running in a degraded mode")
              : meiliHealthy
                ? "Meilisearch connected"
                : "Meilisearch not reachable"
          : meiliConfigured
            ? "Meilisearch configured but not active"
            : "Meilisearch not connected",
      action: tileAction(searchState, "search"),
    },
    {
      id: "public-url",
      label: "Public URL",
      state: publicUrlState,
      stateText: publicUrl ? "Set" : "Not set",
      detail: publicUrl || "Invitation and reset links, and sign-in providers, need it",
      action: tileAction(publicUrlState, "general"),
    },
    {
      id: "email",
      label: "Email",
      state: emailState,
      stateText: mailReady ? "Ready" : "Not set up",
      detail: mailReady ? `SMTP · ${emailHost}` : "Invites and resets can't send",
      action: tileAction(emailState, "email"),
    },
  ];
}

/** The health items that ask the admin to do something, and the page that does it. */
export function settingsAttentionItems(items: readonly SettingsHealthItem[]): SettingsHealthItem[] {
  return items.filter((item) => item.action != null);
}

/**
 * What in the server's setup needs the admin, for the Dashboard's notice and
 * the admin sidebar's dots. Every query it reads is one a settings page
 * already loads. Empty until the settings map arrives, so an unloaded server
 * never reads as unconfigured.
 */
export function useSettingsAttention(): SettingsHealthItem[] {
  const { data: settings } = useAdminServerSettings();
  const { data: sensitive } = useAdminSensitiveStatus();
  const { data: serverStatus } = useAdminServerStatus();
  const branding = useBranding();

  // Postgres search has no external service to check. Avoid paying for the
  // Meilisearch status request unless this server actually selected it.
  const searchProvider = (settings?.["catalog.search.provider"] ?? "").trim() || "postgres";
  const { data: search } = useCatalogSearchStatus(
    settings != null && searchProvider === "meilisearch",
    { pollRebuild: false },
  );

  return useMemo(() => {
    if (settings == null) return [];
    const items = settingsAttentionItems(
      buildSettingsHealth({
        settings,
        sensitiveConfigured: sensitive?.configured,
        storageAvailable: branding.storageAvailable,
        serverStatus,
        search,
      }),
    );
    // Branding reports whether storage is available; before it arrives that
    // reads as false, which is not the same as storage that is not set up.
    return branding.loaded ? items : items.filter((item) => item.id !== "storage");
  }, [
    branding.loaded,
    branding.storageAvailable,
    search,
    sensitive?.configured,
    serverStatus,
    settings,
  ]);
}
