import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { api, ApiClientError } from "@/api/client";
import type {
  AdminDeviceDetail,
  AdminDeviceSummary,
  AdminUser,
  CreateUserRequest,
  LoginResponse,
  UpdateUserRequest,
} from "@/api/types";
import { v2, type V2Result } from "@/api/v2/request";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { adminKeys } from "../keys";
import { useAdminUserProfiles } from "./history";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

interface AdminDevicesResponse {
  devices: AdminDeviceSummary[];
}

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

/** One stored row as GET /admin/users/{id}/settings/values reports it. */
interface AdminSettingValueRow {
  key: string;
  scope: AdminSettingScope;
  profile_id?: string;
  client_family?: AdminSettingClientFamily;
  device_id?: string;
  library_id?: number;
  series_id?: string;
  value: unknown;
  revision: number;
  updated_at?: string;
}

interface AdminSettingValuesResponse {
  values: AdminSettingValueRow[] | null;
  revision: number;
}

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

function identityQuery(identity: AdminSettingIdentity): string {
  const params = new URLSearchParams({ scope: identity.scope });
  if (identity.profileId) params.set("profile_id", identity.profileId);
  if (identity.clientFamily) params.set("client_family", identity.clientFamily);
  if (identity.deviceId) params.set("device_id", identity.deviceId);
  if (identity.libraryId !== undefined) params.set("library_id", String(identity.libraryId));
  if (identity.seriesId !== undefined) params.set("series_id", identity.seriesId);
  return params.toString();
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

function adminSettingValuePath(userId: number, key: string, identity: AdminSettingIdentity) {
  return `/admin/users/${userId}/settings/values/${encodeURIComponent(key)}?${identityQuery(identity)}`;
}

/** Every explicit canonical value the target user has stored, all scopes. */
function useAdminUserSettingValues(userId: number) {
  return useQuery({
    queryKey: adminKeys.userSettings(userId),
    queryFn: async () => {
      const response: AdminSettingValuesResponse | null = await api(
        `/admin/users/${userId}/settings/values`,
      );
      return response?.values ?? [];
    },
    staleTime: ADMIN_STALE_TIME,
  });
}

function invalidateAdminDeviceCaches(
  queryClient: ReturnType<typeof useQueryClient>,
  userId: number,
) {
  // Device overrides derive from the canonical values list, which lives under
  // the userSettings key.
  queryClient.invalidateQueries({ queryKey: adminKeys.userSettings(userId) });
  queryClient.invalidateQueries({ queryKey: adminKeys.devices() });
}

type AdminUserV2 = V2Result<"GET /api/v2/admin/users">["items"][number];

/**
 * Projects a v2 admin user onto the `AdminUser` shape the admin pages share
 * with the detail, create, update, and delete operations, which still answer
 * on v1 with numeric ids. The v2 ids are the same ids as strings; an absent
 * last activity is null on v2 and omitted on v1.
 */
export function adminUserFromV2(user: AdminUserV2): AdminUser {
  return {
    id: Number(user.id),
    username: user.username,
    email: user.email,
    role: user.role,
    permissions: user.permissions,
    enabled: user.enabled,
    library_ids: user.library_ids === null ? null : user.library_ids.map(Number),
    access_group_id: user.access_group_id === null ? null : Number(user.access_group_id),
    max_playback_quality: user.max_playback_quality,
    max_streams: user.max_streams,
    max_transcodes: user.max_transcodes,
    transcode_allowed: user.transcode_allowed,
    audio_transcode_allowed: user.audio_transcode_allowed,
    max_profiles: user.max_profiles,
    download_allowed: user.download_allowed,
    download_transcode_allowed: user.download_transcode_allowed,
    requests_allowed: user.requests_allowed,
    effective_policy: {
      library_ids: user.effective_policy.library_ids.map(Number),
      max_playback_quality: user.effective_policy.max_playback_quality,
      max_streams: user.effective_policy.max_streams,
      max_transcodes: user.effective_policy.max_transcodes,
      transcode_allowed: user.effective_policy.transcode_allowed,
      audio_transcode_allowed: user.effective_policy.audio_transcode_allowed,
      download_allowed: user.effective_policy.download_allowed,
      download_transcode_allowed: user.effective_policy.download_transcode_allowed,
      requests_allowed: user.effective_policy.requests_allowed,
      permissions: user.effective_policy.permissions,
    },
    created_at: user.created_at,
    updated_at: user.updated_at,
    ...(user.last_active_at === null ? {} : { last_active_at: user.last_active_at }),
  };
}

/** Page size for the admin account walk: the v2 maximum. */
const ADMIN_USERS_PAGE_LIMIT = 200;

/**
 * Fetches every account by walking the cursor-paginated v2 listing. The admin
 * screens render, filter, and pick from the full list, so the walk runs to
 * completion; a page the server marks as the last one ends it.
 */
export async function fetchAllAdminUsers(signal?: AbortSignal): Promise<AdminUser[]> {
  const users: AdminUser[] = [];
  let cursor: string | undefined;
  for (;;) {
    const page = await v2("GET /api/v2/admin/users", {
      query: { limit: ADMIN_USERS_PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) },
      signal,
    });
    users.push(...page.items.map(adminUserFromV2));
    if (!page.page?.has_more || !page.page.next_cursor) return users;
    cursor = page.page.next_cursor;
  }
}

