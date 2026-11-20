import { useState, useEffect, useCallback, useMemo, useRef } from "react";
import { toast } from "sonner";
import { t } from "@/i18n";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import {
  adminSettingsKey,
  captureSettingsBaseline,
  type ChatGPTSettingsChange,
} from "@/api/v2/adminSettingsSnapshot";
import type { AdminSettingsConnectionCheckRequest } from "@/api/types";
import {
  useAdminServerSettings,
  useUpdateServerSettings,
  useAdminSensitiveStatus,
} from "@/hooks/queries/admin/settings";
import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";

interface UseSettingsFormOptions {
  /** Setting keys this section manages */
  keys: string[];
}

export function useSettingsForm({ keys }: UseSettingsFormOptions) {
  const { data: settings, isLoading, isError: loadError, refetch } = useAdminServerSettings();
  // Only a list that never loaded leaves the status unknown. A failed re-read
  // keeps the last list, so a page keeps its editor and the drafts in it.
  const { data: sensitiveData, isLoadingError: sensitiveStatusError } = useAdminSensitiveStatus();
  const editBaseline = useRef<Record<string, string> | undefined>(undefined);
  const [, setBaselineRevision] = useState(0);
  const saveEpoch = useRef(0);
  const savePending = useRef(false);
  // The snapshot the last acknowledged save was made over. Until a newer one
  // arrives, it no longer shows what is saved.
  const savedOver = useRef<Record<string, string> | undefined>(undefined);
  const updateSettings = useUpdateServerSettings(editBaseline.current ?? settings);

  const [localValues, setLocalValues] = useState<Record<string, string>>({});
  const [dirty, setDirty] = useState<Set<string>>(new Set());
  const [restartRequired, setRestartRequired] = useState(false);
  const editVersions = useRef(new Map<string, number>());
  const dirtyRef = useRef(dirty);
  const context = captureProfileRequestContext();
  const authority = JSON.stringify(context ? adminSettingsKey(context) : null);
  const [draftAuthority, setDraftAuthority] = useState(authority);
  if (draftAuthority !== authority) {
    setDraftAuthority(authority);
    setLocalValues({});
    setDirty(new Set());
    setRestartRequired(false);
    editVersions.current.clear();
    editBaseline.current = undefined;
    savedOver.current = undefined;
    dirtyRef.current = new Set();
  }
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);

  // The server refuses a whole save that writes a key the environment sets,
  // and that key's field is disabled. A server can restart with the variable
  // set while the page holds a draft for the key, so drop that draft: it can
  // be neither saved nor undone, and the rest of the page has to save. The
  // server still accepts one such write, clearing redis.db under REDIS_URL,
  // which is how a saved number is removed, so that draft stays.
  const envVars = sensitiveData?.managed_by_env_vars;
  const envLockedDrafts = envVars
    ? Array.from(dirty).filter(
        (key) =>
          Object.hasOwn(envVars, key) &&
          !(key === "redis.db" && (localValues[key] ?? "").trim() === ""),
      )
    : [];
  if (envLockedDrafts.length > 0) {
    setDirty((prev) => {
      const next = new Set(prev);
      for (const key of envLockedDrafts) next.delete(key);
      return next;
    });
    // Without a draft the field shows the settings snapshot again.
    setLocalValues((prev) => {
      const next = { ...prev };
      for (const key of envLockedDrafts) delete next[key];
      return next;
    });
  }

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

  const getPersistedValue = useCallback((key: string) => settings?.[key] ?? "", [settings]);

  /**
   * Stages a value. A value equal to the saved one undoes the edit instead,
   * unless `stage` is set: a caller that must write an unchanged value along
   * with a related edit (the Redis database number with a new URL) passes it.
   */
  const setValue = useCallback(
    (key: string, value: string, { stage = false }: { stage?: boolean } = {}) => {
      if (!editBaseline.current) editBaseline.current = settings;
      editVersions.current.set(key, (editVersions.current.get(key) ?? 0) + 1);
      setLocalValues((prev) => ({ ...prev, [key]: value }));
      // The snapshot leaves secrets out, so a key it lacks (a staged secret
      // clear among them) stays dirty. An edit made during a save, or before
      // the snapshot that save wrote has arrived, stays dirty too, because the
      // snapshot cannot yet say what is saved.
      const reverted =
        !stage &&
        !savePending.current &&
        settings != null &&
        settings !== savedOver.current &&
        Object.hasOwn(settings, key) &&
        settings[key] === value;
      setDirty((prev) => {
        const next = new Set(prev);
        if (reverted) next.delete(key);
        else next.add(key);
        return next;
      });
    },
    [settings],
  );

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
  useEffect(() => {
    if (dirtyCount === 0) editBaseline.current = undefined;
  }, [dirtyCount]);
  const dirtyKeys = useMemo(() => Array.from(dirty), [dirty]);

  // The shared registry guards tab close and reload. `UnsavedChangesGuard`
  // also blocks in-app navigation while this registration is live. Reporting
  // through the registry keeps the hook usable without a router or guard,
  // including the setup wizard.
  useReportUnsavedChanges(dirtyCount > 0);

  const isDirty = useCallback((key: string) => dirty.has(key), [dirty]);

  // A staged clear is a dirty empty value: the save batch writes "" and the
  // server drops the stored value. This is what `SecretField`'s own clear
  // affordance stages, and the one thing that distinguishes it from the
  // "leave blank to keep the saved secret" default.
  const isClearStaged = useCallback(
    (key: string) => dirty.has(key) && getValue(key) === "",
    [dirty, getValue],
  );

  const buildConnectionCheckRequest = useCallback(
    (selectedKeys: string[] = keys): AdminSettingsConnectionCheckRequest => ({
      values: Object.fromEntries(
        selectedKeys.map((key) => [key, localValues[key] ?? settings?.[key] ?? ""]),
      ),
      dirty_keys: selectedKeys.filter((key) => dirty.has(key)),
    }),
    [dirty, keys, localValues, settings],
  );

  const beginSettingsTransition = useCallback(() => {
    const startedAt = saveEpoch.current;
    const overlappingSave = savePending.current;
    const authority = captureProfileRequestContext();
    return (change: ChatGPTSettingsChange) => {
      if (
        !authority ||
        !isCapturedProfileAuthorityActive(authority) ||
        overlappingSave ||
        savePending.current ||
        saveEpoch.current !== startedAt
      )
        return;
      if (
        !editBaseline.current ||
        !change.settingsSnapshot ||
        dirtyRef.current.has("ai.chat_model") ||
        dirtyRef.current.has("subtitle_ai.chat_model") ||
        dirtyRef.current.has("ai.reasoning_effort")
      )
        return;
      try {
        const before = captureSettingsBaseline(editBaseline.current);
        const after = captureSettingsBaseline(change.settingsSnapshot);
        if (before.etag !== change.beforeETag || after.etag !== change.afterETag) return;
        // Both records must belong to the initiating authority, even if an old
        // callback is delivered after the form switched profiles.
        if (
          JSON.stringify(adminSettingsKey(before.profileContext)) !==
            JSON.stringify(adminSettingsKey(authority)) ||
          JSON.stringify(adminSettingsKey(after.profileContext)) !==
            JSON.stringify(adminSettingsKey(authority))
        )
          return;
        editBaseline.current = change.settingsSnapshot;
        setBaselineRevision((revision) => revision + 1);
      } catch {
        // Keep the original conflict guard when the transition cannot be proven.
      }
    };
  }, []);

  const save = useCallback(
    async (selectedKeys?: string[]) => {
      const submittedKeys = Array.from(dirty).filter((key) => selectedKeys?.includes(key) ?? true);
      if (submittedKeys.length === 0) return;
      const values = Object.fromEntries(submittedKeys.map((key) => [key, localValues[key] ?? ""]));
      const submittedVersions = new Map(
        submittedKeys.map((key) => [key, editVersions.current.get(key) ?? 0]),
      );
      // Callers must distinguish failed writes from acknowledged saves before
      // advancing a wizard or performing a dependent mutation.
      if (!settings) {
        const error = new Error(t("web.admin.settings.hooks.settings.reloadBeforeSaving"));
        toast.error(error.message);
        throw error;
      }
      saveEpoch.current++;
      savePending.current = true;
      let result;
      try {
        result = await updateSettings.mutateAsync(values);
      } finally {
        savePending.current = false;
      }
      // An acknowledged save changes the validator even when newer local edits
      // remain dirty. Reconcile only with a refresh matching that write's revision;
      // background reads and failed writes never advance this draft baseline.
      if (result.settingsSnapshot) editBaseline.current = result.settingsSnapshot;
      savedOver.current = settings;
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
    },
    [dirty, localValues, settings, updateSettings],
  );

  const discard = useCallback(() => {
    if (!settings) return;
    const reset: Record<string, string> = {};
    for (const key of keys) {
      editVersions.current.set(key, (editVersions.current.get(key) ?? 0) + 1);
      reset[key] = settings[key] ?? "";
    }
    setLocalValues((prev) => ({ ...prev, ...reset }));
    editBaseline.current = undefined;
    setDirty(new Set());
  }, [settings, keys]);

  const sensitiveConfigured = sensitiveData?.configured ?? [];
  const sensitiveManagedByEnv = sensitiveData?.managed_by_env ?? [];
  /**
   * The environment variable that sets `key` on the server, or undefined when
   * an admin can save it. Pass it to a field's `managedByEnv`.
   */
  const envVar = useCallback((key: string) => envVars?.[key], [envVars]);

  // Until the status is known a field cannot tell whether the environment
  // sets it, so the form stays in its loading state rather than offer an edit
  // the server would refuse. A failed status request ends the wait.
  const sensitiveStatusPending = sensitiveData == null && !sensitiveStatusError;

  return {
    isLoading: isLoading || sensitiveStatusPending,
    /**
     * True until the snapshot has either arrived or failed. Unlike `isLoading`
     * this also covers a query that has not been allowed to start yet (no
     * profile selected), so a form can hold its skeleton through that gap.
     */
    isPending: (settings == null && !loadError) || sensitiveStatusPending,
    /** True when the settings snapshot could not be read; values are unset. */
    loadError,
    retryLoad: refetch,
    /** True once the settings snapshot is available to read and save against. */
    loaded: settings != null,
    getValue,
    getPersistedValue,
    setValue,
    resetValue,
    dirtyCount,
    dirtyKeys,
    isDirty,
    isClearStaged,
    save,
    discard,
    isSaving: updateSettings.isPending,
    restartRequired,
    sensitiveConfigured,
    sensitiveManagedByEnv,
    envVar,
    sensitiveStatusReady: sensitiveData != null,
    sensitiveStatusError,
    buildConnectionCheckRequest,
    beginSettingsTransition,
  };
}
