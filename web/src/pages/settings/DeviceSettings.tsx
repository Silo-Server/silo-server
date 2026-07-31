import { useEffect, useMemo, useState } from "react";
import { Info, ShieldCheck, Trash2, Users } from "lucide-react";
import { toast } from "sonner";

import type { UserDevice } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  classifyPlatform,
  PlatformIcon,
  platformKindLabel,
} from "@/components/admin/deviceOverrides";
import { DeviceList, lastSeenLabel } from "@/components/settings/DeviceList";
import { DeviceSettingGroups } from "@/components/settings/DeviceSettingGroups";
import { useClearDeviceSettings, useForgetDevice, useMyDevices } from "@/hooks/queries/devices";
import {
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
} from "@/hooks/queries/settingValues";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { ALL_DEVICE_SETTING_KEYS } from "@/lib/settingsDisplay";
import type { SettingKey } from "@/lib/settingsContract";
import { cn } from "@/lib/utils";

/**
 * "Your devices" — see and change how Silo behaves on each device you watch on.
 *
 * Two things make this screen different from the admin device console. Every
 * device is editable from wherever you are, because the settings API accepts an
 * explicit device rather than only the one in the request headers. And the
 * household parent can switch to the whole household, so a parent can fix a
 * kid's iPad without borrowing it.
 */
export default function DeviceSettings() {
  const actingAdmin = useIsActingAdmin();
  const { profile } = useCurrentProfile();
  const canSeeHousehold = actingAdmin || profile?.is_primary === true;

  const [household, setHousehold] = useState(false);
  const [search, setSearch] = useState("");
  const [profileFilter, setProfileFilter] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // One clock for the whole screen, taken once per mount: relative labels only
  // need to be right to the minute, and reading the clock during render is not
  // something a pure component may do.
  const [now] = useState(() => Date.now());

  const { data: devices = [], isLoading } = useMyDevices({
    household: household && canSeeHousehold,
  });

  // Filtering to one person must not leave someone else's device open in the
  // detail pane — the list and the pane would then disagree about who is being
  // edited, which is the one thing this screen cannot afford to be vague about.
  const selectable = useMemo(
    () =>
      profileFilter ? devices.filter((device) => device.profile_id === profileFilter) : devices,
    [devices, profileFilter],
  );

  // Default to the device you are on: it is the one you can check the effect of
  // immediately, and the one most people came here for.
  const selected = useMemo(() => {
    if (selectable.length === 0) return null;
    return selectable.find((device) => device.device_id === selectedId) ?? selectable[0];
  }, [selectable, selectedId]);

  useEffect(() => {
    if (selected && selected.device_id !== selectedId) {
      setSelectedId(selected.device_id);
    }
  }, [selected, selectedId]);

  return (
    <div className="space-y-5">
      <header className="space-y-2">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Your devices</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Every phone, tablet, TV and browser you watch on. Pick one to see what&apos;s set
          differently there and change it — from here, whichever device you&apos;re holding.
        </p>
      </header>

      {canSeeHousehold ? (
        <HouseholdSwitch
          household={household}
          onChange={(next) => {
            setHousehold(next);
            if (!next) setProfileFilter(null);
          }}
          count={devices.length}
        />
      ) : null}

      <div className="grid gap-5 xl:grid-cols-[minmax(230px,270px)_minmax(0,1fr)] xl:items-start">
        {isLoading ? (
          <Skeleton className="h-64 rounded-[1.5rem]" />
        ) : (
          <DeviceList
            devices={devices}
            selectedDeviceId={selected?.device_id ?? null}
            onSelect={(device) => setSelectedId(device.device_id)}
            search={search}
            onSearchChange={setSearch}
            groupByProfile={household && canSeeHousehold}
            profileFilter={profileFilter}
            onProfileFilterChange={setProfileFilter}
            now={now}
          />
        )}

        {isLoading ? (
          <Skeleton className="h-96 rounded-[1.5rem]" />
        ) : selected ? (
          <DeviceDetail
            key={`${selected.profile_id}:${selected.device_id}`}
            device={selected}
            actingProfileId={profile?.id ?? ""}
            now={now}
          />
        ) : (
          <p className="text-muted-foreground text-sm">
            No devices yet. They appear here as you sign in on them.
          </p>
        )}
      </div>
    </div>
  );
}

function HouseholdSwitch({
  household,
  onChange,
  count,
}: {
  household: boolean;
  onChange: (value: boolean) => void;
  count: number;
}) {
  return (
    <div
      className="border-border/60 bg-surface inline-flex gap-1 rounded-xl border p-1"
      role="group"
      aria-label="Whose devices to show"
    >
      <SwitchButton active={!household} onClick={() => onChange(false)}>
        Just mine
      </SwitchButton>
      <SwitchButton active={household} onClick={() => onChange(true)}>
        <Users className="h-3.5 w-3.5" />
        Everyone{household && count > 0 ? ` (${count})` : ""}
      </SwitchButton>
    </div>
  );
}

function SwitchButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[13px] font-medium transition-colors",
        active
          ? "bg-surface-raised text-foreground"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

