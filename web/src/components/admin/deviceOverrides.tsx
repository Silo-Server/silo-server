import { useMemo, useState } from "react";
// useMemo is used by DeviceProfileTabs to memoize the conflict + anomaly
// maps; useState by the tab-active state.
import {
  AlertTriangle,
  Monitor,
  MonitorSmartphone,
  RotateCcw,
  Smartphone,
  Tablet,
  Tv,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { RegistrySettingControl } from "@/components/settings/RegistrySettingControl";
import {
  ALL_DEVICE_SETTING_KEYS,
  controlKindFor,
  defaultValueToString,
  formatSettingValue,
  getSettingDefinition,
  isStructuredSetting,
} from "@/lib/settingsDisplay";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { manifestPlatformFor, settingAppliesToPlatform } from "@/lib/deviceSettingGroups";
import { cn } from "@/lib/utils";
import type { AdminDeviceSetting, AdminUserSettingEntry } from "@/hooks/queries/admin/users";
import { deviceInheritedValue, type InheritedValue } from "@/lib/inheritedSettingValue";
import { formatRelativeTime } from "@/lib/date";
import { settingDescription, settingLabel } from "@/lib/settingsText";
import { useT, type Translate } from "@/i18n";
import type { PlatformKind } from "./devicePlatformLabels";

export { platformKindLabel, platformLabel, type PlatformKind } from "./devicePlatformLabels";

export const UNKNOWN_PROFILE_ID = "unknown-profile";

const SEPARATOR = " · ";
const DOT = "·";

export function classifyPlatform(raw: string | undefined): PlatformKind {
  if (!raw) return "unknown";
  const p = raw.toLowerCase();
  if (
    p.includes("tvos") ||
    p.includes("apple tv") ||
    p.includes("androidtv") ||
    p.includes("android tv") ||
    p.includes("roku") ||
    p.includes("firetv") ||
    p.includes("fire tv") ||
    p.includes("webos") ||
    p.includes("tizen") ||
    /\btv\b/.test(p)
  ) {
    return "tv";
  }
  if (p.includes("ipad") || p.includes("tablet")) return "tablet";
  if (
    p.includes("ios") ||
    p.includes("iphone") ||
    p.includes("android") ||
    p.includes("mobile") ||
    p.includes("phone")
  ) {
    return "mobile";
  }
  if (
    p.includes("mac") ||
    p.includes("win") ||
    p.includes("linux") ||
    p.includes("desktop") ||
    p.includes("chrome") ||
    p.includes("safari") ||
    p.includes("firefox") ||
    p.includes("edge") ||
    p.includes("web") ||
    p.includes("browser")
  ) {
    return "desktop";
  }
  return "unknown";
}

export function formatRelative(iso: string | undefined): string {
  return formatRelativeTime(iso, { absoluteAfterDays: 30 }) ?? "—";
}

export function shortenId(id: string, visible = 8): string {
  if (id.length <= visible * 2 + 1) return id;
  return `${id.slice(0, visible)}…${id.slice(-visible)}`;
}

export function PlatformIcon({ kind, className }: { kind: PlatformKind; className?: string }) {
  switch (kind) {
    case "tv":
      return <Tv className={className} strokeWidth={1.6} />;
    case "mobile":
      return <Smartphone className={className} strokeWidth={1.6} />;
    case "tablet":
      return <Tablet className={className} strokeWidth={1.6} />;
    case "desktop":
      return <Monitor className={className} strokeWidth={1.6} />;
    default:
      return <MonitorSmartphone className={className} strokeWidth={1.6} />;
  }
}

export function PlatformTile({
  kind,
  size = "md",
}: {
  kind: PlatformKind;
  size?: "sm" | "md" | "lg";
}) {
  const dims = size === "lg" ? "h-11 w-11" : size === "sm" ? "h-8 w-8" : "h-9 w-9";
  const icon = size === "lg" ? "h-[18px] w-[18px]" : size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4";
  return (
    <div
      className={cn(
        "border-border/70 bg-surface-raised/50 text-foreground/90 flex shrink-0 items-center justify-center rounded-md border",
        dims,
      )}
    >
      <PlatformIcon kind={kind} className={icon} />
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────
// Per-override anomaly detection
//
// We want to point the admin at *specific* overrides that look suspect,
// not just say "this device is anomalous, good luck." These checks run
// off the override values themselves (no extra network calls), so the
// signal is bounded but immediate:
//
//   • extreme  — for sliders, value is ≥40% of the way across the full
//     [min,max] range away from the default. Catches things like a -3500ms
//     audio sync or a 50%-from-center subtitle offset that suggest someone
//     was working around a problem and forgot to revert.
//   • conflict — pairwise inconsistencies between settings on the same
//     device, e.g. HDR disabled while DV Profile-7 fallback is enabled.
//     Needs the whole settings list to evaluate, hence the Map output.
//   • stale    — when the device itself is dormant, every override on it
//     is suspect. Caller passes the device-level signal in.
//
// We deliberately do NOT flag "override value matches the manifest
// default" as an anomaly. The fallback chain is device → profile →
// manifest, and we only have the manifest layer in scope here — so a row
// that matches the manifest default may still be doing real work against
// the profile default. The iOS/tvOS clients also write current state on
// registration and treat the explicit value as authoritative regardless
// of the manifest's idea of a default; flagging those rows as redundant
// produced confusing false positives.
// ─────────────────────────────────────────────────────────────────────

export type SettingAnomalyKind = "extreme" | "conflict" | "stale";

export interface SettingAnomaly {
  kind: SettingAnomalyKind;
  /** Short, human-readable explanation rendered inline under the row. */
  reason: string;
}

/**
 * Compute pairwise conflict anomalies across all overrides on the active
 * profile. Returns a `key → anomaly` map; rows whose key is absent have
 * no conflict (they may still be flagged as redundant/extreme/stale by
 * {@link getSettingAnomaly}).
 */
export function detectSettingConflicts(
  settings: readonly AdminDeviceSetting[],
  t: Translate,
): Map<string, SettingAnomaly> {
  const out = new Map<string, SettingAnomaly>();
  const byKey = new Map(settings.map((s) => [s.key, s.value]));

  // HDR disabled but Profile-7 → HDR10 fallback is on. The fallback only
  // matters when HDR is in play, so this is almost certainly a misconfig.
  if (
    byKey.get("player.hdr_enabled") === "false" &&
    byKey.get("player.dv_profile7_hdr10_fallback") === "true"
  ) {
    out.set("player.dv_profile7_hdr10_fallback", {
      kind: "conflict",
      reason: t("web.admin.common.deviceOverrides.conflict.dvFallbackWithoutHdr"),
    });
    out.set("player.hdr_enabled", {
      kind: "conflict",
      reason: t("web.admin.common.deviceOverrides.conflict.hdrWithDvFallback"),
    });
  }

  // Auto-skip credits without auto-play next is a degenerate setup —
  // skipping credits goes to a black screen instead of the next episode.
  if (
    byKey.get("playback.auto_skip_credits") === "true" &&
    byKey.get("playback.auto_play_next") === "false"
  ) {
    out.set("playback.auto_skip_credits", {
      kind: "conflict",
      reason: t("web.admin.common.deviceOverrides.conflict.skipCreditsWithoutAutoPlay"),
    });
  }

  return out;
}

/**
 * Compute the anomaly (if any) for a single override row, using only the
 * setting itself plus a couple of out-of-band signals (device dormancy,
 * pre-computed conflict map). Returns null when nothing is suspect.
 */
export function getSettingAnomaly(
  setting: AdminDeviceSetting,
  isOverride: boolean,
  t: Translate,
  options: {
    /** Days the device has been dormant; flagged when > 30. */
    deviceStaleDays?: number | null;
    /** Pairwise conflict map from {@link detectSettingConflicts}. */
    conflicts?: Map<string, SettingAnomaly>;
  } = {},
): SettingAnomaly | null {
  // Don't flag synthesized default rows — they aren't an override yet, so
  // there's nothing for the admin to act on.
  if (!isOverride) return null;

  // Conflicts trump everything else — pairwise mistakes are the most
  // actionable signal we have.
  const conflict = options.conflicts?.get(setting.key);
  if (conflict) return conflict;

  const definition = getSettingDefinition(setting.key);

  // Extreme: slider value is far from the default. We use a fraction of
  // the [min,max] range so each slider's threshold scales with its own
  // sensitivity rather than picking arbitrary unit thresholds per key.
  if (
    definition &&
    controlKindFor(definition) === "slider" &&
    definition.minimum !== undefined &&
    definition.maximum !== undefined
  ) {
    const v = Number(setting.value);
    const def = Number(definition.defaultValue ?? 0);
    if (Number.isFinite(v) && Number.isFinite(def)) {
      const range = Math.abs(definition.maximum - definition.minimum);
      const distance = Math.abs(v - def);
      if (range > 0 && distance / range >= 0.4) {
        const unit = definition.unit ? ` ${definition.unit}` : "";
        return {
          kind: "extreme",
          reason: t("web.admin.common.deviceOverrides.anomaly.extreme", {
            value: `${v}${unit}`,
            default_value: `${def}${unit}`,
          }),
        };
      }
    }
  }

  // Stale: device itself is dormant. Every override on it is suspect.
  if (options.deviceStaleDays != null && options.deviceStaleDays > 30) {
    return {
      kind: "stale",
      reason: t("web.admin.common.deviceOverrides.anomaly.stale", {
        count: options.deviceStaleDays,
        count_text: options.deviceStaleDays,
      }),
    };
  }

  return null;
}

export interface DeviceOverrideRowProps {
  setting: AdminDeviceSetting;
  disabled: boolean;
  /**
   * True when this row reflects a real override on the device. False when
   * the row was synthesized from the manifest default so the admin can
   * inspect every available setting and create an override on demand.
   */
  isOverride: boolean;
  /**
   * Optional anomaly attached to this override. When set, the row picks up
   * a destructive accent and surfaces the reason inline.
   */
  anomaly?: SettingAnomaly | null;
  /**
   * For a row the device does not override: where its value comes from. The
   * row's `setting.value` is already that value.
   */
  inherited?: InheritedValue | null;
  onChange: (setting: AdminDeviceSetting, value: string) => void;
  /**
   * Open the editor for a JSON-shaped setting. The parent decides which
   * editor to render based on `setting.key` (e.g. a custom dialog for
   * `subtitle_appearance`) — `isOverride` lets the editor decide whether
   * to surface a "Reset" affordance.
   */
  onEditJson: (setting: AdminDeviceSetting, isOverride: boolean) => void;
  onReset: (setting: AdminDeviceSetting) => void;
}

export function DeviceOverrideRow({
  setting,
  disabled,
  isOverride,
  anomaly = null,
  inherited = null,
  onChange,
  onEditJson,
  onReset,
}: DeviceOverrideRowProps) {
  const t = useT();
  const definition = getSettingDefinition(setting.key);
  // Object-valued settings have no inline widget: they open a bespoke panel
  // (subtitle appearance) or the raw JSON editor.
  const isJsonOnly = isStructuredSetting(definition);

  const anomalyBadgeLabel: Record<SettingAnomalyKind, string> = {
    extreme: t("web.admin.common.deviceOverrides.badge.extreme"),
    conflict: t("web.admin.common.deviceOverrides.badge.conflict"),
    stale: t("web.admin.common.deviceOverrides.badge.stale"),
  };

  return (
    <div
      className={cn(
        "group/ovr grid gap-3 py-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-center md:gap-6",
        // Subtly dim non-overridden rows so the eye lands on real overrides
        // first. We don't grey out the controls themselves — the admin can
        // still interact and create a new override.
        !isOverride && "opacity-70 hover:opacity-100",
        // Anomalies get a destructive left edge so they read as "this is
        // probably the row you came here for" without dominating the UI.
        anomaly && "border-destructive/40 -ms-3 border-s-2 ps-3",
      )}
    >
      <div className="min-w-0 space-y-0.5">
        <div className="flex items-center gap-2">
          <span className="text-foreground text-[13.5px] leading-tight font-medium">
            {definition ? settingLabel(definition) : setting.key}
          </span>
          {isOverride ? (
            <span
              className="bg-foreground/80 inline-block h-1.5 w-1.5 shrink-0 rounded-full"
              title={t("web.admin.common.deviceOverrides.override.setTitle")}
              aria-label={t("web.admin.common.deviceOverrides.override.setLabel")}
            />
          ) : (
            <span
              className="text-muted-foreground/60 border-border/50 rounded-full border px-1.5 py-px text-[9.5px] font-medium tracking-[0.05em] uppercase"
              title={t("web.admin.common.deviceOverrides.override.notSetTitle", {
                source: inherited
                  ? inheritedSourceText(t, inherited)
                  : t("web.admin.common.deviceOverrides.source.appDefault"),
              })}
            >
              {inherited?.profileName != null
                ? t("web.admin.common.deviceOverrides.badge.profile")
                : t("web.admin.common.deviceOverrides.badge.default")}
            </span>
          )}
          {anomaly && (
            <span
              className="text-destructive border-destructive/30 bg-destructive/10 inline-flex items-center gap-1 rounded-full border px-1.5 py-px text-[9.5px] font-medium tracking-[0.05em] uppercase"
              title={anomaly.reason}
            >
              <AlertTriangle className="h-2.5 w-2.5" />
              {anomalyBadgeLabel[anomaly.kind]}
            </span>
          )}
        </div>
        <div className="text-muted-foreground/80 font-mono text-[11px] leading-tight">
          {setting.key}
        </div>
        {definition?.description && (
          <p className="text-muted-foreground pt-1 text-[12.5px] leading-relaxed">
            {settingDescription(definition)}
          </p>
        )}
        <div className="text-muted-foreground pt-0.5 text-[11.5px]">
          <span className="text-foreground/80">
            {formatSettingValue(setting.key, setting.value)}
          </span>
          {isOverride
            ? !isJsonOnly &&
              definition && (
                <span className="text-muted-foreground/60">
                  {SEPARATOR}
                  {t("web.admin.common.deviceOverrides.value.default", {
                    value: formatSettingValue(setting.key, defaultValueToString(definition)),
                  })}
                </span>
              )
            : inherited && (
                <span className="text-muted-foreground/60">
                  {SEPARATOR}
                  {inheritedSourceText(t, inherited)}
                </span>
              )}
        </div>
        {anomaly && (
          <div className="text-destructive/85 flex items-start gap-1.5 pt-1.5 text-[11.5px] leading-relaxed">
            <AlertTriangle className="mt-[1px] h-3 w-3 shrink-0" />
            <span>{anomaly.reason}</span>
          </div>
        )}
      </div>

      <div className="flex flex-col items-stretch gap-2 md:min-w-[220px] md:items-end">
        {isJsonOnly || !definition ? (
          <Button variant="outline" size="sm" onClick={() => onEditJson(setting, isOverride)}>
            {setting.key === SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE
              ? t("web.admin.common.deviceOverrides.edit.customize")
              : t("web.admin.common.deviceOverrides.edit.json")}
          </Button>
        ) : (
          <RegistrySettingControl
            definition={definition}
            value={setting.value}
            disabled={disabled}
            onChange={(value) => onChange(setting, value)}
          />
        )}
        {isOverride ? (
          <button
            type="button"
            onClick={() => onReset(setting)}
            className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 self-end text-[11.5px] transition-colors"
          >
            <RotateCcw className="h-3 w-3" />
            {t("web.admin.common.deviceOverrides.reset")}
          </button>
        ) : (
          <span className="text-muted-foreground/60 self-end text-[11px] italic">
            {t("web.admin.common.deviceOverrides.changeToOverride")}
          </span>
        )}
      </div>
    </div>
  );
}

/** "from Alex's profile", "app default", plus where something else may apply. */
function inheritedSourceText(t: Translate, inherited: InheritedValue): string {
  const { profileName } = inherited;
  const source =
    profileName !== null
      ? t("web.admin.common.deviceOverrides.source.profile", { name: profileName })
      : t("web.admin.common.deviceOverrides.source.appDefault");
  return inherited.orVaries
    ? t("web.admin.common.deviceOverrides.source.orVaries", {
        source,
        scope: inherited.orVaries.replace(/ /g, "_"),
      })
    : source;
}

function profileAccent(profileId: string): string {
  let hash = 0;
  for (let i = 0; i < profileId.length; i++) {
    hash = (hash * 31 + profileId.charCodeAt(i)) | 0;
  }
  const hues = [12, 38, 145, 195, 220, 265, 295, 335];
  const hue = hues[Math.abs(hash) % hues.length];
  return `hsl(${hue} 70% 62%)`;
}

export interface DeviceProfileTabEntry {
  profileId: string;
  profileName: string;
  settings: AdminDeviceSetting[];
}

export interface DeviceProfileTabsProps {
  profiles: DeviceProfileTabEntry[];
  initialProfileId?: string | null;
  /**
   * When true, every device-scoped setting from the manifest is rendered in
   * the active profile, regardless of whether an override exists. Rows
   * without an override are visually muted and show the value they inherit
   * (see `storedSettings`); changing them creates a new override via the same
   * mutation.
   *
   * The admin needs the `device` context to synthesize those rows so the
   * mutation handler has the right (userId, deviceId) keys to call.
   */
  showAllSettings?: boolean;
  /**
   * Every value the device's owner stores, at any scope. Rows the device does
   * not override show what they resolve to from these — the profile's own
   * value, else the app default — rather than the bare manifest default.
   */
  storedSettings?: readonly AdminUserSettingEntry[];
  device?: {
    userId: number;
    deviceId: string;
    deviceName: string;
    devicePlatform: string;
  };
  /**
   * Days the device has been dormant. When > 30 we propagate "stale" as a
   * per-row anomaly so overrides on dormant devices read as suspect.
   */
  deviceStaleDays?: number | null;
  onResetProfile: (profileId: string, profileName: string) => void;
  /** Forwarded to {@link DeviceOverrideRow.onEditJson}. */
  onEditJson: (setting: AdminDeviceSetting, isOverride: boolean) => void;
  onResetSetting: (setting: AdminDeviceSetting) => void;
  onChangeSetting: (setting: AdminDeviceSetting, value: string) => void;
  updatePending: boolean;
  resetPending?: boolean;
}

interface RenderedRow {
  setting: AdminDeviceSetting;
  isOverride: boolean;
  inherited?: InheritedValue;
}

/**
 * The device settings offered for a device on [devicePlatform], plus any of
 * [storedKeys] the platform would not otherwise get.
 *
 * Settings the platform never reads are left out, as on Your Devices, so an
 * admin is not invited to set something that changes nothing there. A value
 * already stored for one stays listed so it can still be seen and reset.
 */
export function deviceSettingKeysForPlatform(
  devicePlatform: string | undefined,
  storedKeys: Iterable<string> = [],
): SettingKey[] {
  const platform = manifestPlatformFor(devicePlatform);
  const stored = new Set(storedKeys);
  return ALL_DEVICE_SETTING_KEYS.filter(
    (key) => stored.has(key) || settingAppliesToPlatform(key, platform),
  );
}

function buildRenderedRows(
  profile: DeviceProfileTabEntry,
  showAllSettings: boolean,
  device?: DeviceProfileTabsProps["device"],
  storedSettings: readonly AdminUserSettingEntry[] = [],
): RenderedRow[] {
  const overridden: RenderedRow[] = profile.settings.map((setting) => ({
    setting,
    isOverride: true,
  }));

  if (!showAllSettings || !device) return overridden;

  const seen = new Set(overridden.map((r) => r.setting.key));
  const offered = new Set<string>(deviceSettingKeysForPlatform(device.devicePlatform));
  const profileAllEntries = storedSettings.filter(
    (entry) => entry.profile_id === profile.profileId,
  );
  const profileEntries = profileAllEntries.filter((entry) => entry.scope === "profile");
  const synthetics: RenderedRow[] = ALL_DEVICE_SETTING_KEYS.filter(
    (key) => !seen.has(key) && offered.has(key),
  ).map((key) => {
    const inherited = deviceInheritedValue(
      key,
      profile.profileName,
      profileEntries,
      profileAllEntries,
    );
    return {
      isOverride: false,
      inherited,
      setting: {
        user_id: device.userId,
        profile_id: profile.profileId,
        profile_name: profile.profileName,
        device_id: device.deviceId,
        device_name: device.deviceName,
        device_platform: device.devicePlatform,
        key: key as string,
        value: inherited.raw,
        updated_at: "",
      },
    };
  });

  // Render in canonical manifest order so the layout is stable as overrides
  // are added or removed (rather than "overrides bubble to top, defaults
  // sink").  Stable order > recency for an admin browsing fields.
  const order = new Map<string, number>(ALL_DEVICE_SETTING_KEYS.map((key, idx) => [key, idx]));
  const all = [...overridden, ...synthetics];
  all.sort(
    (a, b) =>
      (order.get(a.setting.key) ?? Number.MAX_SAFE_INTEGER) -
      (order.get(b.setting.key) ?? Number.MAX_SAFE_INTEGER),
  );
  return all;
}

export function DeviceProfileTabs({
  profiles,
  initialProfileId,
  showAllSettings = false,
  storedSettings,
  device,
  deviceStaleDays = null,
  onResetProfile,
  onEditJson,
  onResetSetting,
  onChangeSetting,
  updatePending,
  resetPending = false,
}: DeviceProfileTabsProps) {
  const t = useT();
  const [activeId, setActiveId] = useState<string | null>(initialProfileId ?? null);

  const active = useMemo(
    () => profiles.find((p) => p.profileId === activeId) ?? profiles[0] ?? null,
    [activeId, profiles],
  );
  const rows = useMemo(
    () => (active ? buildRenderedRows(active, showAllSettings, device, storedSettings) : []),
    [active, device, showAllSettings, storedSettings],
  );
  // Conflicts depend on the active profile's settings as a whole. Memoize
  // by the active profile's reference + a content hash via the rendered
  // row keys/values — recomputes on profile switch and on save.
  const conflictMap = useMemo(() => detectSettingConflicts(active?.settings ?? [], t), [active, t]);
  const anomaliesByKey = useMemo(() => {
    const out = new Map<string, SettingAnomaly>();
    for (const { setting, isOverride } of rows) {
      const a = getSettingAnomaly(setting, isOverride, t, {
        deviceStaleDays,
        conflicts: conflictMap,
      });
      if (a) out.set(setting.key, a);
    }
    return out;
  }, [rows, deviceStaleDays, conflictMap, t]);

  if (!active) return null;

  const accent = profileAccent(active.profileId);
  const overrideCount = active.settings.length;
  const anomalyCountInProfile = anomaliesByKey.size;

  return (
    // Flex column with min-h-0 so the override-rows section can own the
    // scroll: the tab strip and profile metadata bar stay pinned at the
    // top, the scrollbar lives only beside the rows below — not next to
    // the profiles strip itself.
    <div className="flex h-full min-h-0 flex-col">
      <div
        role="tablist"
        aria-label={t("web.admin.common.deviceOverrides.tabs.label")}
        className="border-border/60 scrollbar-thin flex shrink-0 gap-0.5 overflow-x-auto border-b px-2 pt-2"
      >
        {profiles.map((profile) => {
          const isActive = profile.profileId === active.profileId;
          const tabAccent = profileAccent(profile.profileId);
          const initial =
            (profile.profileName || profile.profileId).trim().charAt(0).toUpperCase() || "?";
          return (
            <button
              key={profile.profileId}
              type="button"
              role="tab"
              aria-selected={isActive}
              onClick={() => setActiveId(profile.profileId)}
              className={cn(
                "group relative flex shrink-0 items-center gap-2 rounded-t-md px-3 py-2 whitespace-nowrap transition-colors",
                isActive
                  ? "text-foreground bg-surface/60"
                  : "text-muted-foreground hover:text-foreground hover:bg-surface/30",
              )}
              style={
                isActive
                  ? {
                      backgroundImage: `linear-gradient(180deg, color-mix(in oklab, ${tabAccent} 16%, transparent), transparent 70%)`,
                    }
                  : undefined
              }
            >
              <span
                className="border-border/60 bg-surface-raised/70 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border text-[10px] font-semibold"
                style={{ color: tabAccent }}
                aria-hidden
              >
                {initial}
              </span>
              <span className="max-w-[120px] truncate text-[12.5px] font-medium">
                {profile.profileName || t("web.admin.common.deviceOverrides.tabs.unnamed")}
              </span>
              <span
                className={cn(
                  "rounded-full border px-1.5 py-0.5 text-[10.5px] tabular-nums transition-colors",
                  isActive
                    ? "border-border/70 bg-background/70 text-foreground"
                    : "border-border/40 bg-background/30 text-muted-foreground",
                )}
              >
                {profile.settings.length}
              </span>
              <span
                aria-hidden
                className={cn(
                  "absolute inset-x-2 -bottom-px h-[2px] rounded-t transition-opacity",
                  isActive ? "opacity-100" : "opacity-0",
                )}
                style={{ backgroundColor: tabAccent }}
              />
            </button>
          );
        })}
      </div>

      <div
        className="border-border/60 flex shrink-0 flex-wrap items-center justify-between gap-3 border-b px-4 py-2.5"
        style={{
          backgroundImage: `linear-gradient(calc(90deg * var(--dir-sign)), color-mix(in oklab, ${accent} 8%, transparent), transparent 70%)`,
        }}
      >
        <div className="text-muted-foreground flex min-w-0 items-center gap-2 text-[11px]">
          <span
            aria-hidden
            className="inline-block h-1.5 w-1.5 rounded-full"
            style={{ backgroundColor: accent }}
          />
          <span className="truncate font-mono">{active.profileId}</span>
          <span className="text-muted-foreground/40">{DOT}</span>
          <span className="tabular-nums">
            {t("web.admin.common.deviceOverrides.summary.overrides", {
              count: overrideCount,
              count_text: overrideCount,
              b: (chunks) => <span className="text-foreground/80 font-medium">{chunks}</span>,
            })}
            {showAllSettings && rows.length > overrideCount && (
              <span className="text-muted-foreground/60">
                {SEPARATOR}
                {t("web.admin.common.deviceOverrides.summary.inherited", {
                  count: rows.length - overrideCount,
                  count_text: rows.length - overrideCount,
                })}
              </span>
            )}
          </span>
          {anomalyCountInProfile > 0 && (
            <>
              <span className="text-muted-foreground/40">{DOT}</span>
              <span
                className="text-destructive bg-destructive/10 inline-flex items-center gap-1 rounded px-1.5 py-px text-[10px] font-medium tracking-[0.04em] uppercase"
                title={t("web.admin.common.deviceOverrides.summary.suspectTitle")}
              >
                <AlertTriangle className="h-2.5 w-2.5" />
                {t("web.admin.common.deviceOverrides.summary.suspect", {
                  count: anomalyCountInProfile,
                  count_text: anomalyCountInProfile,
                })}
              </span>
            </>
          )}
        </div>
        <button
          type="button"
          // The tab's own id, not the first row's: a profile registered on the
          // device with no override yet has no row to read it from.
          onClick={() => onResetProfile(active.profileId, active.profileName)}
          disabled={resetPending || overrideCount === 0}
          className="text-muted-foreground hover:text-foreground disabled:hover:text-muted-foreground inline-flex items-center gap-1 text-[11.5px] transition-colors disabled:opacity-40"
        >
          <RotateCcw className="h-3 w-3" />
          {t("web.admin.common.deviceOverrides.summary.resetAll")}
        </button>
      </div>

      <div className="overlay-scroll min-h-0 flex-1 overflow-y-auto">
        <div className="divide-border/40 divide-y px-4">
          {rows.map(({ setting, isOverride, inherited }) => (
            <DeviceOverrideRow
              key={`${setting.profile_id}:${setting.device_id}:${setting.key}`}
              setting={setting}
              isOverride={isOverride}
              inherited={inherited}
              anomaly={anomaliesByKey.get(setting.key) ?? null}
              disabled={updatePending}
              onChange={onChangeSetting}
              onEditJson={onEditJson}
              onReset={onResetSetting}
            />
          ))}
        </div>
      </div>
    </div>
  );
}
