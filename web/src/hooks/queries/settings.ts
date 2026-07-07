import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { storage } from "@/utils/storage";
import { settingsKeys } from "./keys";

export interface SettingEntry {
  key: string;
  value: string;
}

export interface EffectiveSettingEntry {
  key: string;
  profile_id?: string;
  user_value?: string;
  device_value?: string;
  effective_value: string;
  source: "unset" | "default" | "user" | "device";
  has_device_override: boolean;
  device_id?: string;
  device_name?: string;
  device_platform?: string;
  updated_at?: string;
}

interface SettingsListResponse {
  settings: SettingEntry[];
}

interface EffectiveSettingsListResponse {
  settings: EffectiveSettingEntry[];
}

export type SettingsMap = Record<string, string>;
export type EffectiveSettingsMap = Record<string, EffectiveSettingEntry>;

function normalizeSettings(entries: SettingEntry[] | undefined): SettingsMap {
  if (!entries || entries.length === 0) {
    return {};
  }
  return entries.reduce<SettingsMap>((acc, entry) => {
    acc[entry.key] = entry.value;
    return acc;
  }, {});
}

function normalizeEffectiveSettings(
  entries: EffectiveSettingEntry[] | undefined,
): EffectiveSettingsMap {
  if (!entries || entries.length === 0) {
    return {};
  }
  return entries.reduce<EffectiveSettingsMap>((acc, entry) => {
    acc[entry.key] = entry;
    return acc;
  }, {});
}

function buildEffectiveQuery(keys: string[]) {
  const params = new URLSearchParams();
  params.set("keys", keys.join(","));
  return `/settings/effective?${params.toString()}`;
}

function getActiveProfileIdForSettings() {
  return storage.get(storage.KEYS.PROFILE_ID);
}

export function useSettings(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: settingsKeys.list(),
    queryFn: async () => {
      const result = await api<SettingsListResponse>("/settings");
      return normalizeSettings(result.settings);
    },
    enabled: options?.enabled ?? true,
    staleTime: 5 * 60 * 1000,
  });
}

export function useSetting(key: string, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: settingsKeys.detail(key),
    queryFn: async () => {
      try {
        const result = await api<SettingEntry>(`/settings/${key}`);
        return result.value;
      } catch {
        return null;
      }
    },
    enabled: options?.enabled ?? true,
    staleTime: 5 * 60 * 1000,
  });
}

export function useSetSetting() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) =>
      api(`/settings/${key}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value }),
      }),
    onMutate: async ({ key, value }) => {
      await Promise.all([
        qc.cancelQueries({ queryKey: settingsKeys.list() }),
        qc.cancelQueries({ queryKey: settingsKeys.detail(key) }),
      ]);
      const previousList = qc.getQueryData<SettingsMap>(settingsKeys.list());
      const previousDetail = qc.getQueryData<string | null>(settingsKeys.detail(key));
      qc.setQueryData(settingsKeys.detail(key), value);
      qc.setQueryData<SettingsMap | undefined>(settingsKeys.list(), (current) => ({
        ...(current ?? {}),
        [key]: value,
      }));
      return { previousList, previousDetail, key, attemptedValue: value };
    },
    onError: (_err, _vars, context) => {
      if (!context) return;
      // Roll back only this key, and only while our optimistic value is still
      // current: a concurrent mutation (other key, or a newer save of the same
      // key) may have already overwritten it and must not be resurrected.
      qc.setQueryData<SettingsMap | undefined>(settingsKeys.list(), (current) => {
        if (!current) return context.previousList;
        if (current[context.key] !== context.attemptedValue) return current;
        const next = { ...current };
        const previousValue = context.previousList?.[context.key];
        if (previousValue === undefined) {
          delete next[context.key];
        } else {
          next[context.key] = previousValue;
        }
        return next;
      });
      qc.setQueryData<string | null | undefined>(settingsKeys.detail(context.key), (current) =>
        current === context.attemptedValue ? context.previousDetail : current,
      );
    },
    onSettled: (_data, _err, variables) => {
      const profileId = getActiveProfileIdForSettings();
      qc.invalidateQueries({ queryKey: settingsKeys.list() });
      qc.invalidateQueries({ queryKey: settingsKeys.detail(variables.key) });
      qc.invalidateQueries({ queryKey: settingsKeys.effective(profileId, [variables.key]) });
      qc.invalidateQueries({ queryKey: ["settings", "effective"] });
    },
  });
}

export function useDeleteSetting() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ key }: { key: string }) =>
      api(`/settings/${key}`, {
        method: "DELETE",
      }),
    onSuccess: (_data, variables) => {
      qc.removeQueries({ queryKey: settingsKeys.detail(variables.key) });
      qc.setQueryData<SettingsMap | undefined>(settingsKeys.list(), (current) => {
        if (!current) return current;
        const next = { ...current };
        delete next[variables.key];
        return next;
      });
      qc.invalidateQueries({ queryKey: settingsKeys.list() });
      const profileId = getActiveProfileIdForSettings();
      qc.invalidateQueries({ queryKey: settingsKeys.effective(profileId, [variables.key]) });
      qc.invalidateQueries({ queryKey: ["settings", "effective"] });
    },
  });
}

export function useDeviceSetting(
  profileId: string | null | undefined,
  key: string,
  options?: { enabled?: boolean },
) {
  return useQuery({
    queryKey: settingsKeys.deviceDetail(profileId, key),
    queryFn: async () => {
      try {
        const result = await api<SettingEntry>(`/settings/device/${key}`);
        return result.value;
      } catch {
        return null;
      }
    },
    enabled: (options?.enabled ?? true) && Boolean(profileId),
    staleTime: 60 * 1000,
  });
}

export function useSetDeviceSetting() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) =>
      api(`/settings/device/${key}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value }),
      }),
    onSuccess: (_data, variables) => {
      const profileId = getActiveProfileIdForSettings();
      qc.setQueryData(settingsKeys.deviceDetail(profileId, variables.key), variables.value);
      qc.invalidateQueries({ queryKey: settingsKeys.deviceDetail(profileId, variables.key) });
      qc.invalidateQueries({ queryKey: settingsKeys.effective(profileId, [variables.key]) });
      qc.invalidateQueries({ queryKey: ["settings", "effective"] });
    },
  });
}

export function useDeleteDeviceSetting() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ key }: { key: string }) =>
      api(`/settings/device/${key}`, {
        method: "DELETE",
      }),
    onSuccess: (_data, variables) => {
      const profileId = getActiveProfileIdForSettings();
      qc.setQueryData(settingsKeys.deviceDetail(profileId, variables.key), null);
      qc.invalidateQueries({ queryKey: settingsKeys.deviceDetail(profileId, variables.key) });
      qc.invalidateQueries({ queryKey: settingsKeys.effective(profileId, [variables.key]) });
      qc.invalidateQueries({ queryKey: ["settings", "effective"] });
    },
  });
}

export function useEffectiveSettings(
  profileId: string | null | undefined,
  keys: string[],
  options?: { enabled?: boolean },
) {
  const uniqueKeys = [...new Set(keys.filter(Boolean))];
  return useQuery({
    queryKey: settingsKeys.effective(profileId, uniqueKeys),
    queryFn: async () => {
      const result = await api<EffectiveSettingsListResponse>(buildEffectiveQuery(uniqueKeys));
      return normalizeEffectiveSettings(result.settings);
    },
    enabled: (options?.enabled ?? true) && uniqueKeys.length > 0 && Boolean(profileId),
    staleTime: 30 * 1000,
  });
}
