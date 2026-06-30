import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  AdminAccessGroup,
  AdminAccessGroupDetail,
  AdminAccessGroupWriteRequest,
  AdminUserAccessExplanation,
  AdminDeviceDetail,
  AdminDeviceSummary,
  AdminSettingEntry,
  AdminUser,
  CreateUserRequest,
  LoginResponse,
  UpdateUserRequest,
} from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

interface AdminSettingsResponse {
  settings: AdminSettingEntry[];
}

interface AdminDeviceSettingsResponse {
  settings: AdminDeviceSetting[];
}

interface AdminDevicesResponse {
  devices: AdminDeviceSummary[];
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

function invalidateAdminDeviceCaches(
  queryClient: ReturnType<typeof useQueryClient>,
  userId: number,
) {
  queryClient.invalidateQueries({ queryKey: adminKeys.userDeviceSettings(userId) });
  queryClient.invalidateQueries({ queryKey: adminKeys.devices() });
}

export function useAdminUsers() {
  return useQuery({
    queryKey: adminKeys.users(),
    queryFn: () => api<AdminUser[]>("/admin/users").then((d) => d ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminAccessGroups(enabled = true) {
  return useQuery({
    queryKey: adminKeys.accessGroups(),
    queryFn: () => api<AdminAccessGroup[]>("/admin/access-groups").then((d) => d ?? []),
    enabled,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminAccessGroup(slug: string | null, enabled = true) {
  return useQuery({
    queryKey: adminKeys.accessGroup(slug ?? ""),
    queryFn: () => api<AdminAccessGroupDetail>(`/admin/access-groups/${slug}`),
    enabled: enabled && Boolean(slug),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminUserAccessExplanation(userId: number | null, enabled = true) {
  return useQuery({
    queryKey: adminKeys.userAccessExplanation(userId ?? 0),
    queryFn: () => api<AdminUserAccessExplanation>(`/admin/users/${userId}/access-explain`),
    enabled: enabled && userId != null,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useCreateAdminAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: AdminAccessGroupWriteRequest) =>
      api<AdminAccessGroupDetail>("/admin/access-groups", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: (group) => {
      toast.success("Access group created");
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
      if (group?.slug)
        queryClient.invalidateQueries({ queryKey: adminKeys.accessGroup(group.slug) });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save access group");
    },
  });
}

export function useUpdateAdminAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ slug, body }: { slug: string; body: AdminAccessGroupWriteRequest }) =>
      api<AdminAccessGroupDetail>(`/admin/access-groups/${slug}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: (group, variables) => {
      toast.success("Access group updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroup(variables.slug) });
      if (group?.slug && group.slug !== variables.slug) {
        queryClient.invalidateQueries({ queryKey: adminKeys.accessGroup(group.slug) });
      }
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save access group");
    },
  });
}

export function useDeleteAdminAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (slug: string) => api(`/admin/access-groups/${slug}`, { method: "DELETE" }),
    onSuccess: (_data, slug) => {
      toast.success("Access group deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroup(slug) });
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete access group");
    },
  });
}

export function useAdminUser(id: number) {
  return useQuery({
    queryKey: adminKeys.userDetail(id),
    queryFn: () => api<AdminUser>(`/admin/users/${id}`),
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
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
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
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
    },
  });
}

export function useAdminUserSettings(userId: number) {
  return useQuery({
    queryKey: adminKeys.userSettings(userId),
    queryFn: () =>
      api<AdminSettingsResponse>(`/admin/users/${userId}/settings`).then((d) => d?.settings ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpdateAdminUserSetting() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, key, value }: { userId: number; key: string; value: string }) =>
      api(`/admin/users/${userId}/settings/${encodeURIComponent(key)}`, {
        method: "PUT",
        body: JSON.stringify({ value }),
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
    mutationFn: ({ userId, key }: { userId: number; key: string }) =>
      api(`/admin/users/${userId}/settings/${encodeURIComponent(key)}`, {
        method: "DELETE",
      }),
    onSuccess: (_data, variables) => {
      toast.success("User setting reset");
      queryClient.invalidateQueries({ queryKey: adminKeys.userSettings(variables.userId) });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to reset setting");
    },
  });
}

export function useAdminUserDeviceSettings(userId: number) {
  return useQuery({
    queryKey: adminKeys.userDeviceSettings(userId),
    queryFn: () =>
      api<AdminDeviceSettingsResponse>(`/admin/users/${userId}/device-settings`).then(
        (d) => d?.settings ?? [],
      ),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpdateAdminUserDeviceSetting() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
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
      api(
        `/admin/users/${userId}/profiles/${encodeURIComponent(profileId)}/device-settings/${encodeURIComponent(key)}/${encodeURIComponent(deviceId)}`,
        {
          method: "PUT",
          body: JSON.stringify({ value }),
        },
      ),
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
      api(
        `/admin/users/${userId}/profiles/${encodeURIComponent(profileId)}/device-settings/${encodeURIComponent(key)}/${encodeURIComponent(deviceId)}`,
        { method: "DELETE" },
      ),
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

export function useDeleteAllAdminUserDeviceSettingsForDevice() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      userId,
      profileId,
      deviceId,
    }: {
      userId: number;
      profileId: string;
      deviceId: string;
    }) =>
      api(
        `/admin/users/${userId}/profiles/${encodeURIComponent(profileId)}/devices/${encodeURIComponent(deviceId)}/settings`,
        {
          method: "DELETE",
        },
      ),
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
    queryFn: () => api<AdminDevicesResponse>("/admin/devices").then((d) => d.devices ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminDeviceDetail(userId: number, deviceId: string, enabled = true) {
  return useQuery({
    queryKey: adminKeys.deviceDetail(userId, deviceId),
    queryFn: () =>
      api<AdminDeviceDetail>(`/admin/devices/${userId}/${encodeURIComponent(deviceId)}`),
    enabled: enabled && userId > 0 && deviceId.length > 0,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useImpersonateUser() {
  return useMutation({
    mutationFn: (id: number) =>
      api<LoginResponse>(`/admin/users/${id}/impersonate`, {
        method: "POST",
      }),
  });
}
