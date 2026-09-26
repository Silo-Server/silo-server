import {
  listAdminSettingValues,
  setAdminSettingValue,
  deleteAdminSettingValue,
  captureAdminSettingAuthority,
} from "@/api/v2/adminAccountSettings";
import { listAdminDevices, getAdminDevice } from "@/api/v2/adminDevices";
import { V2ProblemError } from "@/api/v2/request";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { captureProfileRequestContext } from "@/api/client";
import type { AdminUser, CreateUserRequest, UpdateUserRequest } from "@/api/types";
import {
  adminUserScope,
  captureAdminUserAuthority,
  listAdminUsers,
  getAdminUser,
  createAdminUser,
  updateAdminUser,
  deleteAdminUser,
  impersonateAdminUser,
  issueAdminPasswordReset,
  getAdminUserCapabilities,
  type AdminUserEditor,
} from "@/api/v2/adminUsers";
export { adminUserFromV2 } from "@/api/v2/adminUsers";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { adminKeys } from "../keys";
import { useAdminUserProfiles } from "./history";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

// ─────────────────────────────────────────────────────────────────────
// Canonical admin settings surface.
//
// The string-registry routes (/admin/users/{id}/settings*,
// /device-settings*) were removed server-side in the settings-contract
// cutover; the replacement is the canonical values API:
//   GET    /admin/users/{id}/settings/values
//   PUT    /admin/users/{id}/settings/values/{key}?scope=…
//   DELETE /admin/users/{id}/settings/values/{key}?scope=…
// Values there are typed JSON at an explicit scope. The admin controls still
// edit strings (the registry-era shape the components speak), so these hooks
// are the seam: display stringifies, save re-types through the generated
// contract so requests carry what the server's validation expects.
// ─────────────────────────────────────────────────────────────────────

/** The scopes the canonical settings API stores values at. */
export type AdminSettingScope =
  | "account"
  | "profile"
  | "profile_client"
  | "profile_device"
  | "profile_library"
  | "profile_series";

export type AdminSettingClientFamily = "tv" | "mobile" | "tablet" | "desktop" | "web";

/** Locates the exact row a mutation addresses. */
export interface AdminSettingIdentity {
  scope: AdminSettingScope;
  profileId?: string;
  clientFamily?: AdminSettingClientFamily;
  deviceId?: string;
  libraryId?: number;
  seriesId?: string;
}

/** One non-device setting row, string-valued for the admin controls. */
export interface AdminUserSettingEntry {
  key: string;
  scope: AdminSettingScope;
  profile_id?: string;
  client_family?: AdminSettingClientFamily;
  library_id?: number;
  series_id?: string;
  value: string;
  updated_at?: string;
}

export interface AdminDeviceSetting {
  user_id: number;
  profile_id: string;
  profile_name?: string;
  device_id: string;
  device_name: string;
  device_platform: string;
  key: string;
  value: string;
  updated_at: string;
}

/** Renders a typed JSON value in the string form the admin controls edit. */
export function settingValueToString(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "boolean" || typeof value === "number") return String(value);
  return JSON.stringify(value);
}

/**
 * Re-types an edited string through the generated contract. Throwing (inside
 * a mutationFn) surfaces as the mutation's error toast, which beats sending a
 * value the server will refuse anyway.
 */
export function settingValueFromString(key: string, raw: string): unknown {
  const definition = SETTING_DEFINITIONS[key as SettingKey];
  const trimmed = raw.trim();
  if (definition?.nullable && trimmed === "") return null;
  switch (definition?.type) {
    case "boolean":
      return trimmed === "true";
    case "integer":
    case "number": {
      const parsed = Number(trimmed);
      if (!Number.isFinite(parsed)) {
        throw new Error(`${key}: "${raw}" is not a number`);
      }
      return definition.type === "integer" ? Math.trunc(parsed) : parsed;
    }
    case "object":
      try {
        return JSON.parse(trimmed) as unknown;
      } catch {
        throw new Error(`${key}: the value must be valid JSON`);
      }
    default:
      // Strings, enums and language tags travel as themselves; so does a key
      // this build's contract does not know — the server answers with its own
      // authoritative error.
      return raw;
  }
}

