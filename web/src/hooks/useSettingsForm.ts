import { useState, useEffect, useCallback, useId, useMemo, useRef } from "react";
import { toast } from "sonner";
import { t } from "@/i18n";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import {
  adminSettingsKey,
  captureSettingsBaseline,
  isSettingsSaveConflict,
  type ChatGPTSettingsChange,
  type SettingsValues,
} from "@/api/v2/adminSettingsSnapshot";
import type { AdminSettingsConnectionCheckRequest } from "@/api/types";
import {
  useAdminServerSettings,
  useUpdateServerSettings,
  useAdminSensitiveStatus,
} from "@/hooks/queries/admin/settings";
import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";
import { REDIS_KEYS } from "@/pages/admin-settings/redisDraft";

/**
 * What the snapshot showed for a setting when an edit of it began: its value,
 * undefined when the snapshot leaves the setting out, and whether it was a
 * secret that was set.
 */
interface EditedOver {
  value: string | undefined;
  secretSet: boolean;
}

interface UseSettingsFormOptions {
  /** Setting keys this section manages */
  keys: string[];
}

export function useSettingsForm({ keys }: UseSettingsFormOptions) {
  const { data: settings, isLoading, isError: loadError, refetch } = useAdminServerSettings();
  // Only a list that never loaded leaves the status unknown. A failed re-read
  // keeps the last list, so a page keeps its editor and the drafts in it.
  const {
    data: sensitiveData,
    isLoadingError: sensitiveStatusError,
    refetch: refetchSensitiveStatus,
  } = useAdminSensitiveStatus();
  const editBaseline = useRef<Record<string, string> | undefined>(undefined);
  const [, setBaselineRevision] = useState(0);
  const saveEpoch = useRef(0);
  const savePending = useRef(false);
  // The snapshot the last acknowledged save was made over. Until a newer one
  // arrives, it no longer shows what is saved.
  const savedOver = useRef<Record<string, string> | undefined>(undefined);
  // The form reports a refused save itself, with a way to reload.
  const updateSettings = useUpdateServerSettings(editBaseline.current ?? settings, false);
  const conflictToastId = useId();
  const conflictShown = useRef(false);
  // Bumped whenever the drafts are thrown away (Discard, a profile switch), so
  // a reload or refused save that settles afterwards leaves the form alone.
  const draftGeneration = useRef(0);
  const mounted = useRef(true);
  const reloading = useRef(false);
  const editedOver = useRef(new Map<string, EditedOver>());
  const dismissConflict = useCallback(() => {
    if (!conflictShown.current) return;
    conflictShown.current = false;
    toast.dismiss(conflictToastId);
  }, [conflictToastId]);

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
    editedOver.current.clear();
    draftGeneration.current++;
  }
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      dismissConflict();
    };
  }, [dismissConflict]);

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
    for (const key of envLockedDrafts) editedOver.current.delete(key);
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
      // An edit begins when the key has no record; every path that ends one
      // (save, revert, reset, reload, Discard, a profile switch) drops it.
      if (!editedOver.current.has(key)) {
        editedOver.current.set(key, {
          value: settings && Object.hasOwn(settings, key) ? settings[key] : undefined,
          // Before the status arrives, a secret may be set.
          secretSet: sensitiveData == null || (sensitiveData.configured ?? []).includes(key),
        });
      }
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
      if (reverted) editedOver.current.delete(key);
      setDirty((prev) => {
        const next = new Set(prev);
        if (reverted) next.delete(key);
        else next.add(key);
        return next;
      });
    },
    [sensitiveData, settings],
  );

  // Revert one staged field without disturbing other edits. This is also how
  // a redacted secret toggle can cancel a pending clear without needing the
  // server to send the secret back to the browser.
  const resetValue = useCallback(
    (key: string) => {
      editVersions.current.set(key, (editVersions.current.get(key) ?? 0) + 1);
      editedOver.current.delete(key);
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
    if (dirtyCount > 0) return;
    editBaseline.current = undefined;
    // Nothing is left to save, so a refused save no longer needs a reload.
    dismissConflict();
  }, [dirtyCount, dismissConflict]);
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

  // After a refused save, the admin asked for the settings as they are now.
  // An edit is kept only while its setting still shows what it showed when
  // that edit began; an edit to a setting that changed meanwhile is replaced,
  // so the next save cannot overwrite a change nobody here has seen. Secrets
  // never appear in a snapshot, so an edit to one that was set then or is set
  // now is replaced too: nothing here can show whether it changed. Reads refs
  // and fresh results only, so a toast holding an old copy still reconciles
  // the drafts as they are when it runs.
  const reloadAfterConflict = useCallback(async () => {
    if (reloading.current || savePending.current) return;
    const generation = draftGeneration.current;
    const baseline = editBaseline.current;
    reloading.current = true;
    try {
      // The status is read after the settings, so a secret set by the time of
      // that read shows in it. A later change only makes the next save refused
      // again.
      const settingsFrom = Date.now();
      const settingsResult = await refetch();
      const statusFrom = Date.now();
      const statusResult = await refetchSensitiveStatus();
      // Discard, a profile switch, unmount, a save started meanwhile, or a
      // baseline another path advanced: this result no longer applies.
      if (
        !mounted.current ||
        generation !== draftGeneration.current ||
        savePending.current ||
        editBaseline.current !== baseline
      )
        return;
      if (dirtyRef.current.size === 0) {
        dismissConflict();
        return;
      }
      // Only data read during this reload counts: a refetch that another read
      // cancelled settles with the record it already had.
      const fresh =
        !settingsResult.isError && settingsResult.dataUpdatedAt >= settingsFrom
          ? settingsResult.data
          : undefined;
      // Unknown when the status wasn't read: then a secret may be set.
      const configured =
        !statusResult.isError && statusResult.dataUpdatedAt >= statusFrom
          ? statusResult.data?.configured
          : undefined;
      if (!fresh || !validatorOf(fresh)) {
        toast.error(t("web.admin.settings.hooks.settings.conflict.reloadFailed"));
        return;
      }
      const changed = (key: string) => {
        const was = editedOver.current.get(key);
        const now = Object.hasOwn(fresh, key) ? fresh[key] : undefined;
        if (!was || was.value !== now) return true;
        return now === undefined && (was.secretSet || (configured?.includes(key) ?? true));
      };
      const drafts = [...dirtyRef.current];
      // The Redis URL and database number save as a pair (`stageRedisUrl`).
      const redisReplaced = drafts.some((key) => REDIS_KEYS.includes(key) && changed(key));
      const replaced = drafts.filter(
        (key) => changed(key) || (redisReplaced && REDIS_KEYS.includes(key)),
      );
      // Retire a ChatGPT sign-in callback that started before this reload.
      saveEpoch.current++;
      for (const key of replaced) {
        editVersions.current.set(key, (editVersions.current.get(key) ?? 0) + 1);
        editedOver.current.delete(key);
      }
      setLocalValues((previous) => {
        const next = { ...previous };
        for (const key of replaced) next[key] = fresh[key] ?? "";
        return next;
      });
      setDirty((previous) => new Set([...previous].filter((key) => !replaced.includes(key))));
      editBaseline.current = fresh;
      setBaselineRevision((revision) => revision + 1);
      dismissConflict();
    } finally {
      reloading.current = false;
    }
  }, [dismissConflict, refetch, refetchSensitiveStatus]);

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
      const generation = draftGeneration.current;
      saveEpoch.current++;
      savePending.current = true;
      let result;
      try {
        result = await updateSettings.mutateAsync(values);
      } catch (error) {
        // 412: settings changed after editing began. The drafts stay, and
        // every later save is refused the same way, and shows this again,
        // until the admin reloads. It stays longer than the default toast, to
        // leave time to read it.
        if (
          isSettingsSaveConflict(error) &&
          mounted.current &&
          generation === draftGeneration.current &&
          dirtyRef.current.size > 0
        ) {
          conflictShown.current = true;
          const closed = () => {
            conflictShown.current = false;
          };
          toast.error(t("web.admin.settings.hooks.settings.conflict.title"), {
            id: conflictToastId,
            description: t("web.admin.settings.hooks.settings.conflict.description"),
            duration: 10_000,
            action: {
              label: t("web.admin.settings.hooks.settings.conflict.reload"),
              onClick: (event) => {
                // Stay up while the reload runs, so a failed one can be retried.
                event.preventDefault();
                void reloadAfterConflict();
              },
            },
            onDismiss: closed,
            onAutoClose: closed,
          });
        }
        throw error;
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
      for (const key of submittedKeys) {
        // A later edit of a key this save wrote began over the written value,
        // which secrets only show as set.
        if (settledKeys.includes(key)) editedOver.current.delete(key);
        else
          editedOver.current.set(key, {
            value: result.values[key],
            secretSet: !Object.hasOwn(result.values, key),
          });
      }
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
    [conflictToastId, dirty, localValues, reloadAfterConflict, settings, updateSettings],
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
    editedOver.current.clear();
    draftGeneration.current++;
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

/**
 * The validator a guarded save over `values` would send, or undefined when the
 * record has none or belongs to a profile that is no longer acting.
 */
function validatorOf(values: SettingsValues | undefined) {
  try {
    return captureSettingsBaseline(values).etag;
  } catch {
    return undefined;
  }
}
