import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import {
  Activity,
  AlertTriangle,
  ArrowUpRight,
  ChevronRight,
  Clock,
  Search,
  Sparkles,
  Subtitles,
  X,
} from "lucide-react";
import {
  type AdminDeviceSetting,
  useAdminDeviceDetail,
  useAdminDevices,
  useDeleteAdminUserDeviceSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserDeviceSetting,
} from "@/hooks/queries/admin/users";
import type { AdminDeviceSummary } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import {
  DeviceProfileTabs,
  PlatformTile,
  UNKNOWN_PROFILE_ID,
  classifyPlatform,
  formatRelative,
  platformKindLabel,
  platformLabel,
  type DeviceProfileTabEntry,
  type PlatformKind,
} from "@/components/admin/deviceOverrides";
import { ALL_DEVICE_SETTING_KEYS } from "@/lib/settingsManifest";
import { AdminSubtitleAppearanceDialog } from "@/components/admin/AdminSubtitleAppearanceDialog";
import { cn } from "@/lib/utils";

// ─────────────────────────────────────────────────────────────────────
// types & constants
// ─────────────────────────────────────────────────────────────────────

type OverrideRange = "all" | "none" | "1-2" | "3-5" | "6+";
type RecencyBucket = "all" | "<24h" | "<7d" | "<30d" | ">30d";
type GroupBy = "user" | "platform" | "activity";
type SavedView = "anomalies" | "recent" | "hdr" | "subtitle" | "dormant";

const DAY_MS = 86_400_000;
const PLATFORM_ORDER: PlatformKind[] = ["tv", "mobile", "tablet", "desktop", "unknown"];
const OVERRIDE_RANGES: Exclude<OverrideRange, "all">[] = ["none", "1-2", "3-5", "6+"];
const RECENCY_BUCKETS: Exclude<RecencyBucket, "all">[] = ["<24h", "<7d", "<30d", ">30d"];

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

function ageInDays(iso: string | undefined): number | null {
  if (!iso) return null;
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return null;
  return (Date.now() - t) / DAY_MS;
}

function bucketRecency(iso: string | undefined): Exclude<RecencyBucket, "all"> {
  const d = ageInDays(iso);
  if (d == null) return ">30d";
  if (d < 1) return "<24h";
  if (d < 7) return "<7d";
  if (d < 30) return "<30d";
  return ">30d";
}

function bucketOverrideCount(n: number): Exclude<OverrideRange, "all"> {
  if (n === 0) return "none";
  if (n <= 2) return "1-2";
  if (n <= 5) return "3-5";
  return "6+";
}

/**
 * Flag devices whose override pattern looks unusual relative to their
 * platform peers. We're conservative here — only data we have at the
 * list level (counts, last-update timestamps) — but two real signals:
 *   1. override_count is significantly higher than the platform median
 *   2. device hasn't been touched in 30+ days but still has overrides
 * Deeper per-key divergence detection requires the detail endpoint and
 * is left for a follow-up.
 */
/**
 * Reason a device was flagged anomalous. Carries the actual numbers so the
 * detail pane can show *why* — "6 overrides · 2× iOS median (3)" beats a
 * vague "diverges from platform peers" every time.
 */
export interface DeviceAnomalyReason {
  /** Which signals triggered the flag — both can be true. */
  outlier: boolean;
  stale: boolean;
  /** Override count on this device. */
  overrideCount: number;
  /** Median override count across same-platform peers. */
  platformMedian: number;
  /** Platform classification used for the comparison. */
  platform: PlatformKind;
  /** Days since `last_updated`, rounded down. Null if unknown. */
  staleDays: number | null;
  /** One-line summary suitable for a 2-line readout cell. */
  summary: string;
  /** Longer human explanation, used for tooltips and inline reason text. */
  detail: string;
}

function detectAnomalies(devices: AdminDeviceSummary[]): Map<string, DeviceAnomalyReason> {
  const flagged = new Map<string, DeviceAnomalyReason>();
  if (devices.length === 0) return flagged;

  const byPlatform = new Map<PlatformKind, number[]>();
  devices.forEach((d) => {
    const k = classifyPlatform(d.device_platform);
    const arr = byPlatform.get(k) ?? [];
    arr.push(d.override_count ?? 0);
    byPlatform.set(k, arr);
  });
  const medianByPlatform = new Map<PlatformKind, number>();
  byPlatform.forEach((arr, k) => {
    const sorted = [...arr].sort((a, b) => a - b);
    const mid = Math.floor(sorted.length / 2);
    medianByPlatform.set(k, sorted[mid] ?? 0);
  });

  devices.forEach((d) => {
    const platform = classifyPlatform(d.device_platform);
    const count = d.override_count ?? 0;
    const median = medianByPlatform.get(platform) ?? 0;
    const outlier = count >= 6 || (median > 0 && count >= median * 2 && count >= 4);
    const days = ageInDays(d.last_updated);
    const stale = (days ?? 0) > 30 && count > 0;

    if (!outlier && !stale) return;

    const platformText = platformKindLabel(platform).toLowerCase();
    const summaryParts: string[] = [];
    const detailParts: string[] = [];

    if (outlier) {
      const ratio = median > 0 ? Math.round((count / median) * 10) / 10 : null;
      summaryParts.push(
        ratio != null && ratio >= 1.2
          ? `${count} overrides · ${ratio}× ${platformText} median`
          : `${count} overrides`,
      );
      detailParts.push(
        median > 0
          ? `Has ${count} overrides — ${ratio}× the ${platformText} median of ${median}.`
          : `Has ${count} overrides — well above the typical ${platformText} device.`,
      );
    }

    if (stale) {
      const staleDaysFloor = Math.floor(days ?? 0);
      summaryParts.push(`stale ${staleDaysFloor}d`);
      detailParts.push(
        `Last activity ${staleDaysFloor} days ago — overrides may no longer reflect how the device is used.`,
      );
    }

    flagged.set(d.device_id, {
      outlier,
      stale,
      overrideCount: count,
      platformMedian: median,
      platform,
      staleDays: stale && days != null ? Math.floor(days) : null,
      summary: summaryParts.join(" · "),
      detail: detailParts.join(" "),
    });
  });
  return flagged;
}

function deviceKeyHints(device: AdminDeviceSummary): string {
  // We don't have key info on the summary, so we approximate "HDR-related"
  // and "Subtitle custom" using the override count + platform — good enough
  // as a soft hint for saved-view filtering until we add a key facet to the
  // summary endpoint.
  return [device.device_platform, device.device_name].filter(Boolean).join(" ").toLowerCase();
}

// ─────────────────────────────────────────────────────────────────────
// page
// ─────────────────────────────────────────────────────────────────────

