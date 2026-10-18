import { useState } from "react";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AutoscanSettings as AutoscanSettingsValue } from "@/api/types";
import { SaveBar } from "@/components/SaveBar";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { useAutoscanSettings, useUpdateAutoscanSettings } from "@/hooks/queries/useAutoscan";
import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";
import ConnectionsPanel from "@/pages/admin/autoscan/ConnectionsPanel";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";

type PollingDefaults = Pick<
  AutoscanSettingsValue,
  "default_poll_interval_seconds" | "debounce_seconds"
>;

/**
 * Autoscan › Settings: the polling defaults and the saved server connections.
 * Turning Autoscan on and running it stay on Sources, beside the sources they
 * act on. These are not `server_settings` rows, so the page keeps its own
 * draft, but it saves through the same save bar and unsaved-changes prompt.
 */
export default function AutoscanSettings() {
  const settings = useAutoscanSettings();
  const readAuthority = captureProfileRequestContext();
  const updateSettings = useUpdateAutoscanSettings();

  // A draft belongs to the profile that started it; switching profile drops it
  // rather than saving one profile's edit under another's authority.
  const [draft, setDraft] = useState<{
    value: PollingDefaults;
    authority: ProfileRequestContextSnapshot;
  } | null>(null);
  const activeDraft = draft && isCapturedProfileAuthorityActive(draft.authority) ? draft : null;
  const saved = settings.data;
  const effective = activeDraft?.value ?? saved;

  const isDirty = (key: keyof PollingDefaults) =>
    !!activeDraft && !!saved && activeDraft.value[key] !== saved[key];
  const intervalDirty = isDirty("default_poll_interval_seconds");
  const debounceDirty = isDirty("debounce_seconds");
  const dirtyCount = Number(intervalDirty) + Number(debounceDirty);
  useReportUnsavedChanges(dirtyCount > 0);

  function edit(delta: Partial<PollingDefaults>) {
    const authority = captureProfileRequestContext();
    if (
      !authority ||
      !effective ||
      !readAuthority ||
      !isCapturedProfileAuthorityActive(readAuthority)
    )
      return;
    setDraft({
      value: {
        default_poll_interval_seconds: effective.default_poll_interval_seconds,
        debounce_seconds: effective.debounce_seconds,
        ...delta,
      },
      authority,
    });
  }

  function save() {
    // A failed refetch hides the fields behind "Reload before editing", so the
    // draft can no longer be seen and must not be sent.
    if (
      !activeDraft ||
      !saved ||
      settings.isError ||
      updateSettings.isPending ||
      !readAuthority ||
      !isCapturedProfileAuthorityActive(readAuthority)
    )
      return;
    const captured = draft;
    // Only the two defaults are this page's; the on/off switch is on Sources
    // and may have changed since the draft started.
    updateSettings.mutate(
      { ...saved, ...activeDraft.value },
      { onSuccess: () => setDraft((current) => (current === captured ? null : current)) },
    );
  }

  if (settings.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <span className="sr-only">Loading settings</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Autoscan" />

      <div className="flex-1 space-y-5">
        {!effective || settings.isError ? (
          <p role="alert" className="text-sm">
            Autoscan settings could not be loaded. Reload before editing.
          </p>
        ) : (
          <FieldGroup
            label="Polling defaults"
            description="Used by sources that don't set their own."
            dirty={dirtyCount > 0}
          >
            <SettingField
              label="Default check interval"
              type="number"
              unit="seconds"
              dirty={intervalDirty}
              value={String(effective.default_poll_interval_seconds)}
              onChange={(value) => edit({ default_poll_interval_seconds: Number(value) || 300 })}
            />
            <SettingField
              label="Debounce"
              type="number"
              unit="seconds"
              description="Skips repeat reports of an unchanged file within this window. Changed or deleted files still scan."
              dirty={debounceDirty}
              value={String(effective.debounce_seconds)}
              onChange={(value) => edit({ debounce_seconds: Number(value) || 0 })}
            />
          </FieldGroup>
        )}

        <ConnectionsPanel />
      </div>

      <SaveBar
        dirtyCount={dirtyCount}
        onSave={save}
        onDiscard={() => setDraft(null)}
        isSaving={updateSettings.isPending}
      />
    </div>
  );
}
