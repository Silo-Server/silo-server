import { useMemo } from "react";
import { Search } from "lucide-react";

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
  now,
}: DeviceListProps) {
  // Counts come from the unfiltered list so a chip keeps showing how many
  // devices it would reveal, rather than collapsing to zero once another chip
  // is active.
  const profiles = useMemo(() => profileOptions(devices), [devices]);

  const filtered = useMemo(() => {
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
          className="h-9 pl-8 text-[13px]"
        />
      </div>

      {groupByProfile && profiles.length > 1 && onProfileFilterChange ? (
        <div
          className="flex flex-wrap gap-1 px-0.5 pt-2 pb-1"
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
          {devices.length === 0 ? "No devices yet." : "No devices match that search."}
        </p>
      ) : null}

      {sections.map((section) => (
        <section key={section.title}>
          <h3 className="text-muted-foreground px-2 pt-3 pb-1 text-[10.5px] font-semibold tracking-[0.09em] uppercase">
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
  );
}

interface ProfileOption {
  id: string;
  name: string;
  count: number;
}

/** One entry per profile that owns at least one device, in first-seen order. */
function profileOptions(devices: UserDevice[]): ProfileOption[] {
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
  return [...byId.values()];
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
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[12px] font-medium transition-colors",
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
        "flex w-full items-center gap-2.5 rounded-xl px-2 py-2 text-left transition-colors",
        selected ? "bg-surface-raised" : "hover:bg-surface-hover",
      )}
    >
      <span className="bg-secondary text-muted-foreground flex h-7 w-7 shrink-0 items-center justify-center rounded-lg">
        <PlatformIcon kind={kind} className="h-3.5 w-3.5" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] font-medium">
          {device.device_name || "Unknown device"}
        </span>
        <span className="text-muted-foreground block truncate text-[11.5px]">
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
