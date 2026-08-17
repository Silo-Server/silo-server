import { useEffect, useMemo, useState } from "react";
import { ChevronLeft, Info, ShieldCheck, Trash2, Users } from "lucide-react";
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
import { SubtitleAppearancePanelView } from "@/components/settings/SubtitleAppearancePanelView";
import { useClearDeviceSettings, useForgetDevice, useMyDevices } from "@/hooks/queries/devices";
import {
  settingsCapabilitiesSupportKey,
  useSettingsCapabilities,
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
} from "@/hooks/queries/settingValues";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { deviceSettingKeysForRevision } from "@/lib/settingsDisplay";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { parseSubtitleAppearance, type SubtitleAppearance } from "@/lib/subtitleAppearance";
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
  /**
   * On a phone the list and the settings are two screens, not two panes.
   *
   * Stacking them vertically put a device list over a thousand pixels tall
   * above the first setting, so reaching "turn HDR off" meant scrolling past
   * every other device first. Below xl the list hands off to the detail view
   * and a back control returns; from xl up both are visible and this is
   * ignored.
   */
  const [showDetailOnMobile, setShowDetailOnMobile] = useState(false);
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
      {/* Hidden below xl while the detail view is open: on a phone that screen
          is about one device, and its own header says which. */}
      <header className={cn("space-y-2", showDetailOnMobile && "hidden xl:block")}>
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Your devices</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Every phone, tablet, TV and browser you watch on. Pick one to see what&apos;s set
          differently there and change it — from here, whichever device you&apos;re holding.
        </p>
      </header>

      {canSeeHousehold ? (
        <div className={cn(showDetailOnMobile && "hidden xl:block")}>
          <HouseholdSwitch
            household={household}
            onChange={(next) => {
              setHousehold(next);
              if (!next) setProfileFilter(null);
            }}
            count={devices.length}
          />
        </div>
      ) : null}

      <div className="grid gap-5 xl:grid-cols-[minmax(230px,270px)_minmax(0,1fr)] xl:items-start">
        {isLoading ? (
          <Skeleton
            className={cn("h-64 rounded-[1.5rem]", showDetailOnMobile && "hidden xl:block")}
          />
        ) : (
          <div className={cn(showDetailOnMobile && "hidden xl:block")}>
            <DeviceList
              devices={devices}
              selectedDeviceId={selected?.device_id ?? null}
              onSelect={(device) => {
                setSelectedId(device.device_id);
                setShowDetailOnMobile(true);
                // Swapping the panes leaves the scroll position where the list
                // was, which on a phone lands mid-settings with no context.
                if (window.matchMedia("(max-width: 1279px)").matches) {
                  window.scrollTo({ top: 0, behavior: "instant" });
                }
              }}
              search={search}
              onSearchChange={setSearch}
              groupByProfile={household && canSeeHousehold}
              profileFilter={profileFilter}
              onProfileFilterChange={setProfileFilter}
              ownProfileId={profile?.id}
              now={now}
            />
          </div>
        )}

        {isLoading ? (
          <Skeleton
            className={cn("h-96 rounded-[1.5rem]", !showDetailOnMobile && "hidden xl:block")}
          />
        ) : selected ? (
          <div className={cn(!showDetailOnMobile && "hidden xl:block")}>
            <DeviceDetail
              key={`${selected.profile_id}:${selected.device_id}`}
              device={selected}
              actingProfileId={profile?.id ?? ""}
              now={now}
              onBack={() => {
                setShowDetailOnMobile(false);
                window.scrollTo({ top: 0, behavior: "instant" });
              }}
            />
          </div>
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
        "inline-flex min-h-11 items-center gap-1.5 rounded-lg px-4 text-sm font-medium transition-colors sm:min-h-9 sm:px-3 sm:text-[13px]",
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
  onBack,
}: {
  device: UserDevice;
  actingProfileId: string;
  now: number;
  /** Returns to the list below xl, where the two are separate screens. */
  onBack: () => void;
}) {
  // Acting for someone else changes the copy throughout: the banner, the reset
  // labels, and every mutation's identity.
  const forSomeoneElse = Boolean(device.profile_id) && device.profile_id !== actingProfileId;
  const ownerLabel = forSomeoneElse ? `${device.profile_name}'s` : "your";
  const targetProfileId = forSomeoneElse ? device.profile_id : undefined;

  const capabilities = useSettingsCapabilities();
  const supportedKeys = useMemo(
    () =>
      deviceSettingKeysForRevision(capabilities.data?.revision).filter((key) =>
        settingsCapabilitiesSupportKey(capabilities.data, key),
      ),
    [capabilities.data],
  );
  const canUseDeviceSettings = supportedKeys.length > 0;

  const { data: settings = {}, isLoading: settingsLoading } = useEffectiveSettings({
    keys: supportedKeys,
    deviceId: device.device_id,
    profileId: targetProfileId,
    // An empty key list means "all keys" to the API, so wait until the
    // connected server confirms the complete settings capability contract.
    enabled: canUseDeviceSettings,
  });
  const isLoading = capabilities.isLoading || settingsLoading;
  const capabilitiesUnavailable = !capabilities.isLoading && !canUseDeviceSettings;

  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const clearDevice = useClearDeviceSettings();
  const forgetDevice = useForgetDevice();

  const identity = {
    scope: "profile_device" as const,
    deviceId: device.device_id,
    profileId: targetProfileId,
  };

  // The subtitle appearance panel edits its object locally so dragging a
  // colour or opacity feels instant, persisting each patch as it lands. The
  // server value reasserts itself whenever the panel is (re)opened.
  const [appearanceOpen, setAppearanceOpen] = useState(false);
  const [appearance, setAppearance] = useState<SubtitleAppearance | null>(null);
  const appearanceSetting = settings[SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE];
  const appearanceChangedHere = appearanceSetting?.scope === "profile_device";

  const changedCount = device.changed_count;
  const kind = classifyPlatform(device.device_platform);

  return (
    <div className="space-y-4">
      <button
        type="button"
        onClick={onBack}
        className="text-muted-foreground hover:text-foreground -ml-1 inline-flex min-h-11 items-center gap-1 text-sm font-medium xl:hidden"
      >
        <ChevronLeft className="h-4 w-4" />
        All devices
      </button>

      <section className="surface-panel rounded-[1.5rem] border-0 px-4 py-4 shadow-none sm:px-5">
        <div className="flex items-start gap-3">
          <span className="bg-surface-raised flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl">
            <PlatformIcon kind={kind} className="h-5 w-5" />
          </span>
          <div className="min-w-0 flex-1">
            {/* Wraps rather than truncating: on a phone the name is the whole
                subject of the screen, and "Living Room Apple…" is not it. */}
            <h3 className="text-lg leading-tight font-semibold tracking-tight">
              {device.device_name || "Unknown device"}
            </h3>
            <p className="text-muted-foreground mt-0.5 text-[13px] leading-snug">
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
        <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:flex-wrap">
          {changedCount > 0 ? (
            <Button
              variant="outline"
              className="min-h-11 w-full sm:h-8 sm:min-h-0 sm:w-auto sm:px-3 sm:text-sm"
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
              className="text-muted-foreground hover:text-destructive min-h-11 self-start px-0 text-[13px] sm:h-8 sm:min-h-0 sm:px-3 sm:text-sm"
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

      {/* The scope sentence is required copy and must not be paraphrased away,
          but five lines of prose is a wall on a phone. The mandated clause
          leads; the elaboration is there for anyone who wants it. */}
      <Callout tone="info" icon={<Info className="h-4 w-4" />}>
        <span className="block">
          These apply to{" "}
          <strong className="text-foreground font-semibold">
            this device, for {forSomeoneElse ? `${device.profile_name}'s` : "your"} profile only
          </strong>
          .
        </span>
        <span className="text-muted-foreground/90 mt-1 block text-[12.5px]">
          {forSomeoneElse ? `${device.profile_name}'s` : "Your"} other devices, and anyone else who
          uses this one, are unaffected.
          {!device.is_current_device
            ? " This device picks up your changes the next time it's used."
            : ""}
        </span>
      </Callout>

      {capabilitiesUnavailable ? (
        <div
          role="alert"
          className="border-border/70 bg-surface space-y-3 rounded-[1.7rem] border p-5"
        >
          <div>
            <p className="text-sm font-semibold">Couldn&apos;t check settings compatibility</p>
            <p className="text-muted-foreground mt-1 text-[13px] leading-relaxed">
              Device controls stay unavailable until Silo confirms which settings this server
              supports.
            </p>
          </div>
          <Button
            type="button"
            variant="outline"
            disabled={capabilities.isFetching}
            onClick={() => void capabilities.refetch()}
          >
            {capabilities.isFetching ? "Checking…" : "Retry compatibility check"}
          </Button>
        </div>
      ) : isLoading ? (
        <Skeleton className="h-96 rounded-[1.7rem]" />
      ) : (
        <DeviceSettingGroups
          settings={settings}
          keys={supportedKeys}
          ownerLabel={ownerLabel}
          devicePlatform={device.device_platform}
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
          onOpenPanel={() => {
            setAppearance(parseSubtitleAppearance(appearanceSetting?.value));
            setAppearanceOpen(true);
          }}
        />
      )}

      <SubtitleAppearancePanelView
        open={appearanceOpen && appearance !== null}
        value={appearance ?? parseSubtitleAppearance(undefined)}
        onChange={(patch) => {
          const next = { ...(appearance ?? parseSubtitleAppearance(undefined)), ...patch };
          setAppearance(next);
          setValue.mutate(
            { key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE, value: next, identity },
            {
              onError: (error) =>
                toast.error(error instanceof Error ? error.message : "Couldn't save"),
            },
          );
        }}
        onClose={() => setAppearanceOpen(false)}
        canReset={appearanceChangedHere}
        onReset={() => {
          clearValue.mutate(
            { key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE, identity },
            {
              onError: (error) =>
                toast.error(error instanceof Error ? error.message : "Couldn't reset"),
            },
          );
          setAppearanceOpen(false);
        }}
        resetLabel={`Use ${ownerLabel} style`}
        eyebrow={device.device_name || "This device"}
        status={setValue.isPending ? "Saving…" : "Changes saved automatically"}
      />

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