/** Every explicit canonical value the target user has stored, all scopes. */
function useAdminUserSettingValues(userId: number) {
  const authority = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminUsersKey(adminUserScope(authority)), "settings", userId],
    queryFn: async () => {
      const response = await listAdminSettingValues(
        userId,
        authority ?? captureAdminSettingAuthority(),
      );
      return response.values;
    },
    enabled: authority !== null,
    retry: false,
    staleTime: ADMIN_STALE_TIME,
  });
}

function invalidateAdminDeviceCaches(
  queryClient: ReturnType<typeof useQueryClient>,
  userId: number,
  authority: ReturnType<typeof captureAdminSettingAuthority>,
) {
  // Device overrides derive from the canonical values list, which lives under
  // the userSettings key.
  queryClient.invalidateQueries({
    queryKey: [...adminUsersKey(adminUserScope(authority)), "settings", userId],
  });
  queryClient.invalidateQueries({ queryKey: adminKeys.devices() });
}

export const adminUsersKey = (scope = adminUserScope()) => [...adminKeys.users(), scope];
export async function fetchAllAdminUsers(signal?: AbortSignal): Promise<AdminUser[]> {
  return listAdminUsers(captureAdminUserAuthority(), signal);
}
export function useAdminUsers() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: adminUsersKey(adminUserScope(context)),
    queryFn: ({ signal }) => listAdminUsers(context ?? captureAdminUserAuthority(), signal),
    enabled: context !== null,
    retry: false,
    staleTime: ADMIN_STALE_TIME,
  });
}
/** Whether the signed-in account is the server Owner, read from the account list. */
export function useViewerIsOwner(viewerId: number | undefined): boolean {
  const { data } = useAdminUsers();
  return data?.some((u) => u.id === viewerId && u.is_owner) ?? false;
}
export function useAdminUser(id: number) {
  const context = captureProfileRequestContext();
  const query = useQuery({
    queryKey: [...adminUsersKey(adminUserScope(context)), "detail", id],
    queryFn: () => getAdminUser(id, context ?? captureAdminUserAuthority()),
    enabled: context !== null && Number.isSafeInteger(id) && id > 0,
    retry: false,
    staleTime: ADMIN_STALE_TIME,
  });
  return { ...query, editor: query.data, data: query.data?.user };
}
export function useAdminUserCapabilities() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminUsersKey(adminUserScope(context)), "capabilities"],
    queryFn: () => getAdminUserCapabilities(context ?? captureAdminUserAuthority()),
    enabled: context !== null,
    retry: false,
    staleTime: ADMIN_STALE_TIME,
  });
}
export function useCreateUser() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({
      body,
      profileContext,
    }: {
      body: CreateUserRequest;
      profileContext: ReturnType<typeof captureAdminUserAuthority>;
    }) => createAdminUser(body, profileContext),
    retry: false,
    gcTime: 0,
    onSuccess: (_data, { profileContext }) => {
      void client.invalidateQueries({ queryKey: adminUsersKey(adminUserScope(profileContext)) });
    },
  });
}
export function useUpdateUser() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ editor, body }: { editor: AdminUserEditor; body: UpdateUserRequest }) =>
      updateAdminUser(editor, body),
    retry: false,
    gcTime: 0,
    onSuccess: (_data, { editor }) => {
      const scope = adminUserScope(editor.profileContext);
      void client.invalidateQueries({ queryKey: adminUsersKey(scope) });
      void client.invalidateQueries({ queryKey: [...adminKeys.accessGroups(), scope] });
    },
  });
}
export function useDeleteUser() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: deleteAdminUser,
    retry: false,
    onSuccess: (_data, editor) => {
      const scope = adminUserScope(editor.profileContext);
      void client.invalidateQueries({ queryKey: adminUsersKey(scope) });
      void client.invalidateQueries({ queryKey: [...adminKeys.accessGroups(), scope] });
    },
  });
}

/**
 * The target user's non-device settings: everything the settings tab shows.
 * Device overrides live in useAdminUserDeviceSettings, matching the old
 * two-endpoint split the UI is built around.
 */
export function useAdminUserSettings(userId: number) {
  const values = useAdminUserSettingValues(userId);
  const data = useMemo<AdminUserSettingEntry[]>(
    () =>
      (values.data ?? [])
        .filter((row) => row.scope !== "profile_device")
        .map((row) => ({
          key: row.key,
          scope: row.scope,
          profile_id: row.profile_id,
          client_family: row.client_family,
          library_id: row.library_id,
          series_id: row.series_id,
          value: settingValueToString(row.value),
          updated_at: row.updated_at,
        })),
    [values.data],
  );
  return { data, isLoading: values.isLoading, isError: values.isError };
}

