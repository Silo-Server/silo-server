import { useMemo, useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight, Trash2 } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { PlatformTile, classifyPlatform, platformLabel } from "@/components/admin/deviceOverrides";
import { RegistrySettingControl } from "@/components/settings/RegistrySettingControl";
import { Button } from "@/components/ui/button";
import {
  useDeleteAdminUserSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserSetting,
  type AdminUserSettingEntry,
} from "@/hooks/queries/admin/users";
import { useT, type MessageKey, type Translate } from "@/i18n";
import {
  formatSettingValue,
  getSettingDefinition,
  isStructuredSetting,
} from "@/lib/settingsDisplay";

import { formatLastSeen, possessiveText } from "../format";
import { DetailCard } from "../ui";
import { AddSettingPicker } from "./AddSettingPicker";
import {
  addableSettings,
  categoryTitle,
  identityOf,
  levelPlace,
  replacedValue,
  replacesText,
  type LevelKind,
  type PreferenceLevel,
} from "./levels";
import { SettingJsonDialog } from "./SettingJsonDialog";

function levelTitle(t: Translate, level: PreferenceLevel): string {
  return level.kind === "account"
    ? level.name
    : t("web.admin.users.detail.preferences.level.title", {
        level: level.name,
        profile: level.profileName,
      });
}

function levelDescription(t: Translate, level: PreferenceLevel): string {
  const profile = level.profileName;
  switch (level.kind) {
    case "account":
      return t("web.admin.users.detail.preferences.level.descAccount");
    case "profile":
      return t("web.admin.users.detail.preferences.level.descProfile", { profile });
    case "device": {
      const meta = [
        level.device?.platform ? platformLabel(level.device.platform) : null,
        level.device?.lastSeenAt
          ? t("web.admin.users.detail.preferences.level.lastSeen", {
              when: formatLastSeen(level.device.lastSeenAt),
            })
          : null,
      ].filter(Boolean);
      return meta.length > 0
        ? possessiveText(t, "web.admin.users.detail.preferences.level.descDeviceWithMeta", {
            meta: meta.join(" · "),
            profile,
          })
        : possessiveText(t, "web.admin.users.detail.preferences.level.descDevice", { profile });
    }
    case "client":
      return possessiveText(t, "web.admin.users.detail.preferences.level.descClient", { profile });
    case "library":
      return possessiveText(t, "web.admin.users.detail.preferences.level.descLibrary", { profile });
    case "series":
      return possessiveText(t, "web.admin.users.detail.preferences.level.descSeries", { profile });
  }
}

const EMPTY_TITLE: Record<Exclude<LevelKind, "account" | "profile">, MessageKey> = {
  device: "web.admin.users.detail.preferences.empty.deviceTitle",
  client: "web.admin.users.detail.preferences.empty.clientTitle",
  library: "web.admin.users.detail.preferences.empty.libraryTitle",
  series: "web.admin.users.detail.preferences.empty.seriesTitle",
};

const ADD_LABEL: Record<LevelKind, MessageKey> = {
  account: "web.admin.users.detail.preferences.add.account",
  profile: "web.admin.users.detail.preferences.add.profile",
  device: "web.admin.users.detail.preferences.add.device",
  client: "web.admin.users.detail.preferences.add.client",
  library: "web.admin.users.detail.preferences.add.library",
  series: "web.admin.users.detail.preferences.add.series",
};

function emptyText(t: Translate, level: PreferenceLevel): { title: string; detail: string } {
  if (level.kind === "profile") {
    return {
      title: t("web.admin.users.detail.preferences.empty.profileTitle", {
        profile: level.profileName,
      }),
      detail: t("web.admin.users.detail.preferences.empty.profileDetail"),
    };
  }
  if (level.kind === "account") {
    return {
      title: t("web.admin.users.detail.preferences.empty.accountTitle"),
      detail: t("web.admin.users.detail.preferences.empty.accountDetail"),
    };
  }
  return {
    title: t(EMPTY_TITLE[level.kind]),
    detail: possessiveText(t, "web.admin.users.detail.preferences.empty.detail", {
      profile: level.profileName,
    }),
  };
}