function DeviceDetail({
  device,
  actingProfileId,
  now,
}: {
  device: UserDevice;
  actingProfileId: string;
  now: number;
}) {
  // Acting for someone else changes the copy throughout: the banner, the reset
  // labels, and every mutation's identity.
  const forSomeoneElse = Boolean(device.profile_id) && device.profile_id !== actingProfileId;
  const ownerLabel = forSomeoneElse ? `${device.profile_name}'s` : "your";
  const targetProfileId = forSomeoneElse ? device.profile_id : undefined;

  const { data: settings = {}, isLoading } = useEffectiveSettings({
    keys: ALL_DEVICE_SETTING_KEYS,
    deviceId: device.device_id,
    profileId: targetProfileId,
  });

  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const clearDevice = useClearDeviceSettings();
  const forgetDevice = useForgetDevice();

  const identity = {
    scope: "profile_device" as const,
    deviceId: device.device_id,
    profileId: targetProfileId,
  };

  const changedCount = device.changed_count;
  const kind = classifyPlatform(device.device_platform);

  return (
    <div className="space-y-4">
      <section className="surface-panel rounded-[1.5rem] border-0 px-5 py-4 shadow-none">
        <div className="flex flex-wrap items-start gap-3">
          <span className="bg-surface-raised flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl">
            <PlatformIcon kind={kind} className="h-5 w-5" />
          </span>
          <div className="min-w-0 flex-1">
            <h3 className="truncate text-lg font-semibold tracking-tight">
              {device.device_name || "Unknown device"}
            </h3>
            <p className="text-muted-foreground text-[13px]">
              {[
                platformKindLabel(kind),
                device.is_current_device ? "using now" : lastSeenLabel(device.last_seen_at, now),
                changedCount > 0
                  ? `${changedCount} ${changedCount === 1 ? "thing" : "things"} set differently`
                  : "nothing changed here",
              ].join(" · ")}
            </p>
          </div>
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          {changedCount > 0 ? (
            <Button
              variant="outline"
              size="sm"
              disabled={clearDevice.isPending}
              onClick={() => {
                if (
                  !window.confirm(
                    `Clear all ${changedCount} ${changedCount === 1 ? "change" : "changes"} on ${device.device_name}?`,
                  )
                ) {
                  return;
                }
                clearDevice.mutate(
                  { deviceId: device.device_id, profileId: targetProfileId },
                  {
                    onSuccess: () => toast.success("Settings cleared on this device"),
                    onError: (error) =>
                      toast.error(error instanceof Error ? error.message : "Couldn't clear"),
                  },
                );
              }}
            >
              Clear all changes
            </Button>
          ) : null}
          {!device.is_current_device ? (
            <Button
              variant="ghost"
              size="sm"
              disabled={forgetDevice.isPending}
              onClick={() => {
                if (
                  !window.confirm(
                    `Forget ${device.device_name}? Its settings are removed and it disappears from this list until it's used again.`,
                  )
                ) {
                  return;
                }
                forgetDevice.mutate(
                  { deviceId: device.device_id, profileId: targetProfileId },
                  {
                    onSuccess: () => toast.success("Device forgotten"),
                    onError: (error) =>
                      toast.error(error instanceof Error ? error.message : "Couldn't forget"),
                  },
                );
              }}
            >
              <Trash2 className="h-3.5 w-3.5" />
              Forget
            </Button>
          ) : null}
        </div>
      </section>

      {forSomeoneElse ? (
        <Callout tone="warning" icon={<Users className="h-4 w-4" />}>
          <strong className="text-foreground font-semibold">
            You&apos;re changing {device.profile_name}&apos;s settings, not your own.
          </strong>{" "}
          {device.profile_name} will see these change on this device.
        </Callout>
      ) : null}

      <Callout tone="info" icon={<Info className="h-4 w-4" />}>
        These apply to{" "}
        <strong className="text-foreground font-semibold">
          this device, for {forSomeoneElse ? `${device.profile_name}'s` : "your"} profile only
        </strong>
        . {forSomeoneElse ? `${device.profile_name}'s` : "Your"} other devices, and anyone else who
        uses this one, are unaffected.
        {!device.is_current_device
          ? " This device picks up your changes the next time it's used."
          : ""}
      </Callout>

      {isLoading ? (
        <Skeleton className="h-96 rounded-[1.7rem]" />
      ) : (
        <DeviceSettingGroups
          settings={settings}
          ownerLabel={ownerLabel}
          disabled={setValue.isPending || clearValue.isPending}
          onChange={(key: SettingKey, value: unknown) =>
            setValue.mutate(
              { key, value, identity },
              {
                onError: (error) =>
                  toast.error(error instanceof Error ? error.message : "Couldn't save"),
              },
            )
          }
          onReset={(key: SettingKey) =>
            clearValue.mutate(
              { key, identity },
              {
                onError: (error) =>
                  toast.error(error instanceof Error ? error.message : "Couldn't reset"),
              },
            )
          }
        />
      )}

      {forSomeoneElse ? (
        <Callout tone="muted" icon={<ShieldCheck className="h-4 w-4" />}>
          <strong className="text-foreground font-semibold">What you can&apos;t see here.</strong>{" "}
          This page shows how Silo is set up on each device — not what anyone watched. Viewing
          history stays private to each profile.
        </Callout>
      ) : null}
    </div>
  );
}

function Callout({
  tone,
  icon,
  children,
}: {
  tone: "info" | "warning" | "muted";
  icon: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "flex gap-2.5 rounded-2xl border px-4 py-3 text-[13px] leading-relaxed",
        tone === "info" && "border-info/25 bg-info/5 text-muted-foreground",
        tone === "warning" && "border-amber-500/30 bg-amber-500/5 text-amber-100/90",
        tone === "muted" && "border-border/60 bg-surface text-muted-foreground",
      )}
    >
      <span
        className={cn(
          "mt-px shrink-0",
          tone === "info" && "text-info",
          tone === "warning" && "text-amber-400",
          tone === "muted" && "text-muted-foreground",
        )}
      >
        {icon}
      </span>
      <p className="min-w-0">{children}</p>
    </div>
  );
}
