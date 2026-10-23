import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import type { UserDevice } from "@/api/types";

import { deviceKeys, settingsKeys } from "./keys";

/**
 * The signed-in viewer's own device registry.
 *
 * `household` is opt-in and only succeeds for the household parent — the server
 * answers 403 otherwise. It defaults off so the ordinary screen cannot show the
 * family's devices by forgetting to ask for less.
 */
export function useMyDevices(options?: { household?: boolean; enabled?: boolean }) {
  const household = options?.household ?? false;

  return useQuery({
    queryKey: deviceKeys.list(household ? "household" : "own"),
    queryFn: async () => {
      const devices: UserDevice[] = [];
      let cursor: string | undefined;
      do {
        const result = await v2("GET /api/v2/devices", {
          query: { scope: household ? "household" : "profile", limit: 200, cursor },
        });
        devices.push(...result.items);
        cursor = result.page?.next_cursor || undefined;
      } while (cursor);
      return devices;
    },
    enabled: options?.enabled ?? true,
    staleTime: 30 * 1000,
  });
}

/** Both mutations move settings, so both invalidate the value caches too. */
function useDeviceMutation(forget: boolean) {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (device: DeviceTarget) => {
      const input = {
        path: { device_id: device.deviceId },
        query: { profile_id: device.profileId },
      };
      return forget
        ? v2("DELETE /api/v2/devices/{device_id}", input)
        : v2("DELETE /api/v2/devices/{device_id}/settings", input);
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: deviceKeys.all });
      void qc.invalidateQueries({ queryKey: [...settingsKeys.all, "values"] });
    },
  });
}

export interface DeviceTarget {
  deviceId: string;
  /** Set only when acting for another profile, as the household parent. */
  profileId?: string;
}

/**
 * Give a device a name of the profile's choosing, or clear it with `null` so the
 * device shows the name it reports again. Only the device list reads the name.
 */
export function useRenameDevice() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ device, name }: RenameDeviceInput) =>
      v2("PATCH /api/v2/devices/{device_id}", {
        path: { device_id: device.deviceId },
        query: { profile_id: device.profileId },
        body: { custom_name: name },
      }),
    // The saved entry goes into every cached list first, so a refetch that
    // fails still leaves the new name on screen when the editor closes.
    onSuccess: (saved) => {
      qc.setQueriesData<UserDevice[]>({ queryKey: deviceKeys.all }, (devices) =>
        devices?.map((row) =>
          row.device_id === saved.device_id && row.profile_id === saved.profile_id
            ? { ...row, custom_name: saved.custom_name }
            : row,
        ),
      );
    },
    // Awaited, so a successful refresh lands before the editor closes.
    onSettled: () => qc.invalidateQueries({ queryKey: deviceKeys.all }),
  });
}

export interface RenameDeviceInput {
  device: DeviceTarget;
  /** The new name, or `null` to show the reported name again. */
  name: string | null;
}

/** Remove the device's settings and drop it from the registry. */
export function useForgetDevice() {
  return useDeviceMutation(true);
}

/** Return a device to the profile's own values without forgetting it. */
export function useClearDeviceSettings() {
  return useDeviceMutation(false);
}

/**
 * Devices grouped the way the list reads them: by how recently they were used,
 * because that is how someone identifies a device they own.
 */
export type DeviceRecencyGroup = "current" | "week" | "earlier";

export function deviceRecencyGroup(device: UserDevice, now: number): DeviceRecencyGroup {
  if (device.is_current_device) return "current";
  const seen = Date.parse(device.last_seen_at);
  if (Number.isNaN(seen)) return "earlier";
  return now - seen < 7 * 24 * 60 * 60 * 1000 ? "week" : "earlier";
}

/** What to call a device: the name its profile gave it, else the one it reports. */
export function deviceDisplayName(device: Pick<UserDevice, "custom_name" | "device_name">): string {
  return device.custom_name || device.device_name || "Unknown device";
}