export function useAdminUsers() {
  return useQuery({
    queryKey: adminKeys.users(),
    queryFn: ({ signal }) => fetchAllAdminUsers(signal),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminUser(id: number) {
  return useQuery({
    queryKey: adminKeys.userDetail(id),
    queryFn: (): Promise<AdminUser> => api(`/admin/users/${id}`),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useCreateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateUserRequest) =>
      api("/admin/users", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("User created");
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });
}

export function useUpdateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: UpdateUserRequest }) =>
      api(`/admin/users/${id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      toast.success("User updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
      queryClient.invalidateQueries({ queryKey: adminKeys.userDetail(variables.id) });
      queryClient.invalidateQueries({ queryKey: adminKeys.userProfiles(variables.id) });
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });
}

export function useDeleteUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/admin/users/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("User deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
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
  return useMutation({
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
      api(adminSettingValuePath(userId, key, identity), {
        method: "PUT",
        body: JSON.stringify({ value: settingValueFromString(key, value) }),
      }),
    onSuccess: (_data, variables) => {
      toast.success("User setting updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.userSettings(variables.userId) });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save setting");
    },
  });
}

export function useDeleteAdminUserSetting() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      userId,
      key,
      identity,
    }: {
      userId: number;
      key: string;
      identity: AdminSettingIdentity;
    }) => {
      const path = adminSettingValuePath(userId, key, identity);
      if (key === SETTING_KEYS.NAV_SHORTCUTS) {
        // Shortcut history is revisioned and may not be erased. Its reset is
        // the atomic empty document accepted by the canonical admin endpoint.
        return api(path, {
          method: "PUT",
          body: JSON.stringify({ value: { items: [] } }),
        });
      }
      return api(path, { method: "DELETE" });
    },
    onSuccess: (_data, variables) => {
      toast.success("User setting reset");
      queryClient.invalidateQueries({ queryKey: adminKeys.userSettings(variables.userId) });
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
 * The device *detail* endpoint (GET /admin/devices/{user}/{device}) still
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
  return useMutation({
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
      api(adminSettingValuePath(userId, key, { scope: "profile_device", profileId, deviceId }), {
        method: "PUT",
        body: JSON.stringify({ value: settingValueFromString(key, value) }),
      }),
    onSuccess: (_data, variables) => {
      toast.success("Device override updated");
      invalidateAdminDeviceCaches(queryClient, variables.userId);
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
  return useMutation({
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
      api(adminSettingValuePath(userId, key, { scope: "profile_device", profileId, deviceId }), {
        method: "DELETE",
      }),
    onSuccess: (_data, variables) => {
      toast.success("Device override reset");
      invalidateAdminDeviceCaches(queryClient, variables.userId);
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
  return useMutation({
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
      for (const key of keys) {
        try {
          await api(
            adminSettingValuePath(userId, key, { scope: "profile_device", profileId, deviceId }),
            { method: "DELETE" },
          );
        } catch (err) {
          if (err instanceof ApiClientError && err.status === 404) continue;
          throw err;
        }
      }
    },
    onSuccess: (_data, variables) => {
      toast.success("All device overrides reset");
      invalidateAdminDeviceCaches(queryClient, variables.userId);
      queryClient.invalidateQueries({
        queryKey: adminKeys.deviceDetail(variables.userId, variables.deviceId),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to reset device");
    },
  });
}

export function useAdminDevices() {
  return useQuery({
    queryKey: adminKeys.devices(),
    queryFn: async () => {
      const response: AdminDevicesResponse = await api("/admin/devices");
      return response.devices ?? [];
    },
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminDeviceDetail(userId: number, deviceId: string, enabled = true) {
  return useQuery({
    queryKey: adminKeys.deviceDetail(userId, deviceId),
    queryFn: (): Promise<AdminDeviceDetail> =>
      api(`/admin/devices/${userId}/${encodeURIComponent(deviceId)}`),
    enabled: enabled && userId > 0 && deviceId.length > 0,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useImpersonateUser() {
  return useMutation({
    mutationFn: (id: number): Promise<LoginResponse> =>
      api(`/admin/users/${id}/impersonate`, {
        method: "POST",
      }),
  });
}
