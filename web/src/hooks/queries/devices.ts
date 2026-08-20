import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { UserDevice, UserDeviceListResponse } from "@/api/types";

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
      const result = await api<UserDeviceListResponse>(
        `/devices${household ? "?scope=household" : ""}`,
      );
      return result.devices ?? [];
    },
    enabled: options?.enabled ?? true,
    staleTime: 30 * 1000,
  });
}

/** Both mutations move settings, so both invalidate the value caches too. */
function useDeviceMutation(path: (device: DeviceTarget) => string) {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (device: DeviceTarget) => api(path(device), { method: "DELETE" }),
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

function targetQuery(device: DeviceTarget): string {
  return device.profileId ? `?profile_id=${encodeURIComponent(device.profileId)}` : "";
}

/** Remove the device's settings and drop it from the registry. */
export function useForgetDevice() {
  return useDeviceMutation(
    (device) => `/devices/${encodeURIComponent(device.deviceId)}${targetQuery(device)}`,
  );
}

/** Return a device to the profile's own values without forgetting it. */
export function useClearDeviceSettings() {
  return useDeviceMutation(
    (device) => `/devices/${encodeURIComponent(device.deviceId)}/settings${targetQuery(device)}`,
  );
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
