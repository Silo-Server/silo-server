import { useState, useEffect, useCallback, useMemo } from "react";
import type { AdminSettingsConnectionCheckRequest } from "@/api/types";
import {
  useAdminServerSettings,
  useUpdateServerSetting,
  useAdminSensitiveStatus,
} from "@/hooks/queries/admin/settings";

interface UseSettingsFormOptions {
  /** Setting keys this section manages */
  keys: string[];
}

export function useSettingsForm({ keys }: UseSettingsFormOptions) {
  const { data: settings, isLoading } = useAdminServerSettings();
  const { data: sensitiveData } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();

  const [localValues, setLocalValues] = useState<Record<string, string>>({});
  const [dirty, setDirty] = useState<Set<string>>(new Set());
  const [restartRequired, setRestartRequired] = useState(false);

  // Sync from server when settings load
  useEffect(() => {
    if (!settings) return;
    setLocalValues((prev) => {
      const next = { ...prev };
      for (const key of keys) {
        // Only set if not dirty (user hasn't edited)
        if (!dirty.has(key)) {
          next[key] = settings[key] ?? "";
        }
      }
      return next;
    });
  }, [settings, keys, dirty]);

  const getValue = useCallback(
    (key: string) => localValues[key] ?? settings?.[key] ?? "",
    [localValues, settings],
  );

  const setValue = useCallback((key: string, value: string) => {
    setLocalValues((prev) => ({ ...prev, [key]: value }));
    setDirty((prev) => new Set(prev).add(key));
  }, []);

  const dirtyCount = dirty.size;
  const dirtyKeys = useMemo(() => Array.from(dirty), [dirty]);

  const isDirty = useCallback((key: string) => dirty.has(key), [dirty]);

  const buildConnectionCheckRequest = useCallback(
    (selectedKeys: string[] = keys): AdminSettingsConnectionCheckRequest => ({
      values: Object.fromEntries(
        selectedKeys.map((key) => [key, localValues[key] ?? settings?.[key] ?? ""]),
      ),
      dirty_keys: selectedKeys.filter((key) => dirty.has(key)),
    }),
    [dirty, keys, localValues, settings],
  );

  const save = useCallback(async () => {
    const promises = Array.from(dirty).map((key) =>
      updateSetting.mutateAsync({ key, value: localValues[key] ?? "" }),
    );
    const results = await Promise.all(promises);
    setDirty(new Set());
    // The backend reports per key whether a restart is needed; most settings
    // hot-reload. Once a restart-required key was saved, keep the banner up
    // until the server actually restarts.
    if (results.some((r) => r?.restart_required)) {
      setRestartRequired(true);
    }
  }, [dirty, localValues, updateSetting]);

  const discard = useCallback(() => {
    if (!settings) return;
    const reset: Record<string, string> = {};
    for (const key of keys) {
      reset[key] = settings[key] ?? "";
    }
    setLocalValues((prev) => ({ ...prev, ...reset }));
    setDirty(new Set());
  }, [settings, keys]);

  const sensitiveConfigured = sensitiveData?.configured ?? [];
  const sensitiveManagedByEnv = sensitiveData?.managed_by_env ?? [];

  return {
    isLoading,
    getValue,
    setValue,
    dirtyCount,
    dirtyKeys,
    isDirty,
    save,
    discard,
    isSaving: updateSetting.isPending,
    restartRequired,
    sensitiveConfigured,
    sensitiveManagedByEnv,
    buildConnectionCheckRequest,
  };
}
