import { useState, useEffect, useCallback, useMemo, useRef } from "react";
import type { AdminSettingsConnectionCheckRequest } from "@/api/types";
import {
  useAdminServerSettings,
  useUpdateServerSettings,
  useAdminSensitiveStatus,
} from "@/hooks/queries/admin/settings";

interface UseSettingsFormOptions {
  /** Setting keys this section manages */
  keys: string[];
}

export function useSettingsForm({ keys }: UseSettingsFormOptions) {
  const { data: settings, isLoading } = useAdminServerSettings();
  const { data: sensitiveData } = useAdminSensitiveStatus();
  const updateSettings = useUpdateServerSettings();

  const [localValues, setLocalValues] = useState<Record<string, string>>({});
  const [dirty, setDirty] = useState<Set<string>>(new Set());
  const [restartRequired, setRestartRequired] = useState(false);
  const editVersions = useRef(new Map<string, number>());
  const dirtyRef = useRef(dirty);
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);

  // Treat equivalent key lists as stable even if a caller constructs the
  // array inline. This keeps server hydration tied to actual query/key changes
  // instead of every render.
  const keySignature = keys.join("\u0000");
  const stableKeys = useMemo(
    () => (keySignature === "" ? [] : keySignature.split("\u0000")),
    [keySignature],
  );

  // Sync from server when settings load
  useEffect(() => {
    if (!settings) return;
    setLocalValues((prev) => {
      const next = { ...prev };
      let changed = false;
      for (const key of stableKeys) {
        // Only set if not dirty (user hasn't edited)
        if (!dirtyRef.current.has(key)) {
          const serverValue = settings[key] ?? "";
          if (next[key] !== serverValue) {
            next[key] = serverValue;
            changed = true;
          }
        }
      }
      return changed ? next : prev;
    });
  }, [settings, stableKeys]);

  const getValue = useCallback(
    (key: string) => localValues[key] ?? settings?.[key] ?? "",
    [localValues, settings],
  );

  const setValue = useCallback((key: string, value: string) => {
    editVersions.current.set(key, (editVersions.current.get(key) ?? 0) + 1);
    setLocalValues((prev) => ({ ...prev, [key]: value }));
    setDirty((prev) => new Set(prev).add(key));
  }, []);

  // Revert one staged field without disturbing other edits. This is also how
  // a redacted secret toggle can cancel a pending clear without needing the
  // server to send the secret back to the browser.
  const resetValue = useCallback(
    (key: string) => {
      editVersions.current.set(key, (editVersions.current.get(key) ?? 0) + 1);
      setLocalValues((prev) => ({ ...prev, [key]: settings?.[key] ?? "" }));
      setDirty((prev) => {
        const next = new Set(prev);
        next.delete(key);
        return next;
      });
    },
    [settings],
  );

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
    if (dirty.size === 0) return;
    const submittedKeys = Array.from(dirty);
    const values = Object.fromEntries(submittedKeys.map((key) => [key, localValues[key] ?? ""]));
    const submittedVersions = new Map(
      submittedKeys.map((key) => [key, editVersions.current.get(key) ?? 0]),
    );
    const result = await updateSettings.mutateAsync(values);
    const settledKeys = submittedKeys.filter(
      (key) => (editVersions.current.get(key) ?? 0) === submittedVersions.get(key),
    );
    setLocalValues((previous) => {
      const next = { ...previous };
      for (const key of settledKeys) {
        // The server returns canonical non-secret values. Sensitive values are
        // intentionally omitted, so erase those drafts after a successful save
        // instead of retaining plaintext credentials in component state.
        next[key] = result.values[key] ?? "";
      }
      return next;
    });
    setDirty((current) => {
      const next = new Set(current);
      for (const key of settledKeys) {
        next.delete(key);
      }
      return next;
    });
    // Once a restart-required batch was saved, keep the banner up until the
    // server actually restarts.
    if (result.restart_required) {
      setRestartRequired(true);
    }
  }, [dirty, localValues, updateSettings]);

  const discard = useCallback(() => {
    if (!settings) return;
    const reset: Record<string, string> = {};
    for (const key of keys) {
      editVersions.current.set(key, (editVersions.current.get(key) ?? 0) + 1);
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
    resetValue,
    dirtyCount,
    dirtyKeys,
    isDirty,
    save,
    discard,
    isSaving: updateSettings.isPending,
    restartRequired,
    sensitiveConfigured,
    sensitiveManagedByEnv,
    buildConnectionCheckRequest,
  };
}
