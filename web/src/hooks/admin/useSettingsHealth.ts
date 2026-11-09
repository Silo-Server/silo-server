import { useMemo } from "react";

import { t, useAppLanguage, type MessageKey } from "@/i18n";

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

/** Hardware encoders by product name; the two modes that are not one are translated. */
const HW_ACCEL_PRODUCTS: Record<string, string> = {
  qsv: "Quick Sync",
  vaapi: "VA-API",
  nvenc: "NVENC",
  videotoolbox: "VideoToolbox",
};

const HW_ACCEL_MODES: Record<string, MessageKey> = {
  auto: "web.admin.settings.health.hwAccel.auto",
  none: "web.admin.settings.health.hwAccel.software",
};

function hwAccelLabel(value: string): string {
  const mode = HW_ACCEL_MODES[value];
  if (mode) return t(mode);
  return HW_ACCEL_PRODUCTS[value] ?? value.toUpperCase();
}

/** The configured transcoding mode, in one word. */
function transcodeModeLabel(configured: string): string {
  return hwAccelLabel(configured || "auto");
}

const POSTGRES = "Postgres";
const REDIS = "Redis";

const SEARCH_PROVIDER_LABELS: Record<string, string> = {
  postgres: POSTGRES,
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
  if (state === "warn") return { label: t("web.admin.settings.health.action.fix"), page };
  if (state === "off") return { label: t("web.admin.settings.health.action.setUp"), page };
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
    ? t("web.admin.settings.health.storage.publicAndPrivate")
    : publicBucket
      ? t("web.admin.settings.health.storage.publicBucket", { bucket: publicBucket })
      : t("web.admin.settings.health.storage.noBucket");
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
      label: t("web.admin.settings.health.storage.label"),
      state: storageState,
      stateText: storageReady
        ? t("web.admin.settings.health.state.healthy")
        : storageRestartPending
          ? t("web.admin.settings.health.state.restartPending")
          : t("web.admin.settings.health.state.notSetUp"),
      detail: storageReady
        ? bucketDetail
        : storageRestartPending
          ? t("web.admin.settings.health.storage.appliesAfterRestart", { bucket: bucketDetail })
          : t("web.admin.settings.health.storage.notSetUp"),
      action: tileAction(storageState, "storage"),
    },
    {
      id: "database",
      label: t("web.admin.settings.health.database.label"),
      state: "ok",
      stateText: t("web.admin.settings.health.state.healthy"),
      detail: join([
        POSTGRES,
        maxConnections
          ? t("web.admin.settings.health.database.maxConnections", {
              count: maxConnections,
              count_text: String(maxConnections),
            })
          : null,
        redisConfigured ? REDIS : null,
      ]),
    },
    {
      id: "transcoding",
      label: t("web.admin.settings.health.transcoding.label"),
      state: transcodeState,
      stateText: playbackRestartPending
        ? t("web.admin.settings.health.state.restartPending")
        : transcodeEnabled
          ? t("web.admin.settings.health.state.ready")
          : t("web.admin.settings.health.state.off"),
      detail: playbackRestartPending
        ? t("web.admin.settings.health.transcoding.appliesAfterRestart")
        : transcodeEnabled
          ? transcodeMode
          : t("web.admin.settings.health.transcoding.off"),
      action: tileAction(transcodeState, "playback"),
    },
    {
      id: "search",
      label: t("web.admin.settings.health.search.label"),
      state: searchState,
      stateText: searchProviderLabel(activeSearch),
      detail:
        activeSearch === "meilisearch"
          ? !searchStatusResolved
            ? t("web.admin.settings.health.search.checking")
            : searchDegraded
              ? (input.search?.degraded_reason ?? t("web.admin.settings.health.search.degraded"))
              : meiliHealthy
                ? t("web.admin.settings.health.search.meiliConnected")
                : t("web.admin.settings.health.search.meiliUnreachable")
          : meiliConfigured
            ? t("web.admin.settings.health.search.meiliInactive")
            : t("web.admin.settings.health.search.meiliNotConnected"),
      action: tileAction(searchState, "search"),
    },
    {
      id: "public-url",
      label: t("web.admin.settings.health.publicUrl.label"),
      state: publicUrlState,
      stateText: publicUrl
        ? t("web.admin.settings.health.state.set")
        : t("web.admin.settings.health.state.notSet"),
      detail: publicUrl || t("web.admin.settings.health.publicUrl.notSet"),
      action: tileAction(publicUrlState, "general"),
    },
    {
      id: "email",
      label: t("web.admin.settings.health.email.label"),
      state: emailState,
      stateText: mailReady
        ? t("web.admin.settings.health.state.ready")
        : t("web.admin.settings.health.state.notSetUp"),
      detail: mailReady
        ? t("web.admin.settings.health.email.ready", { host: emailHost })
        : t("web.admin.settings.health.email.notSetUp"),
      action: tileAction(emailState, "email"),
    },
  ];
}

/** The health items that ask the admin to do something, and the page that does it. */
export function settingsAttentionItems(items: readonly SettingsHealthItem[]): SettingsHealthItem[] {
  return items.filter((item) => item.action != null);
}

/** The attention items, and whether every input they depend on has arrived. */
export interface SettingsAttentionState {
  items: SettingsHealthItem[];
  /**
   * False while the settings map, branding, or server status is still
   * loading. An item missing from `items` then may simply not be known yet,
   * rather than resolved.
   */
  loaded: boolean;
}

/**
 * What in the server's setup needs the admin, for the Dashboard's notice and
 * the admin sidebar's dots. Every query it reads is one a settings page
 * already loads. Empty until the settings map arrives, so an unloaded server
 * never reads as unconfigured.
 */
export function useSettingsAttention(): SettingsHealthItem[] {
  return useSettingsAttentionState().items;
}

/** {@link useSettingsAttention}, plus whether its inputs have all arrived. */
export function useSettingsAttentionState(): SettingsAttentionState {
  // The items' text is built in the current language; rebuild it on a change.
  const language = useAppLanguage();
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
    if (settings == null) return { items: [], loaded: false };
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
    // Server status says which saved settings wait on a restart; before it
    // arrives, transcoding saved as on reads "Ready" though it is still off.
    return branding.loaded
      ? { items, loaded: serverStatus != null }
      : { items: items.filter((item) => item.id !== "storage"), loaded: false };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs the item text
  }, [
    language,
    branding.loaded,
    branding.storageAvailable,
    search,
    sensitive?.configured,
    serverStatus,
    settings,
  ]);
}
