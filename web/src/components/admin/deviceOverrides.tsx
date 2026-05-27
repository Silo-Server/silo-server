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
  formatSettingValue,
  getSettingDefinition,
} from "@/lib/settingsManifest";
import { cn } from "@/lib/utils";
import type { AdminDeviceSetting } from "@/hooks/queries/admin/users";

export const UNKNOWN_PROFILE_ID = "unknown-profile";

export type PlatformKind = "tv" | "mobile" | "tablet" | "desktop" | "unknown";

const PLATFORM_KIND_LABELS: Record<PlatformKind, string> = {
  tv: "TV",
  mobile: "Mobile",
  tablet: "Tablet",
  desktop: "Desktop",
  unknown: "Other",
};

export function platformKindLabel(kind: PlatformKind): string {
  return PLATFORM_KIND_LABELS[kind];
}

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

export function platformLabel(raw: string | undefined): string {
  return raw && raw.trim().length > 0 ? raw : "Unknown platform";
}

export function formatRelative(iso: string | undefined): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  const diff = Date.now() - then;
  const m = Math.round(diff / 60000);
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d}d ago`;
  return new Date(iso).toLocaleDateString();
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
      reason: "Conflicts with HDR disabled — the fallback only applies when HDR is on.",
    });
    out.set("player.hdr_enabled", {
      kind: "conflict",
      reason: "Conflicts with the Profile 7 HDR10 fallback override above.",
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
      reason: "Skipping credits with auto-play next disabled lands the player on a blank screen.",
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
    definition?.control === "slider" &&
    definition.min !== undefined &&
    definition.max !== undefined
  ) {
    const v = Number(setting.value);
    const def = Number(definition.defaultValue ?? 0);
    if (Number.isFinite(v) && Number.isFinite(def)) {
      const range = Math.abs(definition.max - definition.min);
      const distance = Math.abs(v - def);
      if (range > 0 && distance / range >= 0.4) {
        const unit = definition.unit ? ` ${definition.unit}` : "";
        return {
          kind: "extreme",
          reason: `Far from the default (${v}${unit} vs ${def}${unit}) — likely a workaround that wasn't reverted.`,
        };
      }
    }
  }

  // Stale: device itself is dormant. Every override on it is suspect.
  if (options.deviceStaleDays != null && options.deviceStaleDays > 30) {
    return {
      kind: "stale",
      reason: `Device hasn't been seen in ${options.deviceStaleDays} days — this override may be obsolete.`,
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
  onChange,
  onEditJson,
  onReset,
}: DeviceOverrideRowProps) {
  const definition = getSettingDefinition(setting.key);
  const isJsonOnly = !definition || definition.control === "json";

  const anomalyBadgeLabel: Record<SettingAnomalyKind, string> = {
    extreme: "extreme",
    conflict: "conflict",
    stale: "stale",
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
        anomaly && "border-destructive/40 -ml-3 border-l-2 pl-3",
      )}
    >
      <div className="min-w-0 space-y-0.5">
        <div className="flex items-center gap-2">
          <span className="text-foreground text-[13.5px] leading-tight font-medium">
            {definition?.label ?? setting.key}
          </span>
          {isOverride ? (
            <span
              className="bg-foreground/80 inline-block h-1.5 w-1.5 shrink-0 rounded-full"
              title="Override is set on this device"
              aria-label="overridden"
            />
          ) : (
            <span className="text-muted-foreground/60 border-border/50 rounded-full border px-1.5 py-px text-[9.5px] font-medium tracking-[0.05em] uppercase">
              default
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
            {definition.description}
          </p>
        )}
        <div className="text-muted-foreground pt-0.5 text-[11.5px]">
          <span className="text-foreground/80">
            {formatSettingValue(setting.key, setting.value)}
          </span>
          {!isJsonOnly && definition?.defaultValue !== undefined && (
            <span className="text-muted-foreground/60">
              {" "}
              · default {formatSettingValue(setting.key, definition.defaultValue)}
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
        {isJsonOnly ? (
          <Button variant="outline" size="sm" onClick={() => onEditJson(setting, isOverride)}>
            {setting.key === "subtitle_appearance" ? "Customize" : "Edit JSON"}
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
            Reset
          </button>
        ) : (
          <span className="text-muted-foreground/60 self-end text-[11px] italic">
            change to add override
          </span>
        )}
      </div>
    </div>
  );
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
   * without an override are visually muted and use the manifest default
   * value; changing them creates a new override via the same mutation.
   *
   * The admin needs the `device` context to synthesize those rows so the
   * mutation handler has the right (userId, deviceId) keys to call.
   */
  showAllSettings?: boolean;
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
}

