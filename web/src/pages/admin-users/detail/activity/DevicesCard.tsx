import { useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight } from "lucide-react";

import type { AdminUserDeviceRow } from "@/api/v2/adminUserActivity";
import { PlatformTile, classifyPlatform, platformLabel } from "@/components/admin/deviceOverrides";
import { Button } from "@/components/ui/button";
import { useAdminUserDevices } from "@/hooks/queries/admin/userActivity";

import { formatLastSeen } from "../format";
import { deviceLevelId } from "../preferences/levels";
import { DetailCard, ListRow } from "../ui";
import { userDetailTabSearch } from "../userDetailTabs";

const RECENT_DAYS = 30;

function lastActivity(device: AdminUserDeviceRow): string | null {
  return device.last_seen_at ?? device.last_updated;
}

/** Whether the device reported or saved something in the last 30 days. */
function seenRecently(device: AdminUserDeviceRow, now = Date.now()): boolean {
  const at = lastActivity(device);
  if (!at) return false;
  const time = Date.parse(at);
  return Number.isFinite(time) && now - time <= RECENT_DAYS * 86_400_000;
}

/** Preferences opened on this device for the first profile that saved settings on it. */
function preferencesSearch(device: AdminUserDeviceRow): string {
  const profile = device.profiles.find((row) => row.override_count > 0) ?? device.profiles[0];
  return userDetailTabSearch(
    "preferences",
    profile ? { level: deviceLevelId(profile.profile_id, device.device_id) } : undefined,
  );
}

export function DevicesCard({ userId }: { userId: number }) {
  const devices = useAdminUserDevices(userId);
  const [showAll, setShowAll] = useState(false);
  const all = devices.data ?? [];
  const recent = all.filter((device) => seenRecently(device));
  const hasOlder = recent.length < all.length;
  const shown = showAll ? all : recent;

  return (
    <DetailCard
      title="Devices"
      description={`Seen in the last ${RECENT_DAYS} days`}
      actions={
        <>
          {hasOlder && (
            <Button variant="ghost" size="sm" onClick={() => setShowAll((value) => !value)}>
              {showAll ? "Show recent" : `Show all ${all.length}`}
            </Button>
          )}
          <Button variant="ghost" size="sm" asChild>
            <Link to="/admin/devices">
              Devices
              <ArrowUpRight className="h-3 w-3" />
            </Link>
          </Button>
        </>
      }
    >
      {devices.isError ? (
        <div role="alert" className="flex items-center gap-3 px-5 py-4 text-sm">
          <span className="text-destructive">Couldn&apos;t load devices.</span>
          <Button variant="outline" size="sm" onClick={() => void devices.refetch()}>
            Retry
          </Button>
        </div>
      ) : devices.isLoading ? (
        <p className="text-muted-foreground px-5 py-6 text-center text-sm">Loading devices...</p>
      ) : shown.length === 0 ? (
        <p className="text-muted-foreground px-5 py-6 text-center text-sm">
          No devices seen in the last {RECENT_DAYS} days.
        </p>
      ) : (
        <div>
          {shown.map((device) => (
            <ListRow
              key={device.device_id}
              leading={<PlatformTile kind={classifyPlatform(device.device_platform)} />}
              title={device.device_name || "Unnamed device"}
              meta={
                <>
                  {platformLabel(device.device_platform)}
                  {device.override_count > 0 ? (
                    <>
                      {" · "}
                      <Link
                        to={{ search: preferencesSearch(device) }}
                        className="hover:text-foreground underline-offset-2 hover:underline"
                      >
                        {device.override_count}{" "}
                        {device.override_count === 1 ? "saved setting" : "saved settings"}
                      </Link>
                    </>
                  ) : null}
                </>
              }
              trailing={formatLastSeen(lastActivity(device))}
            />
          ))}
        </div>
      )}
    </DetailCard>
  );
}