/** One setting the selected level stores, with what it replaces. */
function SettingRow({
  entry,
  replaces,
  busy,
  onChange,
  onEditJson,
  onRemove,
}: {
  entry: AdminUserSettingEntry;
  replaces: string;
  busy: boolean;
  onChange: (value: string) => void;
  onEditJson: () => void;
  onRemove: () => void;
}) {
  const t = useT();
  const definition = getSettingDefinition(entry.key);
  const label = definition?.label ?? entry.key;
  // A key this build does not know, an object-valued one, or one the manifest
  // edits through a panel has no inline control that renders its value
  // truthfully; those go through the raw JSON editor.
  const jsonOnly = !definition || isStructuredSetting(definition);
  return (
    <div
      data-setting-row={entry.key}
      className="border-border/50 grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2 border-t px-4 py-3 first:border-t-0 sm:px-5 md:grid-cols-[auto_minmax(0,1fr)_auto]"
    >
      <Button
        variant="ghost"
        size="icon"
        className="text-muted-foreground hover:text-destructive size-7"
        aria-label={t("web.admin.users.detail.preferences.row.removeAria", { setting: label })}
        disabled={busy}
        onClick={onRemove}
      >
        <Trash2 className="h-3.5 w-3.5" />
      </Button>
      <div className="min-w-0">
        <div className="text-muted-foreground text-[11px] font-medium">
          {definition
            ? categoryTitle(definition.category)
            : t("web.admin.users.detail.preferences.row.notInVersion")}
        </div>
        <div className="text-sm font-medium break-words" title={definition?.description}>
          {label}
        </div>
        <div className="text-muted-foreground text-xs">{replaces}</div>
      </div>
      <div className="col-start-2 flex min-w-0 flex-wrap items-center gap-2 md:col-start-auto md:justify-end">
        {jsonOnly ? (
          <>
            <span className="text-muted-foreground max-w-[14rem] truncate font-mono text-xs">
              {formatSettingValue(entry.key, entry.value)}
            </span>
            {/* Without a definition the save can't keep the value's JSON type,
                so a key from a newer server is shown but not edited here. */}
            {definition ? (
              <Button variant="outline" size="sm" disabled={busy} onClick={onEditJson}>
                {t("web.admin.users.detail.preferences.row.editJson")}
              </Button>
            ) : (
              <span className="text-muted-foreground text-xs">
                {t("web.admin.users.detail.preferences.row.viewOnly")}
              </span>
            )}
          </>
        ) : (
          <RegistrySettingControl
            definition={definition}
            value={entry.value}
            disabled={busy}
            onChange={onChange}
          />
        )}
      </div>
    </div>
  );
}

/**
 * The selected level: only the settings it stores, each with what it
 * replaces, plus adding one, removing one, and clearing a device. Every write
 * addresses one row by its full identity.
 */