export default function AdminDevices() {
  const { userId: userIdParam, deviceId: deviceIdParam } = useParams<{
    userId?: string;
    deviceId?: string;
  }>();
  const [searchParams] = useSearchParams();
  const currentUserId = Number(userIdParam ?? 0);
  const currentDeviceId = deviceIdParam ?? "";
  const currentProfileId = searchParams.get("profile");

  const { data: devices = [], isLoading } = useAdminDevices();

  // filters
  const [search, setSearch] = useState("");
  const [platforms, setPlatforms] = useState<Set<PlatformKind>>(new Set());
  const [overrideRange, setOverrideRange] = useState<Set<Exclude<OverrideRange, "all">>>(new Set());
  const [recency, setRecency] = useState<Set<Exclude<RecencyBucket, "all">>>(new Set());
  const [activeView, setActiveView] = useState<SavedView | null>(null);
  const [groupBy, setGroupBy] = useState<GroupBy>("user");
  const [overridesOnly, setOverridesOnly] = useState(false);

  // ⌘K focuses the global search input
  const searchRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        searchRef.current?.focus();
        searchRef.current?.select();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // anomaly detection
  const anomalies = useMemo(() => detectAnomalies(devices), [devices]);
  const scopedDevices = useMemo(
    () => (overridesOnly ? devices.filter((d) => (d.override_count ?? 0) > 0) : devices),
    [devices, overridesOnly],
  );

  // Facet counts follow the top-level device scope, but not the local facet
  // filters, so each row shows "what would I get inside this scope".
  const platformCounts = useMemo(() => {
    const m = new Map<PlatformKind, number>();
    scopedDevices.forEach((d) => {
      const k = classifyPlatform(d.device_platform);
      m.set(k, (m.get(k) ?? 0) + 1);
    });
    return m;
  }, [scopedDevices]);

  const overrideRangeCounts = useMemo(() => {
    const m = new Map<Exclude<OverrideRange, "all">, number>();
    scopedDevices.forEach((d) => {
      const b = bucketOverrideCount(d.override_count ?? 0);
      m.set(b, (m.get(b) ?? 0) + 1);
    });
    return m;
  }, [scopedDevices]);

  const recencyCounts = useMemo(() => {
    const m = new Map<Exclude<RecencyBucket, "all">, number>();
    scopedDevices.forEach((d) => {
      const b = bucketRecency(d.last_updated);
      m.set(b, (m.get(b) ?? 0) + 1);
    });
    return m;
  }, [scopedDevices]);

  const savedViewCounts = useMemo(
    () => ({
      anomalies: scopedDevices.filter((d) => anomalies.has(d.device_id)).length,
      recent: scopedDevices.filter((d) => (ageInDays(d.last_updated) ?? Infinity) < 7).length,
      hdr: scopedDevices.filter((d) => /tv|appletv|tvos|shield/i.test(deviceKeyHints(d))).length,
      subtitle: scopedDevices.filter((d) => (d.override_count ?? 0) >= 3).length,
      dormant: scopedDevices.filter((d) => (ageInDays(d.last_updated) ?? 0) > 30).length,
    }),
    [anomalies, scopedDevices],
  );

  // filter pipeline
  const filteredDevices = useMemo(() => {
    const query = search.trim().toLowerCase();
    return scopedDevices.filter((device) => {
      // saved view
      if (activeView === "anomalies" && !anomalies.has(device.device_id)) return false;
      if (activeView === "recent" && (ageInDays(device.last_updated) ?? Infinity) >= 7)
        return false;
      if (activeView === "hdr" && !/tv|appletv|tvos|shield/i.test(deviceKeyHints(device)))
        return false;
      if (activeView === "subtitle" && (device.override_count ?? 0) < 3) return false;
      if (activeView === "dormant" && (ageInDays(device.last_updated) ?? 0) <= 30) return false;

      // platform
      if (platforms.size > 0 && !platforms.has(classifyPlatform(device.device_platform)))
        return false;

      // override count
      if (
        overrideRange.size > 0 &&
        !overrideRange.has(bucketOverrideCount(device.override_count ?? 0))
      )
        return false;

      // recency
      if (recency.size > 0 && !recency.has(bucketRecency(device.last_updated))) return false;

      // free-text search
      if (query) {
        const profileText = (device.profiles ?? [])
          .map((p) => `${p.profile_name} ${p.profile_id}`)
          .join(" ");
        const haystack = [
          device.device_name,
          device.device_id,
          device.username,
          device.email,
          device.device_platform,
          profileText,
        ]
          .filter(Boolean)
          .join(" ")
          .toLowerCase();
        if (!haystack.includes(query)) return false;
      }
      return true;
    });
  }, [scopedDevices, anomalies, activeView, platforms, overrideRange, recency, search]);

  // group pivot
  const groups = useMemo(() => buildGroups(filteredDevices, groupBy), [filteredDevices, groupBy]);

  // header stats (computed on full set so they don't change as you filter)
  const totalUsers = useMemo(() => new Set(devices.map((d) => d.user_id)).size, [devices]);
  const totalProfiles = useMemo(
    () => devices.reduce((sum, d) => sum + (d.profiles?.length ?? 0), 0),
    [devices],
  );
  const totalOverrides = useMemo(
    () => devices.reduce((sum, d) => sum + (d.override_count ?? 0), 0),
    [devices],
  );
  const devicesWithOverrides = useMemo(
    () => devices.filter((d) => (d.override_count ?? 0) > 0).length,
    [devices],
  );

  const hasAnyFilter =
    activeView !== null ||
    overridesOnly ||
    platforms.size > 0 ||
    overrideRange.size > 0 ||
    recency.size > 0 ||
    search.length > 0;

  const clearAllFilters = () => {
    setActiveView(null);
    setPlatforms(new Set());
    setOverrideRange(new Set());
    setRecency(new Set());
    setOverridesOnly(false);
    setSearch("");
  };

  return (
    <div className="mx-auto w-full max-w-[1920px] space-y-5 px-4 py-4 sm:px-6 sm:py-6 lg:px-8 xl:px-10">
      {/* page header */}
      <div className="page-header items-end gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Devices</h1>
          <p className="page-subtitle max-w-2xl text-sm sm:text-base">
            Inspect, tune, and reset per-profile playback overrides across the fleet. Filter by
            user, platform, or override pattern. Press{" "}
            <kbd className="bg-surface/70 border-border/70 text-foreground/80 inline-flex items-center rounded border px-1.5 py-0.5 font-mono text-[10.5px]">
              ⌘K
            </kbd>{" "}
            to jump.
          </p>
        </div>

        <div className="relative w-full sm:w-[340px]">
          <Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
          <Input
            ref={searchRef}
            className="bg-background/60 h-9 pr-14 pl-9 text-[13px]"
            placeholder="Search devices, users, IDs, profiles…"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
          {search ? (
            <button
              type="button"
              onClick={() => setSearch("")}
              className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2 rounded p-1 transition-colors"
              aria-label="Clear search"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          ) : (
            <kbd className="border-border/70 text-muted-foreground absolute top-1/2 right-2.5 -translate-y-1/2 rounded border px-1.5 py-0.5 font-mono text-[10px]">
              ⌘K
            </kbd>
          )}
        </div>
      </div>

      {/* fleet pulse */}
      {!isLoading && devices.length > 0 && (
        <FleetPulse
          totalUsers={totalUsers}
          totalDevices={devices.length}
          totalProfiles={totalProfiles}
          totalOverrides={totalOverrides}
          anomalyCount={anomalies.size}
          devices={devices}
          groupBy={groupBy}
          onGroupByChange={setGroupBy}
        />
      )}

      <div className="flex justify-start">
        <DeviceScopeControl
          overridesOnly={overridesOnly}
          totalDevices={devices.length}
          devicesWithOverrides={devicesWithOverrides}
          onChange={setOverridesOnly}
        />
      </div>

      {/* main 3-pane */}
      <div className="grid gap-4 lg:grid-cols-[200px_minmax(300px,360px)_minmax(0,1fr)] xl:grid-cols-[220px_minmax(320px,380px)_minmax(0,1fr)]">
        {/* filter rail */}
        <FilterRail
          activeView={activeView}
          onViewChange={setActiveView}
          savedViewCounts={savedViewCounts}
          platforms={platforms}
          onPlatformsChange={setPlatforms}
          platformCounts={platformCounts}
          overrideRange={overrideRange}
          onOverrideRangeChange={setOverrideRange}
          overrideRangeCounts={overrideRangeCounts}
          recency={recency}
          onRecencyChange={setRecency}
          recencyCounts={recencyCounts}
          hasAnyFilter={hasAnyFilter}
          onClearAll={clearAllFilters}
        />

        {/* device list */}
        <section className="surface-panel flex flex-col overflow-hidden rounded-xl border-0">
          <div className="border-border/60 text-muted-foreground flex items-center justify-between border-b px-3 py-2.5 text-[10.5px] tracking-[0.08em] uppercase">
            <span>
              {filteredDevices.length}
              {filteredDevices.length !== devices.length && (
                <span className="text-muted-foreground/60"> of {devices.length}</span>
              )}{" "}
              {filteredDevices.length === 1 ? "device" : "devices"}
            </span>
            {hasAnyFilter && (
              <button
                type="button"
                onClick={clearAllFilters}
                className="hover:text-foreground transition-colors"
              >
                clear all
              </button>
            )}
          </div>

          <div className="overlay-scroll max-h-[calc(100vh-22rem)] flex-1 overflow-y-auto lg:max-h-[calc(100vh-18rem)]">
            {isLoading ? (
              <div className="space-y-1.5 p-2.5">
                {Array.from({ length: 5 }).map((_, i) => (
                  <Skeleton key={i} className="h-[58px] w-full rounded-md" />
                ))}
              </div>
            ) : filteredDevices.length === 0 ? (
              <EmptyFleet hasDevices={devices.length > 0} onClear={clearAllFilters} />
            ) : (
              <div className="divide-border/60 divide-y">
                {groups.map((group) => (
                  <DeviceGroup
                    key={`${groupBy}:${group.key}`}
                    group={group}
                    anomalies={anomalies}
                    currentUserId={currentUserId}
                    currentDeviceId={currentDeviceId}
                    currentProfileId={currentProfileId}
                    forceOpen={hasAnyFilter || groups.length === 1}
                  />
                ))}
              </div>
            )}
          </div>
        </section>

        {/* detail */}
        <section className="surface-panel overflow-hidden rounded-xl border-0">
          {currentUserId > 0 && currentDeviceId ? (
            <DeviceDetailPanel
              key={`${currentUserId}:${currentDeviceId}`}
              userId={currentUserId}
              deviceId={currentDeviceId}
              initialProfileId={currentProfileId}
              anomaly={anomalies.get(currentDeviceId) ?? null}
              defaultShowAllSettings={!overridesOnly}
            />
          ) : (
            <EmptyDetail />
          )}
        </section>
      </div>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────
// Fleet pulse strip — stats + density histogram + group-by toggle
// ─────────────────────────────────────────────────────────────────────

function FleetPulse({
  totalUsers,
  totalDevices,
  totalProfiles,
  totalOverrides,
  anomalyCount,
  devices,
  groupBy,
  onGroupByChange,
}: {
  totalUsers: number;
  totalDevices: number;
  totalProfiles: number;
  totalOverrides: number;
  anomalyCount: number;
  devices: AdminDeviceSummary[];
  groupBy: GroupBy;
  onGroupByChange: (v: GroupBy) => void;
}) {
  // sorted override counts for the histogram
  const sortedCounts = useMemo(
    () => [...devices].map((d) => d.override_count ?? 0).sort((a, b) => b - a),
    [devices],
  );
  const maxCount = sortedCounts[0] ?? 0;

  return (
    <div className="surface-panel-subtle text-muted-foreground flex flex-wrap items-center gap-x-6 gap-y-3 rounded-xl px-4 py-3 text-[12px]">
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <Stat label="users" value={totalUsers} />
        <Dot />
        <Stat label="devices" value={totalDevices} />
        <Dot />
        <Stat label="profiles" value={totalProfiles} />
        <Dot />
        <Stat label="overrides" value={totalOverrides} />
        {anomalyCount > 0 && (
          <>
            <Dot />
            <Stat label="anomalies" value={anomalyCount} tone="destructive" />
          </>
        )}
      </div>

      <div className="text-muted-foreground/80 flex items-center gap-2 font-mono text-[10px] tracking-[0.06em] uppercase">
        <span className="hidden md:inline">density</span>
        <Histogram counts={sortedCounts} max={maxCount} />
      </div>

      <div className="ml-auto flex flex-wrap items-center justify-end gap-2">
        <span className="text-muted-foreground/70 text-[10px] tracking-[0.08em] uppercase">
          group by
        </span>
        <SegmentedControl
          value={groupBy}
          onChange={onGroupByChange}
          options={[
            { value: "user", label: "User" },
            { value: "platform", label: "Platform" },
            { value: "activity", label: "Activity" },
          ]}
        />
      </div>
    </div>
  );
}

function DeviceScopeControl({
  overridesOnly,
  totalDevices,
  devicesWithOverrides,
  onChange,
}: {
  overridesOnly: boolean;
  totalDevices: number;
  devicesWithOverrides: number;
  onChange: (overridesOnly: boolean) => void;
}) {
  return (
    <div className="border-border/70 bg-background/40 inline-flex overflow-hidden rounded-md border">
      <button
        type="button"
        onClick={() => onChange(false)}
        aria-pressed={!overridesOnly}
        className={cn(
          "inline-flex items-center gap-2 px-3.5 py-2 text-[13px] font-medium transition-colors",
          !overridesOnly
            ? "bg-surface-hover/70 text-foreground"
            : "text-muted-foreground hover:bg-surface-hover/40 hover:text-foreground",
        )}
      >
        <span>All Devices</span>
        <span className="font-mono text-[11.5px] tabular-nums opacity-70">{totalDevices}</span>
      </button>
      <button
        type="button"
        onClick={() => onChange(true)}
        aria-pressed={overridesOnly}
        className={cn(
          "border-border/60 inline-flex items-center gap-2 border-l px-3.5 py-2 text-[13px] font-medium transition-colors",
          overridesOnly
            ? "bg-surface-hover/70 text-foreground"
            : "text-muted-foreground hover:bg-surface-hover/40 hover:text-foreground",
        )}
      >
        <span>Devices with Overrides</span>
        <span className="font-mono text-[11.5px] tabular-nums opacity-70">
          {devicesWithOverrides}
        </span>
      </button>
    </div>
  );
}

function SettingsScopeControl({
  showAllSettings,
  totalSettings,
  overrideCount,
  disabled,
  lockToAllSettings,
  onChange,
}: {
  showAllSettings: boolean;
  totalSettings: number;
  overrideCount: number;
  disabled: boolean;
  lockToAllSettings: boolean;
  onChange: (showAllSettings: boolean) => void;
}) {
  const canShowOverrides = !disabled && !lockToAllSettings;

  return (
    <div
      className={cn(
        "border-border/70 bg-background/40 inline-flex overflow-hidden rounded-md border",
        disabled && "opacity-40",
      )}
      title={
        disabled
          ? "This device does not have a registered profile yet"
          : lockToAllSettings
            ? "This device has no overrides yet, so every device setting is shown"
            : undefined
      }
    >
      <button
        type="button"
        onClick={() => {
          if (!disabled) onChange(true);
        }}
        disabled={disabled}
        aria-pressed={showAllSettings}
        className={cn(
          "inline-flex items-center gap-1.5 px-2.5 py-1 text-[11.5px] transition-colors disabled:cursor-not-allowed",
          showAllSettings
            ? "bg-surface-hover/70 text-foreground"
            : "text-muted-foreground hover:bg-surface-hover/40 hover:text-foreground",
        )}
      >
        <span>All Settings</span>
        <span className="font-mono text-[10.5px] tabular-nums opacity-70">{totalSettings}</span>
      </button>
      <button
        type="button"
        onClick={() => {
          if (canShowOverrides) onChange(false);
        }}
        disabled={!canShowOverrides}
        aria-pressed={!showAllSettings}
        className={cn(
          "border-border/60 inline-flex items-center gap-1.5 border-l px-2.5 py-1 text-[11.5px] transition-colors disabled:cursor-not-allowed disabled:opacity-50",
          !showAllSettings
            ? "bg-surface-hover/70 text-foreground"
            : "text-muted-foreground hover:bg-surface-hover/40 hover:text-foreground",
        )}
      >
        <span>Overrides</span>
        <span className="font-mono text-[10.5px] tabular-nums opacity-70">{overrideCount}</span>
      </button>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: number; tone?: "destructive" }) {
  return (
    <span className="inline-flex items-baseline gap-1.5">
      <span
        className={cn(
          "font-medium tabular-nums",
          tone === "destructive" ? "text-destructive" : "text-foreground",
        )}
      >
        {value}
      </span>
      <span className="text-muted-foreground/70 text-[10px] tracking-[0.08em] uppercase">
        {label}
      </span>
    </span>
  );
}

function Dot() {
  return <span className="text-muted-foreground/30">·</span>;
}

function Histogram({ counts, max }: { counts: number[]; max: number }) {
  // 28 bars feels right for the "fleet at a glance" — if there are more
  // devices we sample, fewer we just render what we have.
  const SLOTS = 28;
  const slots: number[] = useMemo(() => {
    if (counts.length === 0) return Array.from({ length: SLOTS }, () => 0);
    if (counts.length <= SLOTS) {
      return [...counts, ...Array(SLOTS - counts.length).fill(0)];
    }
    const step = counts.length / SLOTS;
    return Array.from({ length: SLOTS }, (_, i) => counts[Math.floor(i * step)] ?? 0);
  }, [counts]);

  const safeMax = Math.max(max, 1);
  return (
    <span className="flex h-6 items-end gap-[2px]" aria-hidden>
      {slots.map((n, i) => {
        const h = Math.max((n / safeMax) * 100, n > 0 ? 16 : 6);
        const tone =
          n === 0
            ? "bg-border/60"
            : n >= safeMax * 0.6
              ? "bg-foreground/85"
              : "bg-muted-foreground/70";
        return (
          <span
            key={i}
            className={cn("w-[3px] rounded-[1px] transition-colors", tone)}
            style={{ height: `${h}%` }}
          />
        );
      })}
    </span>
  );
}

function SegmentedControl<T extends string>({
  value,
  onChange,
  options,
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: string }[];
}) {
  return (
    <div className="border-border/70 bg-background/40 inline-flex overflow-hidden rounded-md border">
      {options.map((opt, i) => {
        const active = value === opt.value;
        return (
          <button
            key={opt.value}
            type="button"
            onClick={() => onChange(opt.value)}
            className={cn(
              "px-2.5 py-1 text-[11.5px] transition-colors",
              i > 0 && "border-border/60 border-l",
              active
                ? "bg-surface-hover/70 text-foreground"
                : "text-muted-foreground hover:bg-surface-hover/40 hover:text-foreground",
            )}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────
// Filter rail — saved views + facets
// ─────────────────────────────────────────────────────────────────────

function FilterRail({
  activeView,
  onViewChange,
  savedViewCounts,
  platforms,
  onPlatformsChange,
  platformCounts,
  overrideRange,
  onOverrideRangeChange,
  overrideRangeCounts,
  recency,
  onRecencyChange,
  recencyCounts,
  hasAnyFilter,
  onClearAll,
}: {
  activeView: SavedView | null;
  onViewChange: (v: SavedView | null) => void;
  savedViewCounts: Record<SavedView, number>;
  platforms: Set<PlatformKind>;
  onPlatformsChange: (next: Set<PlatformKind>) => void;
  platformCounts: Map<PlatformKind, number>;
  overrideRange: Set<Exclude<OverrideRange, "all">>;
  onOverrideRangeChange: (next: Set<Exclude<OverrideRange, "all">>) => void;
  overrideRangeCounts: Map<Exclude<OverrideRange, "all">, number>;
  recency: Set<Exclude<RecencyBucket, "all">>;
  onRecencyChange: (next: Set<Exclude<RecencyBucket, "all">>) => void;
  recencyCounts: Map<Exclude<RecencyBucket, "all">, number>;
  hasAnyFilter: boolean;
  onClearAll: () => void;
}) {
  const togglePlatform = (k: PlatformKind) => {
    const next = new Set(platforms);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    onPlatformsChange(next);
  };
  const toggleRange = (b: Exclude<OverrideRange, "all">) => {
    const next = new Set(overrideRange);
    if (next.has(b)) next.delete(b);
    else next.add(b);
    onOverrideRangeChange(next);
  };
  const toggleRecency = (b: Exclude<RecencyBucket, "all">) => {
    const next = new Set(recency);
    if (next.has(b)) next.delete(b);
    else next.add(b);
    onRecencyChange(next);
  };

  // available platforms (only render facets that have at least one device)
  const availablePlatforms = PLATFORM_ORDER.filter((k) => (platformCounts.get(k) ?? 0) > 0);

  return (
    <aside className="surface-panel hidden h-fit flex-col gap-5 overflow-hidden rounded-xl border-0 px-3 py-4 text-[12.5px] lg:flex">
      <FilterGroup
        label="Saved views"
        action={
          hasAnyFilter ? (
            <button
              type="button"
              onClick={onClearAll}
              className="text-muted-foreground hover:text-foreground italic transition-colors"
            >
              clear
            </button>
          ) : null
        }
      >
        <SavedViewRow
          icon={<AlertTriangle className="h-3.5 w-3.5" />}
          label="Anomalies"
          count={savedViewCounts.anomalies}
          active={activeView === "anomalies"}
          tone="destructive"
          onClick={() => onViewChange(activeView === "anomalies" ? null : "anomalies")}
        />
        <SavedViewRow
          icon={<Clock className="h-3.5 w-3.5" />}
          label="Updated < 7d"
          count={savedViewCounts.recent}
          active={activeView === "recent"}
          onClick={() => onViewChange(activeView === "recent" ? null : "recent")}
        />
        <SavedViewRow
          icon={<Sparkles className="h-3.5 w-3.5" />}
          label="HDR-capable"
          count={savedViewCounts.hdr}
          active={activeView === "hdr"}
          onClick={() => onViewChange(activeView === "hdr" ? null : "hdr")}
        />
        <SavedViewRow
          icon={<Subtitles className="h-3.5 w-3.5" />}
          label="Heavy customizers"
          count={savedViewCounts.subtitle}
          active={activeView === "subtitle"}
          onClick={() => onViewChange(activeView === "subtitle" ? null : "subtitle")}
        />
        <SavedViewRow
          icon={<Activity className="h-3.5 w-3.5" />}
          label="Dormant 30d"
          count={savedViewCounts.dormant}
          active={activeView === "dormant"}
          onClick={() => onViewChange(activeView === "dormant" ? null : "dormant")}
        />
      </FilterGroup>

      {availablePlatforms.length > 1 && (
        <FilterGroup label="Platform">
          {availablePlatforms.map((k) => (
            <FacetCheckbox
              key={k}
              label={platformKindLabel(k)}
              count={platformCounts.get(k) ?? 0}
              checked={platforms.has(k)}
              onToggle={() => togglePlatform(k)}
            />
          ))}
        </FilterGroup>
      )}

      <FilterGroup label="Override count">
        {OVERRIDE_RANGES.map((b) => (
          <FacetCheckbox
            key={b}
            label={b === "none" ? "none" : b}
            count={overrideRangeCounts.get(b) ?? 0}
            checked={overrideRange.has(b)}
            onToggle={() => toggleRange(b)}
          />
        ))}
      </FilterGroup>

      <FilterGroup label="Last seen">
        {RECENCY_BUCKETS.map((b) => (
          <FacetCheckbox
            key={b}
            label={b}
            count={recencyCounts.get(b) ?? 0}
            checked={recency.has(b)}
            onToggle={() => toggleRecency(b)}
          />
        ))}
      </FilterGroup>
    </aside>
  );
}

function FilterGroup({
  label,
  action,
  children,
}: {
  label: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div>
      <div className="text-muted-foreground/70 mb-2 flex items-center justify-between px-1 text-[10px] font-semibold tracking-[0.12em] uppercase">
        <span>{label}</span>
        {action}
      </div>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

function SavedViewRow({
  icon,
  label,
  count,
  active,
  tone,
  onClick,
}: {
  icon: ReactNode;
  label: string;
  count: number;
  active: boolean;
  tone?: "destructive";
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors",
        active
          ? "bg-surface-hover/70 text-foreground"
          : "text-muted-foreground hover:bg-surface-hover/40 hover:text-foreground",
      )}
    >
      <span
        className={cn(
          "shrink-0 transition-colors",
          tone === "destructive"
            ? "text-destructive/85"
            : active
              ? "text-foreground/90"
              : "text-muted-foreground/70 group-hover:text-foreground/80",
        )}
      >
        {icon}
      </span>
      <span className="flex-1 truncate">{label}</span>
      <span
        className={cn(
          "font-mono text-[10.5px] tabular-nums",
          active ? "text-foreground/80" : "text-muted-foreground/60",
        )}
      >
        {count}
      </span>
    </button>
  );
}

function FacetCheckbox({
  label,
  count,
  checked,
  onToggle,
}: {
  label: string;
  count: number;
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <label
      className={cn(
        "group flex cursor-pointer items-center justify-between rounded-md px-2 py-1.5 transition-colors",
        checked
          ? "bg-surface-hover/60 text-foreground"
          : "text-muted-foreground hover:bg-surface-hover/30 hover:text-foreground",
      )}
    >
      <span className="flex items-center gap-2">
        <input
          type="checkbox"
          className="border-border/80 bg-background/60 checked:bg-foreground checked:border-foreground checked:text-background h-3 w-3 cursor-pointer appearance-none rounded-[3px] border transition-colors checked:bg-[image:url('data:image/svg+xml;utf8,<svg%20xmlns=%22http://www.w3.org/2000/svg%22%20viewBox=%220%200%2012%2012%22%20fill=%22none%22%20stroke=%22%23141417%22%20stroke-width=%222.4%22%20stroke-linecap=%22round%22%20stroke-linejoin=%22round%22><polyline%20points=%222.5,6.5%205,9%209.5,3.5%22/></svg>')] checked:bg-[length:9px_9px] checked:bg-center checked:bg-no-repeat"
          checked={checked}
          onChange={onToggle}
        />
        <span>{label}</span>
      </span>
      <span
        className={cn(
          "font-mono text-[10.5px] tabular-nums",
          checked ? "text-foreground/80" : "text-muted-foreground/60",
        )}
      >
        {count}
      </span>
    </label>
  );
}

// ─────────────────────────────────────────────────────────────────────
// Device list — grouped, with override gauge + anomaly indicator
// ─────────────────────────────────────────────────────────────────────

interface DeviceGroupData {
  key: string;
  label: string;
  href?: string;
  devices: AdminDeviceSummary[];
  meta: { devices: number; profiles: number; overrides: number; lastUpdated?: string };
  anomalyCount: number;
}

function buildGroups(devices: AdminDeviceSummary[], groupBy: GroupBy): DeviceGroupData[] {
  type Entry = { key: string; label: string; href?: string; list: AdminDeviceSummary[] };
  const byKey = new Map<string, Entry>();

  const keyFor = (d: AdminDeviceSummary): { key: string; label: string; href?: string } => {
    if (groupBy === "user") {
      return {
        key: `u:${d.user_id}`,
        label: d.username || d.email || `User ${d.user_id}`,
        href: `/admin/users/${d.user_id}`,
      };
    }
    if (groupBy === "platform") {
      const k = classifyPlatform(d.device_platform);
      return { key: `p:${k}`, label: platformKindLabel(k) };
    }
    // activity
    const b = bucketRecency(d.last_updated);
    const labels: Record<Exclude<RecencyBucket, "all">, string> = {
      "<24h": "Last 24 hours",
      "<7d": "This week",
      "<30d": "This month",
      ">30d": "Older than 30 days",
    };
    return { key: `a:${b}`, label: labels[b] };
  };

  devices.forEach((d) => {
    const { key, label, href } = keyFor(d);
    const existing = byKey.get(key);
    if (existing) {
      existing.list.push(d);
    } else {
      byKey.set(key, { key, label, href, list: [d] });
    }
  });

  const all = Array.from(byKey.values()).map<DeviceGroupData>((entry) => {
    const list = entry.list;
    const overrides = list.reduce((s, d) => s + (d.override_count ?? 0), 0);
    const profiles = list.reduce((s, d) => s + (d.profiles?.length ?? 0), 0);
    const lastUpdated = list
      .map((d) => d.last_updated)
      .filter(Boolean)
      .sort()
      .pop();
    return {
      key: entry.key,
      label: entry.label,
      href: entry.href,
      devices: list,
      meta: { devices: list.length, profiles, overrides, lastUpdated },
      anomalyCount: 0, // filled in by caller (it has the anomaly set)
    };
  });

  // Stable, useful ordering per pivot
  if (groupBy === "user") {
    all.sort((a, b) => a.label.localeCompare(b.label));
  } else if (groupBy === "platform") {
    const order = new Map(PLATFORM_ORDER.map((k, i) => [platformKindLabel(k), i]));
    all.sort((a, b) => (order.get(a.label) ?? 99) - (order.get(b.label) ?? 99));
  } else {
    const order = new Map<string, number>([
      ["Last 24 hours", 0],
      ["This week", 1],
      ["This month", 2],
      ["Older than 30 days", 3],
    ]);
    all.sort((a, b) => (order.get(a.label) ?? 99) - (order.get(b.label) ?? 99));
  }
  return all;
}

function DeviceGroup({
  group,
  anomalies,
  currentUserId,
  currentDeviceId,
  currentProfileId,
  forceOpen,
}: {
  group: DeviceGroupData;
  anomalies: Map<string, DeviceAnomalyReason>;
  currentUserId: number;
  currentDeviceId: string;
  currentProfileId: string | null;
  forceOpen: boolean;
}) {
  const groupAnomalies = group.devices.filter((d) => anomalies.has(d.device_id)).length;
  const containsActive = group.devices.some(
    (d) => d.user_id === currentUserId && d.device_id === currentDeviceId,
  );
  // Default behavior: collapsed. We override to open when (a) the active
  // device lives in this group, or (b) the parent told us to force open
  // (search/filter active, or only one group). User clicks toggle this
  // local state; whenever the external triggers change we resync, so a
  // newly-applied filter expands everything as expected. We use the
  // "adjusting state on prop change" pattern (rather than useEffect) to
  // avoid cascading renders.
  const externalOpen = forceOpen || containsActive;
  const [open, setOpen] = useState(externalOpen);
  const [prevExternal, setPrevExternal] = useState(externalOpen);
  if (prevExternal !== externalOpen) {
    setPrevExternal(externalOpen);
    setOpen(externalOpen);
  }

  return (
    <section>
      <header className="bg-surface/60 border-border/40 sticky top-0 z-10 flex items-baseline justify-between gap-2 border-b px-3 py-2 backdrop-blur">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          aria-controls={`device-group-${group.key}`}
          className="text-foreground hover:text-foreground/80 group flex min-w-0 flex-1 items-center gap-1.5 text-left transition-colors"
        >
          <ChevronRight
            className={cn(
              "text-muted-foreground/70 group-hover:text-foreground h-3 w-3 shrink-0 transition-transform",
              open && "rotate-90",
            )}
          />
          <span className="truncate text-[12.5px] font-semibold">{group.label}</span>
        </button>
        <div className="flex shrink-0 items-center gap-2">
          {group.href && (
            <Link
              to={group.href}
              onClick={(e) => e.stopPropagation()}
              className="text-muted-foreground/70 hover:text-foreground inline-flex items-center transition-colors"
              aria-label={`Open ${group.label}`}
            >
              <ArrowUpRight className="h-3 w-3" />
            </Link>
          )}
          {groupAnomalies > 0 && (
            <span className="text-destructive bg-destructive/10 inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] tracking-[0.04em]">
              <AlertTriangle className="h-2.5 w-2.5" />
              {groupAnomalies}
            </span>
          )}
          <span className="text-muted-foreground/70 font-mono text-[10.5px] tabular-nums">
            {group.meta.devices}d · {group.meta.profiles}p · {group.meta.overrides}k
          </span>
        </div>
      </header>
      {open && (
        <ul id={`device-group-${group.key}`} className="space-y-0.5 px-1.5 pb-1.5">
          {group.devices.map((device) => (
            <li key={`${device.user_id}:${device.device_id}`}>
              <DeviceRow
                device={device}
                isAnomaly={anomalies.has(device.device_id)}
                active={device.user_id === currentUserId && device.device_id === currentDeviceId}
                activeProfileId={currentProfileId}
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function DeviceRow({
  device,
  isAnomaly,
  active,
  activeProfileId,
}: {
  device: AdminDeviceSummary;
  isAnomaly: boolean;
  active: boolean;
  activeProfileId: string | null;
}) {
  const kind = classifyPlatform(device.device_platform);
  const profileSuffix = activeProfileId ? `?profile=${encodeURIComponent(activeProfileId)}` : "";
  const href = `/admin/devices/${device.user_id}/${encodeURIComponent(device.device_id)}${profileSuffix}`;
  const recency = bucketRecency(device.last_updated);

  return (
    <Link
      to={href}
      className={cn(
        "group relative flex items-center gap-2.5 rounded-md px-2 py-1.5 transition-colors",
        active ? "bg-surface-hover/70" : "hover:bg-surface-hover/40",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "absolute top-1.5 bottom-1.5 left-0 w-[2px] rounded-r transition-opacity",
          active ? "bg-foreground opacity-100" : "opacity-0",
        )}
      />
      <PlatformTile kind={kind} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="text-foreground truncate text-[13px] font-medium">
            {device.device_name || "Unnamed device"}
          </span>
          {isAnomaly && (
            <span
              className="bg-destructive/90 ring-destructive/20 h-1.5 w-1.5 shrink-0 rounded-full ring-2"
              title="Anomaly: override pattern diverges from platform peers, or device is dormant"
              aria-label="anomaly"
            />
          )}
        </div>
        <div className="text-muted-foreground/80 mt-0.5 flex items-center gap-1.5 truncate text-[11px]">
          <span className="truncate">{platformLabel(device.device_platform)}</span>
          <span className="text-muted-foreground/40">·</span>
          <span
            className={cn(
              "tabular-nums",
              recency === "<24h" && "text-success/80",
              recency === ">30d" && "text-muted-foreground/60",
            )}
          >
            {formatRelative(device.last_updated)}
          </span>
        </div>
      </div>
      <OverrideGauge count={device.override_count ?? 0} active={active} />
    </Link>
  );
}

function OverrideGauge({ count, active }: { count: number; active: boolean }) {
  const SLOTS = 5;
  // Map count to slots: 0→0, 1-2→1-2, 3→3, 4-5→4, 6+→5 (full)
  const filled = count === 0 ? 0 : count >= 6 ? 5 : Math.min(count, 5);
  return (
    <div className="flex shrink-0 items-center gap-1.5">
      <span className="flex items-end gap-[2px]" aria-hidden>
        {Array.from({ length: SLOTS }).map((_, i) => (
          <span
            key={i}
            className={cn(
              "h-3 w-[3px] rounded-[1px] transition-colors",
              i < filled ? "bg-foreground/85" : "bg-border",
            )}
          />
        ))}
      </span>
      <span
        className={cn(
          "min-w-[14px] text-right font-mono text-[11px] tabular-nums",
          active
            ? "text-foreground"
            : count === 0
              ? "text-muted-foreground/50"
              : "text-muted-foreground/85",
        )}
      >
        {count}
      </span>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────
// Empty states
// ─────────────────────────────────────────────────────────────────────

function EmptyFleet({ hasDevices, onClear }: { hasDevices: boolean; onClear: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 px-4 py-14 text-center">
      <p className="text-foreground text-sm font-medium">
        {hasDevices ? "No devices match your filters" : "No devices seen yet"}
      </p>
      <p className="text-muted-foreground max-w-xs text-[12.5px] leading-relaxed">
        {hasDevices
          ? "Try a different search term or clear a few filters."
          : "Devices appear here after a client connects with a stable device id."}
      </p>
      {hasDevices && (
        <Button variant="outline" size="sm" onClick={onClear}>
          Clear filters
        </Button>
      )}
    </div>
  );
}

function EmptyDetail() {
  return (
    <div className="flex min-h-[420px] flex-col items-center justify-center gap-2 px-6 py-16 text-center">
      <h2 className="text-foreground text-base font-medium">Select a device</h2>
      <p className="text-muted-foreground max-w-sm text-[13px] leading-relaxed">
        Pick a device from the list to inspect its per-profile overrides, tune controls, or reset
        individual keys.
      </p>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────
// Detail panel — header with instrument readout + profile tabs
// ─────────────────────────────────────────────────────────────────────

function DeviceDetailPanel({
  userId,
  deviceId,
  initialProfileId,
  anomaly,
  defaultShowAllSettings,
}: {
  userId: number;
  deviceId: string;
  initialProfileId: string | null;
  /** Reason this device was flagged, or null if no anomaly. */
  anomaly: DeviceAnomalyReason | null;
  defaultShowAllSettings: boolean;
}) {
  const isAnomaly = anomaly !== null;
  const { data, isLoading } = useAdminDeviceDetail(userId, deviceId);
  const updateSetting = useUpdateAdminUserDeviceSetting();
  const deleteSetting = useDeleteAdminUserDeviceSetting();
  const deleteProfileOverrides = useDeleteAllAdminUserDeviceSettingsForDevice();
  const [settingToReset, setSettingToReset] = useState<AdminDeviceSetting | null>(null);
  const [profileToReset, setProfileToReset] = useState<{ id: string; name: string } | null>(null);
  // The editor target carries `isOverride` alongside the setting so the
  // editor (raw JSON or the subtitle-appearance panel) can decide whether
  // to surface a "Reset" affordance. The current setting always lives in
  // `setting`, even for synthesized default rows from the manifest.
  const [jsonEditor, setJsonEditor] = useState<{
    setting: AdminDeviceSetting;
    isOverride: boolean;
  } | null>(null);
  const [jsonValue, setJsonValue] = useState("");
  const closeJsonEditor = () => {
    setJsonEditor(null);
    setJsonValue("");
  };
  // When on, the profile tabs render every device-scoped setting from the
  // manifest — not just the ones currently overridden — so the admin can
  // create overrides on settings that haven't been touched yet.
  const [showAllSettings, setShowAllSettings] = useState(defaultShowAllSettings);
  const [prevDefaultShowAllSettings, setPrevDefaultShowAllSettings] =
    useState(defaultShowAllSettings);
  if (prevDefaultShowAllSettings !== defaultShowAllSettings) {
    setPrevDefaultShowAllSettings(defaultShowAllSettings);
    setShowAllSettings(defaultShowAllSettings);
  }

  const profileTabs = useMemo<DeviceProfileTabEntry[]>(() => {
    if (!data) return [];
    const grouped = new Map<string, DeviceProfileTabEntry>();
    for (const profile of data.profiles ?? []) {
      const profileId = profile.profile_id || UNKNOWN_PROFILE_ID;
      grouped.set(profileId, {
        profileId,
        profileName: profile.profile_name || profileId,
        settings: [],
      });
    }
    for (const setting of data.settings) {
      const profileId = setting.profile_id || UNKNOWN_PROFILE_ID;
      const existing = grouped.get(profileId);
      if (existing) {
        existing.settings.push(setting);
      } else {
        grouped.set(profileId, {
          profileId,
          profileName: setting.profile_name || profileId,
          settings: [setting],
        });
      }
    }
    return Array.from(grouped.values());
  }, [data]);

  if (isLoading) {
    return (
      <div className="space-y-4 p-5">
        <Skeleton className="h-16 w-full rounded-md" />
        <Skeleton className="h-32 w-full rounded-md" />
        <Skeleton className="h-32 w-full rounded-md" />
      </div>
    );
  }

  if (!data) {
    return (
      <div className="flex min-h-[320px] flex-col items-center justify-center gap-1 text-center">
        <div className="text-foreground text-sm font-medium">Device not found</div>
        <div className="text-muted-foreground max-w-xs text-xs">
          The device may have been removed, or its overrides may have been cleared.
        </div>
      </div>
    );
  }

  const kind = classifyPlatform(data.device_platform);
  const totalOverrides = data.override_count ?? data.settings.length;
  const forceAllSettings = totalOverrides === 0;
  const effectiveShowAllSettings = forceAllSettings || showAllSettings;
  const lastUpdate = data.settings
    .map((s) => s.updated_at)
    .filter(Boolean)
    .sort()
    .pop();

  return (
    <div className="flex h-full flex-col">
      <ConfirmDialog
        open={settingToReset !== null}
        onOpenChange={(open) => {
          if (!open) setSettingToReset(null);
        }}
        title="Reset this override?"
        description="The override will be removed and the device will fall back to the profile default."
        confirmLabel="Reset override"
        variant="destructive"
        onConfirm={() => {
          if (settingToReset) {
            deleteSetting.mutate({
              userId: data.user_id,
              profileId: settingToReset.profile_id,
              deviceId: settingToReset.device_id,
              key: settingToReset.key,
            });
          }
          setSettingToReset(null);
        }}
      />

      <ConfirmDialog
        open={profileToReset !== null}
        onOpenChange={(open) => {
          if (!open) setProfileToReset(null);
        }}
        title={profileToReset ? `Reset overrides for ${profileToReset.name}?` : "Reset profile"}
        description="Every override for this profile on this device will be cleared."
        confirmLabel="Reset all"
        variant="destructive"
        onConfirm={() => {
          if (profileToReset) {
            deleteProfileOverrides.mutate({
              userId: data.user_id,
              profileId: profileToReset.id,
              deviceId: data.device_id,
            });
          }
          setProfileToReset(null);
        }}
      />

      {/*
        Subtitle appearance gets the same rich panel that end users see in
        the player — preview, pill groups, color swatches — so admin
        overrides land in exactly the shape clients consume. Other JSON
        settings fall through to the raw textarea editor below.
      */}
      <AdminSubtitleAppearanceDialog
        setting={
          jsonEditor && jsonEditor.setting.key === "subtitle_appearance" ? jsonEditor.setting : null
        }
        isOverride={jsonEditor?.isOverride ?? false}
        onClose={closeJsonEditor}
        onSave={(setting, value) =>
          updateSetting.mutate({
            userId: data.user_id,
            profileId: setting.profile_id,
            deviceId: setting.device_id,
            key: setting.key,
            value,
          })
        }
        onReset={(setting) =>
          deleteSetting.mutate({
            userId: data.user_id,
            profileId: setting.profile_id,
            deviceId: setting.device_id,
            key: setting.key,
          })
        }
        saving={updateSetting.isPending}
      />

      <Dialog
        open={jsonEditor !== null && jsonEditor.setting.key !== "subtitle_appearance"}
        onOpenChange={(open) => {
          if (!open) closeJsonEditor();
        }}
      >
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="font-mono text-sm">
              {jsonEditor?.setting.key ?? "JSON"}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-3">
            <p className="text-muted-foreground text-[12.5px]">
              Edit the raw value. Invalid JSON is saved as-is and may cause clients to fall back to
              defaults.
            </p>
            <textarea
              spellCheck={false}
              className="border-border bg-background focus:border-foreground/40 min-h-[260px] w-full rounded-md border px-3 py-2 font-mono text-[13px] leading-relaxed transition-colors outline-none"
              value={jsonValue}
              onChange={(event) => setJsonValue(event.target.value)}
            />
            <div className="flex justify-end gap-2">
              <Button variant="outline" size="sm" onClick={closeJsonEditor}>
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={() => {
                  if (!jsonEditor) return;
                  const target = jsonEditor.setting;
                  updateSetting.mutate(
                    {
                      userId: data.user_id,
                      profileId: target.profile_id,
                      deviceId: target.device_id,
                      key: target.key,
                      value: jsonValue,
                    },
                    {
                      onSuccess: () => closeJsonEditor(),
                    },
                  );
                }}
              >
                Save override
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      <header className="border-border/60 flex flex-wrap items-start justify-between gap-4 border-b px-5 py-4 sm:px-6">
        <div className="flex min-w-0 items-start gap-3">
          <PlatformTile kind={kind} size="lg" />
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-foreground text-lg leading-tight font-semibold sm:text-xl">
                {data.device_name || "Unnamed device"}
              </h2>
              {isAnomaly && (
                <span className="text-destructive border-destructive/30 bg-destructive/10 inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10.5px] tracking-[0.04em] uppercase">
                  <AlertTriangle className="h-3 w-3" />
                  anomaly
                </span>
              )}
            </div>
            <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px]">
              <span className="text-foreground/80 font-mono">{data.device_id}</span>
              <span className="text-muted-foreground/40">·</span>
              <span>{platformLabel(data.device_platform)}</span>
              <span className="text-muted-foreground/40">·</span>
              <Link
                to={`/admin/users/${data.user_id}`}
                className="text-foreground/85 hover:text-foreground inline-flex items-center gap-0.5 font-medium transition-colors"
              >
                {data.username}
                <ArrowUpRight className="h-3 w-3" />
              </Link>
              <span className="text-muted-foreground/40">·</span>
              <span>updated {formatRelative(data.last_updated)}</span>
            </div>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <SettingsScopeControl
            showAllSettings={effectiveShowAllSettings}
            totalSettings={ALL_DEVICE_SETTING_KEYS.length}
            overrideCount={totalOverrides}
            disabled={profileTabs.length === 0}
            lockToAllSettings={forceAllSettings}
            onChange={setShowAllSettings}
          />
          <Button variant="outline" size="sm" asChild>
            <Link to={`/admin/users/${data.user_id}`}>
              Open user
              <ArrowUpRight className="h-3 w-3" />
            </Link>
          </Button>
        </div>
      </header>

      {/* instrument readout */}
      <div className="border-border/60 grid grid-cols-2 gap-x-6 gap-y-3 border-b px-5 py-4 sm:grid-cols-4 sm:px-6">
        <ReadoutCell
          label="Overrides"
          value={String(totalOverrides)}
          sub={`across ${profileTabs.length} ${profileTabs.length === 1 ? "profile" : "profiles"}`}
          tone="primary"
        />
        <ReadoutCell
          label="Profiles touched"
          value={
            profileTabs
              .slice(0, 2)
              .map((p) => p.profileName)
              .join(", ") || "—"
          }
          sub={
            profileTabs.length > 2
              ? `+ ${profileTabs.length - 2} more`
              : profileTabs.map((p) => `${p.settings.length}`).join(" / ")
          }
        />
        <ReadoutCell
          label="Status"
          value={isAnomaly ? "Anomaly" : "Normal"}
          sub={
            anomaly
              ? anomaly.summary
              : `aligned with ${platformKindLabel(kind).toLowerCase()} fleet`
          }
          subTitle={anomaly?.detail}
          tone={isAnomaly ? "destructive" : undefined}
        />
        <ReadoutCell
          label="Last update"
          value={formatRelative(lastUpdate || data.last_updated)}
          sub={lastUpdate ? lastUpdate.split("T")[0] : "—"}
        />
      </div>

      {/*
        flex-1 + min-h-0 lets the tabs component own its own scroll region.
        DeviceProfileTabs pins the tab strip + profile metadata bar at the
        top and scrolls only the override rows below — so the scrollbar
        appears next to the rows, not next to the profiles strip.
      */}
      <div className="flex min-h-0 flex-1 flex-col">
        {profileTabs.length === 0 ? (
          <div className="px-5 py-5 sm:px-6">
            <div className="border-border/60 rounded-md border border-dashed px-6 py-12 text-center">
              <p className="text-foreground text-sm font-medium">No profiles registered</p>
              <p className="text-muted-foreground mx-auto mt-1 max-w-sm text-xs leading-relaxed">
                The device exists, but it does not have enough profile context to create overrides.
              </p>
            </div>
          </div>
        ) : (
          <DeviceProfileTabs
            key={initialProfileId ?? "default"}
            profiles={profileTabs}
            initialProfileId={initialProfileId}
            showAllSettings={effectiveShowAllSettings}
            device={{
              userId: data.user_id,
              deviceId: data.device_id,
              deviceName: data.device_name,
              devicePlatform: data.device_platform,
            }}
            deviceStaleDays={anomaly?.staleDays ?? null}
            onResetProfile={(profileId, profileName) =>
              setProfileToReset({ id: profileId, name: profileName })
            }
            onEditJson={(setting, isOverride) => {
              setJsonEditor({ setting, isOverride });
              setJsonValue(setting.value);
            }}
            onResetSetting={(setting) => setSettingToReset(setting)}
            onChangeSetting={(setting, value) =>
              updateSetting.mutate({
                userId: data.user_id,
                profileId: setting.profile_id,
                deviceId: setting.device_id,
                key: setting.key,
                value,
              })
            }
            updatePending={updateSetting.isPending}
            resetPending={deleteProfileOverrides.isPending}
          />
        )}
      </div>
    </div>
  );
}

function ReadoutCell({
  label,
  value,
  sub,
  /**
   * Optional override for the sub-line tooltip. When omitted, the sub text
   * is used as the title — fine for short subs, but inadequate when the
   * sub is a one-line summary that has a longer detail explanation behind
   * it (e.g., the Status cell on an anomaly).
   */
  subTitle,
  tone,
}: {
  label: string;
  value: string;
  sub?: string;
  subTitle?: string;
  tone?: "primary" | "destructive";
}) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground/70 text-[10px] font-semibold tracking-[0.12em] uppercase">
        {label}
      </div>
      <div
        className={cn(
          "mt-1 truncate font-mono text-[14px]",
          tone === "primary"
            ? "text-foreground"
            : tone === "destructive"
              ? "text-destructive"
              : "text-foreground/90",
        )}
        title={value}
      >
        {value}
      </div>
      {sub && (
        <div
          className="text-muted-foreground/70 mt-0.5 truncate text-[11px]"
          title={subTitle ?? sub}
        >
          {sub}
        </div>
      )}
    </div>
  );
}
