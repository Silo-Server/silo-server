import { useCallback, useMemo, useState } from "react";

import type { PluginInstallation } from "@/api/types";
import { t } from "@/i18n";
import {
  useSaveSignInPluginConfig,
  useUpdateSignInBinding,
  type AuthConnectionTestStaged,
} from "@/hooks/queries/admin/externalSignIn";
import {
  adminSignInErrorText,
  authBindingFieldIsManaged,
  primarySignInInstallations,
  authProviderName,
  savedAutoProvision,
  signInBindingWrite,
} from "@/lib/externalSignInAdmin";

import {
  changedFieldCount,
  savedFormValues,
  signInSetupLayout,
  stagedEntry,
  type ConfigDraft,
  type SignInConfigEntry,
  type SignInSetupLayout,
} from "./signInSetup";

interface InstallationDraft {
  config: Record<string, ConfigDraft>;
  autoProvision?: boolean;
}

type Drafts = Record<number, InstallationDraft>;

/** Values contains only the fields the administrator edited. */
function entryDraftOf(draft: InstallationDraft, entry: SignInConfigEntry): ConfigDraft {
  return draft.config[entry.schema.key] ?? { values: {}, clearSecrets: [] };
}

export type SignInDraftSaveResult = { ok: true } | { ok: false; text: string };

function isEmpty(draft: InstallationDraft): boolean {
  return Object.keys(draft.config).length === 0 && draft.autoProvision === undefined;
}

function dropManagedEdits(installation: PluginInstallation, draft: InstallationDraft) {
  let next = draft;
  if (
    draft.autoProvision !== undefined &&
    authBindingFieldIsManaged(installation, "auto_provision")
  ) {
    next = { ...next, autoProvision: undefined };
  }
  for (const [key, fields] of Object.entries(installation.env_managed?.config_fields ?? {})) {
    const entry = draft.config[key];
    if (
      !entry ||
      !fields.some(
        (field) => Object.hasOwn(entry.values, field) || entry.clearSecrets.includes(field),
      )
    )
      continue;
    const values = { ...entry.values };
    for (const field of fields) delete values[field];
    const clearSecrets = entry.clearSecrets.filter((field) => !fields.includes(field));
    const config = { ...next.config };
    if (Object.keys(values).length || clearSecrets.length) config[key] = { values, clearSecrets };
    else delete config[key];
    next = { ...next, config };
  }
  return next;
}

/**
 * Unsaved edits to the sign-in plugins' configuration and to "Create accounts
 * on first sign-in", which the Sign-in page saves with its own settings from
 * one save bar. A draft that matches what is saved again drops out.
 */