export function useUpdateAdminUserSetting() {
  const queryClient = useQueryClient();
  const authority = captureAdminSettingAuthority();
  return useMutation({
    retry: false,
    mutationFn: async ({
      userId,
      key,
      identity,
      value,
    }: {
      userId: number;
      key: string;
      identity: AdminSettingIdentity;
      value: string;
    }) =>
      setAdminSettingValue(userId, key, identity, settingValueFromString(key, value), authority),
    onSuccess: (_data, variables) => {
      toast.success("User setting updated");
      queryClient.invalidateQueries({
        queryKey: [...adminUsersKey(adminUserScope(authority)), "settings", variables.userId],
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save setting");
    },
  });
}

export function useDeleteAdminUserSetting() {
  const queryClient = useQueryClient();
  const authority = captureAdminSettingAuthority();
  return useMutation({
    retry: false,
    mutationFn: async ({
      userId,
      key,
      identity,
    }: {
      userId: number;
      key: string;
      identity: AdminSettingIdentity;
    }) => {
      if (key === SETTING_KEYS.NAV_SHORTCUTS) {
        // Shortcut history is revisioned and may not be erased. Its reset is
        // the atomic empty document accepted by the canonical admin endpoint.
        await setAdminSettingValue(userId, key, identity, { items: [] }, authority);
        return;
      }
      await deleteAdminSettingValue(userId, key, identity, authority);
    },
    onSuccess: (_data, variables) => {
      toast.success("User setting reset");
      queryClient.invalidateQueries({
        queryKey: [...adminUsersKey(adminUserScope(authority)), "settings", variables.userId],
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to reset setting");
    },
  });
}

/**
 * The target user's device overrides, enriched with device and profile names
 * the way the removed /device-settings endpoint reported them: the canonical
 * rows carry only ids, so names come from the devices list and the profile
 * list and degrade to the raw id when a registration is gone.
 */
export function useAdminUserDeviceSettings(userId: number) {
  const values = useAdminUserSettingValues(userId);
  const devices = useAdminDevices();
  const profiles = useAdminUserProfiles(userId);

  const data = useMemo<AdminDeviceSetting[]>(() => {
    const deviceMeta = new Map<string, { name: string; platform: string }>();
    for (const device of devices.data ?? []) {
      if (device.user_id === userId) {
        deviceMeta.set(device.device_id, {
          name: device.device_name,
          platform: device.device_platform,
        });
      }
    }
    const profileNames = new Map(
      (profiles.data ?? []).map((profile) => [profile.id, profile.name]),
    );
    return (values.data ?? [])
      .filter((row) => row.scope === "profile_device")
      .map((row) => {
        const meta = row.device_id ? deviceMeta.get(row.device_id) : undefined;
        return {
          user_id: userId,
          profile_id: row.profile_id ?? "",
          profile_name: row.profile_id ? profileNames.get(row.profile_id) : undefined,
          device_id: row.device_id ?? "",
          device_name: meta?.name ?? "",
          device_platform: meta?.platform ?? "",
          key: row.key,
          value: settingValueToString(row.value),
          updated_at: row.updated_at ?? "",
        };
      });
  }, [values.data, devices.data, profiles.data, userId]);

  return { data, isLoading: values.isLoading, isError: values.isError };
}

/**
 * One device's overrides, canonically.
 *
 * The device detail compatibility array (including its v2 projection) still
 * reports rows straight out of the legacy user_device_settings table, which
 * the settings-contract migration folded into user_setting_values and which
 * nothing canonical writes to any more. Reading the values API instead means
 * the panel shows both storage generations — the migrated legacy rows and
 * everything written since — rather than a table that only ever shrinks.
 */
export function useAdminDeviceOverrides(userId: number, deviceId: string) {
  const settings = useAdminUserDeviceSettings(userId);
  const data = useMemo(
    () => settings.data.filter((setting) => setting.device_id === deviceId),
    [settings.data, deviceId],
  );
  return { data, isLoading: settings.isLoading, isError: settings.isError };
}

export function useUpdateAdminUserDeviceSetting() {
  const queryClient = useQueryClient();
  const authority = captureAdminSettingAuthority();
  return useMutation({
    retry: false,
    mutationFn: async ({
      userId,
      profileId,
      deviceId,
      key,
      value,
    }: {
      userId: number;
      profileId: string;
      deviceId: string;
      key: string;
      value: string;
    }) =>
      setAdminSettingValue(
        userId,
        key,
        { scope: "profile_device", profileId, deviceId },
        settingValueFromString(key, value),
        authority,
      ),
    onSuccess: (_data, variables) => {
      toast.success("Device override updated");
      invalidateAdminDeviceCaches(queryClient, variables.userId, authority);
      queryClient.invalidateQueries({
        queryKey: adminKeys.deviceDetail(variables.userId, variables.deviceId),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save device override");
    },
  });
}

export function useDeleteAdminUserDeviceSetting() {
  const queryClient = useQueryClient();
  const authority = captureAdminSettingAuthority();
  return useMutation({
    retry: false,
    mutationFn: ({
      userId,
      profileId,
      deviceId,
      key,
    }: {
      userId: number;
      profileId: string;
      deviceId: string;
      key: string;
    }) =>
      deleteAdminSettingValue(
        userId,
        key,
        { scope: "profile_device", profileId, deviceId },
        authority,
      ),
    onSuccess: (_data, variables) => {
      toast.success("Device override reset");
      invalidateAdminDeviceCaches(queryClient, variables.userId, authority);
      queryClient.invalidateQueries({
        queryKey: adminKeys.deviceDetail(variables.userId, variables.deviceId),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to reset override");
    },
  });
}

/**
 * Resets every override for one (profile, device) pair. The bulk server route
 * went away with the registry API, so this issues one canonical delete per
 * key; a 404 means the row is already gone, which is the goal state, not a
 * failure.
 */
export function useDeleteAllAdminUserDeviceSettingsForDevice() {
  const queryClient = useQueryClient();
  const authority = captureAdminSettingAuthority();
  return useMutation({
    retry: false,
    mutationFn: async ({
      userId,
      profileId,
      deviceId,
      keys,
    }: {
      userId: number;
      profileId: string;
      deviceId: string;
      keys: readonly string[];
    }) => {
      let completed = 0;
      for (const key of keys) {
        try {
          await deleteAdminSettingValue(
            userId,
            key,
            { scope: "profile_device", profileId, deviceId },
            authority,
          );
          completed++;
        } catch (err) {
          if (err instanceof V2ProblemError && err.status === 404) {
            completed++;
            continue;
          }
          throw new Error(
            `Reset stopped after ${completed} of ${keys.length} overrides. Reload the settings before continuing. ${err instanceof Error ? err.message : "The remaining override could not be reset."}`,
          );
        }
      }
      return { completed };
    },
    onSuccess: (_data, variables) => {
      toast.success("All device overrides reset");
      invalidateAdminDeviceCaches(queryClient, variables.userId, authority);
      queryClient.invalidateQueries({
        queryKey: adminKeys.deviceDetail(variables.userId, variables.deviceId),
      });
    },
    onError: (err, variables) => {
      invalidateAdminDeviceCaches(queryClient, variables.userId, authority);
      toast.error(err instanceof Error ? err.message : "Failed to reset device");
    },
  });
}

export function useAdminDevices() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminKeys.devices(), adminUserScope(context)],
    queryFn: () => listAdminDevices(context ?? captureAdminUserAuthority()),
    enabled: context !== null,
    staleTime: ADMIN_STALE_TIME,
  });
}
export function useAdminDeviceDetail(userId: number, deviceId: string, enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminKeys.deviceDetail(userId, deviceId), adminUserScope(context)],
    queryFn: () => getAdminDevice(userId, deviceId, context ?? captureAdminUserAuthority()),
    enabled: enabled && context !== null && userId > 0 && deviceId.length > 0,
    staleTime: ADMIN_STALE_TIME,
  });
}

/** Issues a password reset link. Not retried: each call replaces the
 * account's link, and a delivery the server could not confirm is reported,
 * not repeated. */
export function useIssuePasswordReset() {
  return useMutation({
    retry: false,
    mutationFn: ({ id, delivery }: { id: number; delivery: "email" | "link" }) =>
      issueAdminPasswordReset(id, delivery),
    gcTime: 0,
  });
}

export function useImpersonateUser() {
  return useMutation({
    retry: false,
    mutationFn: ({
      id,
      profileContext,
    }: {
      id: number;
      profileContext: ReturnType<typeof captureAdminUserAuthority>;
    }) => impersonateAdminUser(id, profileContext),
    gcTime: 0,
  });
}
