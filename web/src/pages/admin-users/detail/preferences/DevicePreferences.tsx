import { useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { ArrowUpRight, RotateCcw } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import {
  DeviceOverrideRow,
  PlatformTile,
  UNKNOWN_PROFILE_ID,
  classifyPlatform,
  formatRelative,
  platformLabel,
  shortenId,
} from "@/components/admin/deviceOverrides";
import { Button } from "@/components/ui/button";
import {
  useAdminUserDeviceSettings,
  useDeleteAdminUserDeviceSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserDeviceSetting,
  type AdminDeviceSetting,
} from "@/hooks/queries/admin/users";

import { DetailCard, SubTabs } from "../ui";
import { SettingJsonDialog } from "./SettingJsonDialog";

interface DeviceProfileGroup {
  profileId: string;
  profileName: string;
  entries: AdminDeviceSetting[];
}

interface DeviceGroup {
  deviceId: string;
  profiles: DeviceProfileGroup[];
}

interface DeviceReset {
  deviceId: string;
  deviceName: string;
  profileId: string;
  profileName: string;
  keys: string[];
}

function plural(n: number, one: string, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

function DeviceCard({
  userId,
  device,
  updatePending,
  resetPending,
  onChange,
  onEditJson,
  onResetSetting,
  onResetProfile,
}: {
  userId: number;
  device: DeviceGroup;
  updatePending: boolean;
  resetPending: boolean;
  onChange: (setting: AdminDeviceSetting, value: string) => void;
  onEditJson: (setting: AdminDeviceSetting) => void;
  onResetSetting: (setting: AdminDeviceSetting) => void;
  onResetProfile: (reset: DeviceReset) => void;
}) {
  const [activeId, setActiveId] = useState<string | null>(null);
  const active =
    device.profiles.find((profile) => profile.profileId === activeId) ?? device.profiles[0];
  const all = device.profiles.flatMap((profile) => profile.entries);
  const first = all[0];
  if (!active || !first) return null;
  const lastUpdated = all
    .map((entry) => entry.updated_at)
    .filter(Boolean)
    .sort((a, b) => b.localeCompare(a))[0];
  const deviceName = first.device_name || "Unnamed device";

  return (
    <DetailCard
      title={
        <span className="flex items-center gap-3">
          <PlatformTile kind={classifyPlatform(first.device_platform)} />
          {deviceName}
        </span>
      }
      description={
        <span className="block pl-12">
          <span className="text-foreground/80 font-mono">{shortenId(device.deviceId, 8)}</span>
          {" · "}
          {platformLabel(first.device_platform)}
          {lastUpdated ? ` · updated ${formatRelative(lastUpdated)}` : ""}
        </span>
      }
      actions={
        <>
          <Button
            variant="ghost"
            size="sm"
            disabled={resetPending || active.entries.length === 0}
            onClick={() =>
              onResetProfile({
                deviceId: device.deviceId,
                deviceName,
                profileId: active.profileId,
                profileName: active.profileName,
                keys: active.entries.map((entry) => entry.key),
              })
            }
          >
            <RotateCcw className="h-3 w-3" />
            Reset {active.profileName} on this device
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to={`/admin/devices/${userId}/${encodeURIComponent(device.deviceId)}`}>
              Open device
              <ArrowUpRight className="h-3 w-3" />
            </Link>
          </Button>
        </>
      }
    >
      <div className="px-4 pt-3 sm:px-5">
        <SubTabs
          ariaLabel={`Profiles on ${deviceName}`}
          value={active.profileId}
          onValueChange={setActiveId}
          items={device.profiles.map((profile) => ({
            value: profile.profileId,
            label: profile.profileName,
          }))}
        />
      </div>
      <div className="divide-border/40 divide-y px-4 sm:px-5">
        {active.entries.map((setting) => (
          <DeviceOverrideRow
            key={`${setting.profile_id}:${setting.device_id}:${setting.key}`}
            setting={setting}
            isOverride
            disabled={updatePending}
            onChange={onChange}
            onEditJson={onEditJson}
            onReset={onResetSetting}
          />
        ))}
      </div>
    </DetailCard>
  );
}

/** Settings saved per device, one card per device with a tab per profile. */
export function DevicePreferences({ userId, tabs }: { userId: number; tabs: ReactNode }) {
  const { data: settings = [], isLoading, isError } = useAdminUserDeviceSettings(userId);
  const updateSetting = useUpdateAdminUserDeviceSetting();
  const deleteSetting = useDeleteAdminUserDeviceSetting();
  const deleteDevice = useDeleteAllAdminUserDeviceSettingsForDevice();
  const [deviceToReset, setDeviceToReset] = useState<DeviceReset | null>(null);
  const [settingToReset, setSettingToReset] = useState<AdminDeviceSetting | null>(null);
  const [jsonEditor, setJsonEditor] = useState<AdminDeviceSetting | null>(null);
  const [jsonValue, setJsonValue] = useState("");
  const closeJsonEditor = () => {
    setJsonEditor(null);
    setJsonValue("");
  };

  const profileList = useAdminUserProfiles(userId).data;

  const devices = useMemo<DeviceGroup[]>(() => {
    // Profile tabs follow the account's profile order (primary first); a
    // profile no longer listed goes last.
    const order = (profileList ?? []).map((profile) => profile.id);
    const rank = (id: string) => {
      const index = order.indexOf(id);
      return index === -1 ? order.length : index;
    };
    const grouped = new Map<string, Map<string, AdminDeviceSetting[]>>();
    for (const entry of settings) {
      const profileKey = entry.profile_id || UNKNOWN_PROFILE_ID;
      let deviceGroup = grouped.get(entry.device_id);
      if (!deviceGroup) {
        deviceGroup = new Map();
        grouped.set(entry.device_id, deviceGroup);
      }
      const list = deviceGroup.get(profileKey);
      if (list) list.push(entry);
      else deviceGroup.set(profileKey, [entry]);
    }
    return Array.from(grouped, ([deviceId, profileMap]) => ({
      deviceId,
      profiles: Array.from(profileMap, ([profileId, entries]) => ({
        profileId,
        profileName: entries[0]?.profile_name || profileId,
        entries,
      })).sort((a, b) => rank(a.profileId) - rank(b.profileId)),
    }));
  }, [settings, profileList]);

  const totalProfiles = new Set(settings.map((s) => s.profile_id || UNKNOWN_PROFILE_ID)).size;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {tabs}
        {devices.length > 0 && (
          <span className="text-muted-foreground text-[13px] tabular-nums">
            {plural(devices.length, "device")} · {plural(settings.length, "saved setting")} ·{" "}
            {plural(totalProfiles, "profile")}
          </span>
        )}
      </div>

      <ConfirmDialog
        open={deviceToReset !== null}
        onOpenChange={(open) => {
          if (!open) setDeviceToReset(null);
        }}
        title={
          deviceToReset
            ? `Reset ${deviceToReset.profileName} on ${deviceToReset.deviceName}?`
            : "Reset this device?"
        }
        description={`Every setting ${deviceToReset?.profileName ?? "this profile"} saved on this device is cleared. Playback falls back to account or default values.`}
        confirmLabel="Reset"
        variant="destructive"
        onConfirm={() => {
          if (deviceToReset) {
            deleteDevice.mutate({
              userId,
              profileId: deviceToReset.profileId,
              deviceId: deviceToReset.deviceId,
              keys: deviceToReset.keys,
            });
          }
          setDeviceToReset(null);
        }}
      />
      <ConfirmDialog
        open={settingToReset !== null}
        onOpenChange={(open) => {
          if (!open) setSettingToReset(null);
        }}
        title="Reset this override?"
        description="The override will be removed and the device will fall back to the profile default."
        confirmLabel="Reset override"
        variant="destructive"
        onConfirm={() => {
          if (settingToReset) {
            deleteSetting.mutate({
              userId,
              profileId: settingToReset.profile_id,
              deviceId: settingToReset.device_id,
              key: settingToReset.key,
            });
          }
          setSettingToReset(null);
        }}
      />
      <SettingJsonDialog
        settingKey={jsonEditor?.key ?? null}
        value={jsonValue}
        description="Edit the raw value. Invalid JSON is saved as-is and may cause clients to fall back to defaults."
        saveLabel="Save override"
        onValueChange={setJsonValue}
        onCancel={closeJsonEditor}
        onSave={() => {
          if (!jsonEditor) return;
          updateSetting.mutate(
            {
              userId,
              profileId: jsonEditor.profile_id,
              deviceId: jsonEditor.device_id,
              key: jsonEditor.key,
              value: jsonValue,
            },
            { onSuccess: closeJsonEditor },
          );
        }}
      />

      {isLoading ? (
        <p className="text-muted-foreground py-8 text-center text-sm">
          Loading device overrides...
        </p>
      ) : isError ? (
        <p role="alert" className="text-destructive py-8 text-center text-sm">
          Couldn&apos;t load this user&apos;s device settings.
        </p>
      ) : devices.length === 0 ? (
        <div className="surface-panel rounded-2xl border-0 px-6 py-12 text-center">
          <p className="text-foreground text-sm font-medium">No device overrides</p>
          <p className="text-muted-foreground mx-auto mt-1 max-w-sm text-[12.5px] leading-relaxed">
            Overrides appear here as soon as this user tunes a per-device playback setting.
          </p>
        </div>
      ) : (
        devices.map((device) => (
          <DeviceCard
            key={device.deviceId}
            userId={userId}
            device={device}
            updatePending={updateSetting.isPending}
            resetPending={deleteDevice.isPending}
            onChange={(setting, value) =>
              updateSetting.mutate({
                userId,
                profileId: setting.profile_id,
                deviceId: setting.device_id,
                key: setting.key,
                value,
              })
            }
            onEditJson={(setting) => {
              setJsonEditor(setting);
              setJsonValue(setting.value);
            }}
            onResetSetting={setSettingToReset}
            onResetProfile={setDeviceToReset}
          />
        ))
      )}
    </div>
  );
}
