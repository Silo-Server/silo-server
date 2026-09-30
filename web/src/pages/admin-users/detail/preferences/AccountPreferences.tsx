import { useMemo, useState, type ReactNode } from "react";
import { RotateCcw } from "lucide-react";

import { RegistrySettingControl } from "@/components/settings/RegistrySettingControl";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useAdminUserSettings,
  useDeleteAdminUserSetting,
  useUpdateAdminUserSetting,
  type AdminSettingIdentity,
  type AdminUserSettingEntry,
} from "@/hooks/queries/admin/users";
import {
  formatSettingValue,
  getSettingDefinition,
  isStructuredSetting,
} from "@/lib/settingsDisplay";

import { DetailCard } from "../ui";
import {
  PREFERENCE_SCOPE_OPTIONS,
  filterPreferences,
  groupPreferences,
  type PreferenceScopeFilter,
} from "./preferenceGroups";
import { SettingJsonDialog } from "./SettingJsonDialog";

/**
 * A key can be stored at several scopes (or for several profiles, libraries
 * or series) at once, so a row is identified by its full identity, and every
 * mutation addresses exactly that row.
 */
function identityOf(entry: AdminUserSettingEntry): AdminSettingIdentity {
  return {
    scope: entry.scope,
    profileId: entry.profile_id,
    clientFamily: entry.client_family,
    libraryId: entry.library_id,
    seriesId: entry.series_id,
  };
}

function rowKeyOf(entry: AdminUserSettingEntry): string {
  return [
    entry.key,
    entry.scope,
    entry.profile_id ?? "",
    entry.client_family ?? "",
    entry.library_id ?? "",
    entry.series_id ?? "",
  ].join(":");
}

/** Stored settings for the account and its profiles, grouped by where they apply. */
export function AccountPreferences({ userId, tabs }: { userId: number; tabs: ReactNode }) {
  const { data: settings = [], isLoading, isError } = useAdminUserSettings(userId);
  const profiles = useAdminUserProfiles(userId);
  const libraries = useAdminLibraries();
  const updateSetting = useUpdateAdminUserSetting();
  const deleteSetting = useDeleteAdminUserSetting();
  const [query, setQuery] = useState("");
  const [scope, setScope] = useState<PreferenceScopeFilter>("all");
  // Object-valued settings (pinned sidebar items, per-library overlays, the
  // custom theme) have no inline widget, so they open the raw JSON editor —
  // the same treatment the per-device view gives them.
  const [jsonEditor, setJsonEditor] = useState<{
    entry: AdminUserSettingEntry;
    identity: AdminSettingIdentity;
  } | null>(null);
  const [jsonValue, setJsonValue] = useState("");
  const closeJsonEditor = () => {
    setJsonEditor(null);
    setJsonValue("");
  };

  const groups = useMemo(
    () =>
      groupPreferences(settings, {
        profiles: new Map((profiles.data ?? []).map((p) => [p.id, p.name])),
        libraries: new Map((libraries.data ?? []).map((l) => [l.id, l.name])),
      }),
    [settings, profiles.data, libraries.data],
  );
  const visible = useMemo(() => filterPreferences(groups, query, scope), [groups, query, scope]);
  const busy = updateSetting.isPending || deleteSetting.isPending;

  const renderRow = (entry: AdminUserSettingEntry) => {
    const definition = getSettingDefinition(entry.key);
    const label = definition?.label ?? entry.key;
    const description = definition?.description ?? "Stored preference.";
    // A definition this build does not know, an object-valued one, or one the
    // manifest marks as panel-edited has no inline control that could render
    // its value truthfully. Without this the select branch would render a
    // structured value as a one-entry "Unset" dropdown whose only option
    // destroys it.
    const isJsonOnly = !definition || isStructuredSetting(definition);
    const identity = identityOf(entry);
    return (
      <div
        key={rowKeyOf(entry)}
        data-setting-row={rowKeyOf(entry)}
        className="border-border/50 grid gap-3 border-t px-4 py-3.5 first:border-t-0 sm:px-5 md:grid-cols-[minmax(0,1fr)_auto] md:items-center"
      >
        <div className="min-w-0 space-y-0.5">
          <div className="text-sm font-medium">{label}</div>
          <p className="text-muted-foreground text-[13px] leading-relaxed">{description}</p>
          {isJsonOnly && (
            <p className="text-muted-foreground/80 truncate font-mono text-[11.5px]">
              {formatSettingValue(entry.key, entry.value)}
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2 md:justify-end">
          {definition && !isJsonOnly ? (
            <RegistrySettingControl
              definition={definition}
              value={entry.value}
              disabled={busy}
              onChange={(value) =>
                updateSetting.mutate({ userId, key: entry.key, identity, value })
              }
            />
          ) : (
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => {
                setJsonEditor({ entry, identity });
                setJsonValue(entry.value);
              }}
            >
              Edit JSON
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-xs"
            onClick={() => deleteSetting.mutate({ userId, key: entry.key, identity })}
            disabled={busy}
          >
            <RotateCcw className="mr-1 h-3 w-3" />
            Reset
          </Button>
        </div>
      </div>
    );
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {tabs}
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="search"
            aria-label="Search settings"
            placeholder="Search settings"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="h-8 w-full sm:w-60"
          />
          <Select value={scope} onValueChange={(value) => setScope(value as PreferenceScopeFilter)}>
            <SelectTrigger size="sm" className="w-40" aria-label="Scope">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PREFERENCE_SCOPE_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <p
        role="note"
        className="border-border/60 bg-muted/30 rounded-lg border px-4 py-2.5 text-[13px] leading-relaxed"
      >
        Settings this user saved in their apps. A change here reaches their apps the next time they
        sync. Reset removes the saved value so the app default applies.
      </p>

      <SettingJsonDialog
        settingKey={jsonEditor?.entry.key ?? null}
        value={jsonValue}
        description="Edit the raw value. This setting has no inline control, so saving replaces the stored value wholesale — clearing it entirely is what the Reset button does."
        saveLabel="Save value"
        onValueChange={setJsonValue}
        onCancel={closeJsonEditor}
        onSave={() => {
          if (!jsonEditor) return;
          updateSetting.mutate(
            {
              userId,
              key: jsonEditor.entry.key,
              identity: jsonEditor.identity,
              value: jsonValue,
            },
            { onSuccess: () => closeJsonEditor() },
          );
        }}
      />

      {isLoading ? (
        <p className="text-muted-foreground py-8 text-center text-sm">Loading settings...</p>
      ) : isError ? (
        <p role="alert" className="text-destructive py-8 text-center text-sm">
          Couldn&apos;t load this user&apos;s settings.
        </p>
      ) : settings.length === 0 ? (
        <p className="surface-panel text-muted-foreground rounded-2xl py-10 text-center text-sm">
          No settings are stored for this user.
        </p>
      ) : visible.length === 0 ? (
        <p className="surface-panel text-muted-foreground rounded-2xl py-10 text-center text-sm">
          No settings match.
        </p>
      ) : (
        visible.map((group) => (
          <DetailCard
            key={group.key}
            title={group.title}
            description={group.subtitle}
            actions={
              <span className="text-muted-foreground text-xs tabular-nums">
                {group.entries.length}
              </span>
            }
          >
            {group.entries.map(renderRow)}
          </DetailCard>
        ))
      )}
    </div>
  );
}