function buildRenderedRows(
  profile: DeviceProfileTabEntry,
  showAllSettings: boolean,
  device?: DeviceProfileTabsProps["device"],
): RenderedRow[] {
  const overridden: RenderedRow[] = profile.settings.map((setting) => ({
    setting,
    isOverride: true,
  }));

  if (!showAllSettings || !device) return overridden;

  const seen = new Set(overridden.map((r) => r.setting.key));
  const synthetics: RenderedRow[] = ALL_DEVICE_SETTING_KEYS.filter((key) => !seen.has(key)).map(
    (key) => {
      const definition = getSettingDefinition(key);
      return {
        isOverride: false,
        setting: {
          user_id: device.userId,
          profile_id: profile.profileId,
          profile_name: profile.profileName,
          device_id: device.deviceId,
          device_name: device.deviceName,
          device_platform: device.devicePlatform,
          key,
          value: definition?.defaultValue ?? "",
          updated_at: "",
        },
      };
    },
  );

  // Render in canonical manifest order so the layout is stable as overrides
  // are added or removed (rather than "overrides bubble to top, defaults
  // sink").  Stable order > recency for an admin browsing fields.
  const order = new Map(ALL_DEVICE_SETTING_KEYS.map((key, idx) => [key, idx]));
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
  device,
  deviceStaleDays = null,
  onResetProfile,
  onEditJson,
  onResetSetting,
  onChangeSetting,
  updatePending,
  resetPending = false,
}: DeviceProfileTabsProps) {
  const [activeId, setActiveId] = useState<string | null>(initialProfileId ?? null);

  const active = useMemo(
    () => profiles.find((p) => p.profileId === activeId) ?? profiles[0] ?? null,
    [activeId, profiles],
  );
  const rows = useMemo(
    () => (active ? buildRenderedRows(active, showAllSettings, device) : []),
    [active, device, showAllSettings],
  );
  // Conflicts depend on the active profile's settings as a whole. Memoize
  // by the active profile's reference + a content hash via the rendered
  // row keys/values — recomputes on profile switch and on save.
  const conflictMap = useMemo(() => detectSettingConflicts(active?.settings ?? []), [active]);
  const anomaliesByKey = useMemo(() => {
    const out = new Map<string, SettingAnomaly>();
    for (const { setting, isOverride } of rows) {
      const a = getSettingAnomaly(setting, isOverride, {
        deviceStaleDays,
        conflicts: conflictMap,
      });
      if (a) out.set(setting.key, a);
    }
    return out;
  }, [rows, deviceStaleDays, conflictMap]);

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
        aria-label="Profiles on this device"
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
                {profile.profileName || "Unnamed"}
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
          backgroundImage: `linear-gradient(90deg, color-mix(in oklab, ${accent} 8%, transparent), transparent 70%)`,
        }}
      >
        <div className="text-muted-foreground flex min-w-0 items-center gap-2 text-[11px]">
          <span
            aria-hidden
            className="inline-block h-1.5 w-1.5 rounded-full"
            style={{ backgroundColor: accent }}
          />
          <span className="truncate font-mono">{active.profileId}</span>
          <span className="text-muted-foreground/40">·</span>
          <span className="tabular-nums">
            <span className="text-foreground/80 font-medium">{overrideCount}</span>{" "}
            {overrideCount === 1 ? "override" : "overrides"}
            {showAllSettings && rows.length > overrideCount && (
              <span className="text-muted-foreground/60">
                {" "}
                · {rows.length - overrideCount} default
              </span>
            )}
          </span>
          {anomalyCountInProfile > 0 && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span
                className="text-destructive bg-destructive/10 inline-flex items-center gap-1 rounded px-1.5 py-px text-[10px] font-medium tracking-[0.04em] uppercase"
                title="One or more overrides in this profile look suspect — see the highlighted rows below."
              >
                <AlertTriangle className="h-2.5 w-2.5" />
                {anomalyCountInProfile} suspect
              </span>
            </>
          )}
        </div>
        <button
          type="button"
          onClick={() => onResetProfile(active.settings[0]?.profile_id ?? "", active.profileName)}
          disabled={resetPending || overrideCount === 0}
          className="text-muted-foreground hover:text-foreground disabled:hover:text-muted-foreground inline-flex items-center gap-1 text-[11.5px] transition-colors disabled:opacity-40"
        >
          <RotateCcw className="h-3 w-3" />
          Reset all overrides
        </button>
      </div>

      <div className="overlay-scroll min-h-0 flex-1 overflow-y-auto">
        <div className="divide-border/40 divide-y px-4">
          {rows.map(({ setting, isOverride }) => (
            <DeviceOverrideRow
              key={`${setting.profile_id}:${setting.device_id}:${setting.key}`}
              setting={setting}
              isOverride={isOverride}
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