export function LevelSettings({
  userId,
  level,
  profileEntries,
  profileAllEntries,
}: {
  userId: number;
  level: PreferenceLevel;
  /** The owning profile's own (All devices) settings, for "Replaces Main: …". */
  profileEntries: readonly AdminUserSettingEntry[];
  /** Every scope the profile stores; see replacedValue. */
  profileAllEntries: readonly AdminUserSettingEntry[];
}) {
  const t = useT();
  const updateSetting = useUpdateAdminUserSetting();
  const deleteSetting = useDeleteAdminUserSetting();
  const clearDevice = useDeleteAllAdminUserDeviceSettingsForDevice();
  const [removing, setRemoving] = useState<AdminUserSettingEntry | null>(null);
  const [clearing, setClearing] = useState(false);
  const [jsonEditor, setJsonEditor] = useState<AdminUserSettingEntry | null>(null);
  const [jsonValue, setJsonValue] = useState("");
  const closeJsonEditor = () => {
    setJsonEditor(null);
    setJsonValue("");
  };

  const busy = updateSetting.isPending || deleteSetting.isPending || clearDevice.isPending;
  const addable = useMemo(
    () => addableSettings(level, profileEntries, profileAllEntries),
    [level, profileEntries, profileAllEntries],
  );
  const canAdd = addable.length > 0;
  const addLabel = t(ADD_LABEL[level.kind]);
  const replacedFor = (key: string) => replacedValue(level, key, profileEntries, profileAllEntries);

  const add = async (keys: string[]) => {
    // A retry after a partial failure must not overwrite what already landed.
    const stored = new Set(level.entries.map((entry) => entry.key));
    for (const key of keys.filter((k) => !stored.has(k))) {
      // Start from the value it replaces, so nothing changes until edited.
      await updateSetting.mutateAsync({
        userId,
        key,
        identity: level.identity,
        value: replacedFor(key).raw,
      });
    }
  };

  const picker = canAdd ? (
    <AddSettingPicker label={addLabel} categories={addable} disabled={busy} onAdd={add} />
  ) : null;

  const removal = removing ? replacedFor(removing.key) : null;
  const removingLabel = removing ? (getSettingDefinition(removing.key)?.label ?? removing.key) : "";
  const fromProfile = removal?.profileName != null;
  const place = levelPlace(level, t);
  const removalTitle = fromProfile
    ? possessiveText(t, "web.admin.users.detail.preferences.removal.titleProfile", {
        profile: level.profileName,
        place,
      })
    : t("web.admin.users.detail.preferences.removal.titleDefault");
  const removalValue = removal?.display || undefined;
  let removalDescription: string;
  if (fromProfile) {
    const values = { setting: removingLabel, profile: level.profileName, place };
    removalDescription = removalValue
      ? possessiveText(t, "web.admin.users.detail.preferences.removal.toProfileWithValue", {
          ...values,
          value: removalValue,
        })
      : possessiveText(t, "web.admin.users.detail.preferences.removal.toProfile", values);
  } else {
    const values = { setting: removingLabel, place };
    removalDescription = removalValue
      ? t("web.admin.users.detail.preferences.removal.toDefaultWithValue", {
          ...values,
          value: removalValue,
        })
      : t("web.admin.users.detail.preferences.removal.toDefault", values);
  }

  const device = level.kind === "device" ? level.device : undefined;
  const empty = emptyText(t, level);

  return (
    <>
      <DetailCard
        title={
          device ? (
            <span className="flex items-center gap-3">
              <PlatformTile kind={classifyPlatform(device.platform)} size="sm" />
              {levelTitle(t, level)}
            </span>
          ) : (
            levelTitle(t, level)
          )
        }
        description={levelDescription(t, level)}
        actions={
          device ? (
            <>
              <Button variant="ghost" size="sm" asChild>
                <Link to={`/admin/devices/${userId}/${encodeURIComponent(device.id)}`}>
                  {t("web.admin.users.detail.preferences.level.openDevice")}
                  <ArrowUpRight className="h-3 w-3" />
                </Link>
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={busy || level.entries.length === 0}
                onClick={() => setClearing(true)}
              >
                {t("web.admin.users.detail.preferences.level.clearDeviceSettings")}
              </Button>
            </>
          ) : undefined
        }
        footer={level.entries.length > 0 ? picker : undefined}
      >
        {level.entries.length === 0 ? (
          <div className="px-5 py-10 text-center">
            <p className="text-sm font-medium">{empty.title}</p>
            <p className="text-muted-foreground mt-1 text-[13px]">{empty.detail}</p>
            {picker ? <div className="mt-3 flex justify-center">{picker}</div> : null}
          </div>
        ) : (
          level.entries.map((entry) => (
            <SettingRow
              key={`${level.id}:${entry.key}`}
              entry={entry}
              replaces={replacesText(replacedFor(entry.key))}
              busy={busy}
              onChange={(value) =>
                updateSetting.mutate({ userId, key: entry.key, identity: identityOf(entry), value })
              }
              onEditJson={() => {
                setJsonEditor(entry);
                setJsonValue(entry.value);
              }}
              onRemove={() => setRemoving(entry)}
            />
          ))
        )}
      </DetailCard>

      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => {
          if (!open) setRemoving(null);
        }}
        title={removalTitle}
        description={removalDescription}
        confirmLabel={
          fromProfile
            ? possessiveText(t, "web.admin.users.detail.preferences.removal.confirmProfile", {
                profile: level.profileName,
              })
            : t("web.admin.users.detail.preferences.removal.confirmDefault")
        }
        onConfirm={() => {
          if (removing) {
            deleteSetting.mutate({ userId, key: removing.key, identity: identityOf(removing) });
          }
          setRemoving(null);
        }}
      />
      {device && level.profileId !== undefined ? (
        <ConfirmDialog
          open={clearing}
          onOpenChange={setClearing}
          title={possessiveText(t, "web.admin.users.detail.preferences.clear.title", {
            profile: level.profileName,
            device: level.name,
          })}
          description={possessiveText(t, "web.admin.users.detail.preferences.clear.description", {
            profile: level.profileName,
          })}
          confirmLabel={t("web.admin.users.detail.preferences.clear.confirm")}
          variant="destructive"
          onConfirm={() => {
            clearDevice.mutate({
              userId,
              profileId: level.profileId ?? "",
              deviceId: device.id,
              keys: level.entries.map((entry) => entry.key),
            });
            setClearing(false);
          }}
        />
      ) : null}
      <SettingJsonDialog
        settingKey={jsonEditor?.key ?? null}
        value={jsonValue}
        description={t("web.admin.users.detail.preferences.json.description")}
        saveLabel={t("web.admin.users.detail.preferences.json.save")}
        onValueChange={setJsonValue}
        onCancel={closeJsonEditor}
        onSave={() => {
          if (!jsonEditor) return;
          updateSetting.mutate(
            { userId, key: jsonEditor.key, identity: identityOf(jsonEditor), value: jsonValue },
            { onSuccess: closeJsonEditor },
          );
        }}
      />
    </>
  );
}
