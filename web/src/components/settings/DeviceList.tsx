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
  now,
}: DeviceListProps) {
  const filtered = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) return devices;
    return devices.filter((device) => {
      const platform = platformKindLabel(classifyPlatform(device.device_platform));
      return (
        device.device_name.toLowerCase().includes(query) ||
        device.device_platform.toLowerCase().includes(query) ||
        platform.toLowerCase().includes(query) ||
        device.profile_name.toLowerCase().includes(query)
      );
    });
  }, [devices, search]);

  const sections = useMemo(() => {
    if (groupByProfile) {
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
  }, [filtered, groupByProfile, now]);

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
