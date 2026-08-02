import { useMemo, useState } from "react";
import { ChevronRight, Search } from "lucide-react";

import type { UserDevice } from "@/api/types";
import { Input } from "@/components/ui/input";
import {
  classifyPlatform,
  PlatformIcon,
  platformKindLabel,
} from "@/components/admin/deviceOverrides";
import { deviceRecencyGroup, type DeviceRecencyGroup } from "@/hooks/queries/devices";
import { cn } from "@/lib/utils";

const GROUP_TITLES: Record<DeviceRecencyGroup, string> = {
  current: "Using now",
  week: "This week",
  earlier: "Earlier",
};

const GROUP_ORDER: DeviceRecencyGroup[] = ["current", "week", "earlier"];

/**
 * A device is "dormant" when nobody has used it for three months and it carries
 * no settings of its own.
 *
 * Accounts accumulate these relentlessly — every browser profile, private
 * window, reinstall and test build registers an identity, and nothing prunes
 * them. A real account had 260 devices, over half of them one-off sessions that
 * had never changed a setting. Listing them alongside the living-room TV is
 * what turned this screen into sixteen screens of scrolling, so they collapse
 * behind a count until asked for.
 */
const DORMANT_AFTER_MS = 90 * 24 * 60 * 60 * 1000;

export function isDormantDevice(device: UserDevice, now: number): boolean {
  if (device.is_current_device || device.changed_count > 0) return false;
  const seen = Date.parse(device.last_seen_at);
  if (Number.isNaN(seen)) return true;
  return now - seen > DORMANT_AFTER_MS;
}

export interface DeviceListProps {
  devices: UserDevice[];
  selectedDeviceId: string | null;
  onSelect: (device: UserDevice) => void;
  search: string;
  onSearchChange: (value: string) => void;
  /** Group by person first. Only the household view sets this. */
  groupByProfile?: boolean;
  /**
   * Show only this profile's devices. Null means everyone. Only meaningful
   * alongside groupByProfile — a viewer looking at their own devices has just
   * the one profile, so a filter with a single option would be noise.
   */
  profileFilter?: string | null;
  onProfileFilterChange?: (profileId: string | null) => void;
  /** The viewer's own profile, so their chip leads the row. */
  ownProfileId?: string;
  /**
   * "Now" for relative-time labels. Passed in rather than read during render so
   * the component stays pure — and so a test can pin the clock.
   */
  now: number;
}

/**
 * The device list.
 *
 * Fixed-height rows carrying a name, when it was last used, and the one number
 * that matters — how many settings differ there. A device with nothing changed
 * shows a dash rather than a zero, so "which one did I change?" is answerable
 * by scanning rather than by opening each device in turn.
 */