export function useSignInProviderDrafts(installations: PluginInstallation[] | undefined) {
  const [drafts, setDrafts] = useState<Drafts>({});
  const [saving, setSaving] = useState(false);
  const saveConfig = useSaveSignInPluginConfig();
  const updateBinding = useUpdateSignInBinding();

  const candidates = useMemo(() => primarySignInInstallations(installations), [installations]);
  const layouts = useMemo(
    () =>
      new Map(candidates.map((installation) => [installation.id, signInSetupLayout(installation)])),
    [candidates],
  );

  const layoutOf = useCallback(
    (installation: PluginInstallation): SignInSetupLayout =>
      layouts.get(installation.id) ?? signInSetupLayout(installation),
    [layouts],
  );

  // Discard edits as soon as the deployment claims the field. Releasing
  // ownership later must not bring back a superseded value or secret clear.
  let editable = drafts;
  for (const installation of candidates) {
    const draft = drafts[installation.id];
    if (!draft) continue;
    const next = dropManagedEdits(installation, draft);
    if (next === draft) continue;
    if (editable === drafts) editable = { ...drafts };
    if (isEmpty(next)) delete editable[installation.id];
    else editable[installation.id] = next;
  }
  if (editable !== drafts) setDrafts(editable);

  function update(
    installationId: number,
    change: (draft: InstallationDraft) => InstallationDraft,
  ): void {
    setDrafts((current) => {
      const next = change(current[installationId] ?? { config: {} });
      const copy = { ...current };
      if (isEmpty(next)) delete copy[installationId];
      else copy[installationId] = next;
      return copy;
    });
  }

  function withEntryDraft(
    draft: InstallationDraft,
    entry: SignInConfigEntry,
    entryDraft: ConfigDraft,
  ): InstallationDraft {
    const config = { ...draft.config };
    if (changedFieldCount(entry, entryDraft) === 0) delete config[entry.schema.key];
    else config[entry.schema.key] = entryDraft;
    return { ...draft, config };
  }

  /** The values a config entry shows: its draft, else what is saved. */
  function valuesOf(installation: PluginInstallation, entry: SignInConfigEntry) {
    const saved = savedFormValues(entry);
    const draft = drafts[installation.id]?.config[entry.schema.key];
    return { ...saved, ...draft?.values };
  }

  function draftOf(installation: PluginInstallation, schemaKey: string): ConfigDraft | undefined {
    return drafts[installation.id]?.config[schemaKey];
  }

  function setField(
    installation: PluginInstallation,
    entry: SignInConfigEntry,
    fieldKey: string,
    value: unknown,
  ) {
    if (entry.managedFields.includes(fieldKey)) return;
    update(installation.id, (draft) => {
      const previous = entryDraftOf(draft, entry);
      // Typing a new secret replaces the saved one, so it is no longer cleared.
      const replacing = typeof value === "string" && value.trim() !== "";
      return withEntryDraft(draft, entry, {
        values: { ...previous.values, [fieldKey]: value },
        clearSecrets: replacing
          ? previous.clearSecrets.filter((key) => key !== fieldKey)
          : previous.clearSecrets,
      });
    });
  }

  function toggleClearSecret(
    installation: PluginInstallation,
    entry: SignInConfigEntry,
    fieldKey: string,
  ) {
    if (entry.managedFields.includes(fieldKey)) return;
    update(installation.id, (draft) => {
      const previous = entryDraftOf(draft, entry);
      const clearing = previous.clearSecrets.includes(fieldKey);
      return withEntryDraft(draft, entry, {
        values: { ...previous.values, [fieldKey]: "" },
        clearSecrets: clearing
          ? previous.clearSecrets.filter((key) => key !== fieldKey)
          : [...previous.clearSecrets, fieldKey],
      });
    });
  }

  function autoProvisionOf(installation: PluginInstallation): boolean {
    const saved = savedAutoProvision(installation);
    return authBindingFieldIsManaged(installation, "auto_provision")
      ? saved
      : (drafts[installation.id]?.autoProvision ?? saved);
  }

  function setAutoProvision(installation: PluginInstallation, value: boolean) {
    if (authBindingFieldIsManaged(installation, "auto_provision")) return;
    update(installation.id, (draft) => ({
      ...draft,
      autoProvision: value === savedAutoProvision(installation) ? undefined : value,
    }));
  }

  function changeCountOf(installation: PluginInstallation): number {
    const draft = drafts[installation.id];
    if (!draft) return 0;
    const layout = layoutOf(installation);
    let count =
      draft.autoProvision !== undefined &&
      !authBindingFieldIsManaged(installation, "auto_provision")
        ? 1
        : 0;
    for (const [key, entryDraft] of Object.entries(draft.config)) {
      const entry = layout.entries.get(key);
      if (entry) count += changedFieldCount(entry, entryDraft);
    }
    return count;
  }

  const changeCount = candidates.reduce(
    (sum, installation) => sum + changeCountOf(installation),
    0,
  );

  /**
   * The edited entries as the connection test takes them. `extra` lays one
   * more field over an entry without staging it for save (the test's
   * look-up username).
   */
  function stagedForTest(
    installation: PluginInstallation,
    extra?: { schemaKey: string; fieldKey: string; value: string },
  ): AuthConnectionTestStaged[] {
    const layout = layoutOf(installation);
    const config = drafts[installation.id]?.config ?? {};
    const keys = new Set(Object.keys(config));
    if (extra) keys.add(extra.schemaKey);
    const staged: AuthConnectionTestStaged[] = [];
    for (const key of keys) {
      const entry = layout.entries.get(key);
      if (!entry) continue;
      const values = { ...valuesOf(installation, entry) };
      if (extra?.schemaKey === key) values[extra.fieldKey] = extra.value;
      staged.push({
        key,
        value: stagedEntry(entry, values),
        clear_secrets: (config[key]?.clearSecrets ?? []).filter(
          (field) => !entry.managedFields.includes(field),
        ),
      });
    }
    return staged;
  }

  /**
   * Saves every edited entry, then "Create accounts on first sign-in", one
   * write at a time. Each write that lands drops out of the drafts, so after
   * a failure only what is still unsaved stays staged.
   */
  async function save(): Promise<SignInDraftSaveResult> {
    setSaving(true);
    try {
      for (const installation of candidates) {
        const draft = drafts[installation.id];
        if (!draft) continue;
        const layout = layoutOf(installation);
        try {
          for (const [key, entryDraft] of Object.entries(draft.config)) {
            const entry = layout.entries.get(key);
            if (!entry || changedFieldCount(entry, entryDraft) === 0) continue;
            await saveConfig.mutateAsync({
              installationId: installation.id,
              key,
              value: stagedEntry(entry, valuesOf(installation, entry)),
              clearSecrets: entryDraft.clearSecrets.filter(
                (field) => !entry.managedFields.includes(field),
              ),
            });
            // An edit made while this write was in flight is a newer draft;
            // it stays staged.
            update(installation.id, (current) => {
              if (current.config[key] !== entryDraft) return current;
              const config = { ...current.config };
              delete config[key];
              return { ...current, config };
            });
          }
          if (
            draft.autoProvision !== undefined &&
            !authBindingFieldIsManaged(installation, "auto_provision")
          ) {
            await updateBinding.mutateAsync({
              installationId: installation.id,
              body: signInBindingWrite(installation, { auto_provision: draft.autoProvision }),
            });
            update(installation.id, (current) =>
              current.autoProvision === draft.autoProvision
                ? { ...current, autoProvision: undefined }
                : current,
            );
          }
        } catch (error) {
          return {
            ok: false,
            text: t("web.admin.settings.signIn.drafts.saveFailed", {
              provider: authProviderName(installation),
              error: adminSignInErrorText(
                error,
                t("web.admin.settings.signIn.drafts.saveFallback"),
              ),
            }),
          };
        }
      }
      return { ok: true };
    } finally {
      setSaving(false);
    }
  }

  return {
    layoutOf,
    valuesOf,
    draftOf,
    setField,
    toggleClearSecret,
    autoProvisionOf,
    setAutoProvision,
    changeCountOf,
    changeCount,
    stagedForTest,
    save,
    saving,
    discard: () => setDrafts({}),
  };
}

export type SignInProviderDrafts = ReturnType<typeof useSignInProviderDrafts>;