export function DeviceList({
  devices,
  selectedDeviceId,
  onSelect,
  search,
  onSearchChange,
  groupByProfile = false,
  profileFilter = null,
  onProfileFilterChange,
  ownProfileId,
  now,
}: DeviceListProps) {
  const [showDormant, setShowDormant] = useState(false);
  // Counts come from the unfiltered list so a chip keeps showing how many
  // devices it would reveal, rather than collapsing to zero once another chip
  // is active.
  const profiles = useMemo(() => profileOptions(devices, ownProfileId), [devices, ownProfileId]);

  const matching = useMemo(() => {
    const query = search.trim().toLowerCase();
    return devices.filter((device) => {
      if (profileFilter && device.profile_id !== profileFilter) return false;
      if (!query) return true;
      const platform = platformKindLabel(classifyPlatform(device.device_platform));
      return (
        device.device_name.toLowerCase().includes(query) ||
        device.device_platform.toLowerCase().includes(query) ||
        platform.toLowerCase().includes(query) ||
        device.profile_name.toLowerCase().includes(query)
      );
    });
  }, [devices, search, profileFilter]);

  // Searching means you are looking for something specific, so a search spans
  // everything — hiding a dormant device from its own name would be a bug.
  const searching = search.trim().length > 0;
  const dormantCount = useMemo(
    () => (searching ? 0 : matching.filter((device) => isDormantDevice(device, now)).length),
    [matching, searching, now],
  );
  const filtered = useMemo(
    () =>
      searching || showDormant
        ? matching
        : matching.filter((device) => !isDormantDevice(device, now)),
    [matching, searching, showDormant, now],
  );

  const sections = useMemo(() => {
    if (groupByProfile && !profileFilter) {
      const byProfile = new Map<string, { title: string; devices: UserDevice[] }>();
      for (const device of filtered) {
        const existing = byProfile.get(device.profile_id);
        if (existing) {
          existing.devices.push(device);
        } else {
          byProfile.set(device.profile_id, {
            title: device.profile_name || "Other profile",
            devices: [device],
          });
        }
      }
      return [...byProfile.values()];
    }

    return GROUP_ORDER.map((group) => ({
      title: GROUP_TITLES[group],
      devices: filtered.filter((device) => deviceRecencyGroup(device, now) === group),
    })).filter((section) => section.devices.length > 0);
  }, [filtered, groupByProfile, profileFilter, now]);

  return (
    <div className="surface-panel rounded-[1.5rem] border-0 p-3 shadow-none">
      <div className="relative mb-1">
        <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
        <Input
          value={search}
          onChange={(event) => onSearchChange(event.target.value)}
          placeholder={`Search ${devices.length} ${devices.length === 1 ? "device" : "devices"}`}
          aria-label="Search devices"
          // text-base on mobile: iOS Safari zooms the viewport when a focused
          // input's font is under 16px, and the zoom does not undo itself.
          className="h-11 pl-8 text-base xl:h-9 xl:text-[13px]"
        />
      </div>

      {groupByProfile && profiles.length > 1 && onProfileFilterChange ? (
        // A wrapping row cost three lines and pushed the list off a phone
        // screen at eight profiles. Scrolling horizontally keeps it to one
        // line at any household size — the same trade the settings shell's own
        // mobile tab bar makes — and unwraps into a normal row from xl up.
        <div
          className="flex gap-1.5 overflow-x-auto px-0.5 pt-2 pb-1 [-ms-overflow-style:none] [scrollbar-width:none] xl:flex-wrap xl:gap-1 xl:overflow-visible [&::-webkit-scrollbar]:hidden"
          role="group"
          aria-label="Filter by profile"
        >
          <ProfileChip
            label="Everyone"
            count={devices.length}
            active={profileFilter === null}
            onClick={() => onProfileFilterChange(null)}
          />
          {profiles.map((profile) => (
            <ProfileChip
              key={profile.id}
              label={profile.name}
              count={profile.count}
              active={profileFilter === profile.id}
              onClick={() =>
                onProfileFilterChange(profileFilter === profile.id ? null : profile.id)
              }
            />
          ))}
        </div>
      ) : null}

      {sections.length === 0 ? (
        <p className="text-muted-foreground px-2 py-6 text-center text-[13px]">
          {devices.length === 0
            ? "No devices yet."
            : searching
              ? "No devices match that search."
              : "Nothing here. Try showing unused devices."}
        </p>
      ) : null}

      {/* Capped and scrollable rather than growing without limit: an account
          with a few hundred devices produced a thirteen-thousand-pixel page,
          so the settings never came into view. The list keeps its own scroll
          and the page stays roughly one screen. */}
      <div className="overlay-scroll -mr-1.5 max-h-[min(60vh,520px)] overflow-y-auto overscroll-contain pr-1.5 xl:max-h-[calc(100vh-16rem)]">
        {sections.map((section) => (
          <section key={section.title}>
            <h3 className="text-muted-foreground bg-surface sticky top-0 z-10 px-2 pt-3 pb-1 text-[10.5px] font-semibold tracking-[0.09em] uppercase">
              {section.title}
            </h3>
            <ul>
              {section.devices.map((device) => (
                <li key={`${device.profile_id}:${device.device_id}`}>
                  <DeviceRow
                    device={device}
                    selected={device.device_id === selectedDeviceId}
                    onSelect={onSelect}
                    now={now}
                  />
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>

      {/* The long tail is opt-in, and says how big it is so nobody wonders
          whether a device is missing. */}
      {dormantCount > 0 ? (
        <button
          type="button"
          onClick={() => setShowDormant((shown) => !shown)}
          className="text-muted-foreground hover:text-foreground mt-1 inline-flex min-h-11 w-full items-center justify-center gap-1 rounded-xl text-[13px] transition-colors xl:min-h-9"
        >
          {showDormant
            ? "Hide unused devices"
            : `Show ${dormantCount} unused ${dormantCount === 1 ? "device" : "devices"}`}
        </button>
      ) : null}
    </div>
  );
}

interface ProfileOption {
  id: string;
  name: string;
  count: number;
}

/**
 * One entry per profile that owns at least one device.
 *
 * The viewer's own profile comes first — at eight profiles the arrival order
 * put the person actually using the screen last — and the rest are alphabetical
 * so a chip stays where someone last saw it, rather than moving whenever a
 * device is used.
 */
function profileOptions(devices: UserDevice[], ownProfileId?: string): ProfileOption[] {
  const byId = new Map<string, ProfileOption>();
  for (const device of devices) {
    const existing = byId.get(device.profile_id);
    if (existing) {
      existing.count += 1;
    } else {
      byId.set(device.profile_id, {
        id: device.profile_id,
        name: device.profile_name || "Other profile",
        count: 1,
      });
    }
  }
  return [...byId.values()].sort((a, b) => {
    if (a.id === ownProfileId) return -1;
    if (b.id === ownProfileId) return 1;
    return a.name.localeCompare(b.name);
  });
}

function ProfileChip({
  label,
  count,
  active,
  onClick,
}: {
  label: string;
  count: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      // The visible label and the count are separate elements, so without this
      // a screen reader announces them run together as "Everyone3".
      aria-label={`${label}, ${count} ${count === 1 ? "device" : "devices"}`}
      className={cn(
        "inline-flex min-h-11 shrink-0 items-center gap-1.5 rounded-full border px-3.5 text-[13px] font-medium whitespace-nowrap transition-colors xl:min-h-0 xl:px-2.5 xl:py-1 xl:text-[12px]",
        active
          ? "border-foreground/25 bg-surface-raised text-foreground"
          : "border-border/60 text-muted-foreground hover:bg-surface-hover hover:text-foreground",
      )}
    >
      {label}
      <span
        className={cn("text-[11px]", active ? "text-muted-foreground" : "text-muted-foreground/70")}
      >
        {count}
      </span>
    </button>
  );
}

function DeviceRow({
  device,
  selected,
  onSelect,
  now,
}: {
  device: UserDevice;
  selected: boolean;
  onSelect: (device: UserDevice) => void;
  now: number;
}) {
  const kind = classifyPlatform(device.device_platform);
  return (
    <button
      type="button"
      aria-current={selected ? "true" : undefined}
      onClick={() => onSelect(device)}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-xl px-2 py-2.5 text-left transition-colors",
        // Rows are a tap target on a phone and a selection on a desktop, so
        // they carry a comfortable minimum height only where that matters.
        "min-h-14 xl:min-h-0",
        selected ? "bg-surface-raised" : "hover:bg-surface-hover active:bg-surface-hover",
      )}
    >
      <span className="bg-secondary text-muted-foreground flex h-9 w-9 shrink-0 items-center justify-center rounded-lg xl:h-7 xl:w-7">
        <PlatformIcon kind={kind} className="h-4 w-4 xl:h-3.5 xl:w-3.5" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium xl:text-[13px]">
          {device.device_name || "Unknown device"}
        </span>
        <span className="text-muted-foreground block truncate text-[12.5px] xl:text-[11.5px]">
          {device.is_current_device
            ? "This is the device you're on"
            : lastSeenLabel(device.last_seen_at, now)}
        </span>
      </span>
      {device.is_current_device ? (
        <span
          className="bg-success h-1.5 w-1.5 shrink-0 rounded-full"
          aria-label="You're on this device"
        />
      ) : null}
      <ChangedCount count={device.changed_count} />
      {/* Below xl a row navigates to another screen; say so. */}
      <ChevronRight className="text-muted-foreground/50 h-4 w-4 shrink-0 xl:hidden" />
    </button>
  );
}

function ChangedCount({ count }: { count: number }) {
  if (count <= 0) {
    return (
      <span className="text-muted-foreground/50 shrink-0 text-[11px]" aria-label="Nothing changed">
        —
      </span>
    );
  }
  return (
    <span
      className="shrink-0 rounded-full border border-amber-500/30 bg-amber-500/10 px-1.5 text-[11px] font-semibold text-amber-300"
      aria-label={`${count} ${count === 1 ? "setting" : "settings"} changed here`}
    >
      {count}
    </span>
  );
}

/** Plain relative time; the exact timestamp is not what anyone is asking. */
export function lastSeenLabel(iso: string, now: number): string {
  const seen = Date.parse(iso);
  if (Number.isNaN(seen)) return "Never used";
  const minutes = Math.max(0, Math.round((now - seen) / 60000));
  if (minutes < 60) return "Less than an hour ago";
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} ${hours === 1 ? "hour" : "hours"} ago`;
  const days = Math.round(hours / 24);
  if (days === 1) return "Yesterday";
  if (days < 30) return `${days} days ago`;
  const months = Math.round(days / 30);
  if (months < 12) return `${months} ${months === 1 ? "month" : "months"} ago`;
  const years = Math.round(months / 12);
  return `${years} ${years === 1 ? "year" : "years"} ago`;
}
