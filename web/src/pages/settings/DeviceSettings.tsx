import { useEffect, useMemo, useState } from "react";
import { ChevronLeft, Info, Pencil, ShieldCheck, Trash2, Users } from "lucide-react";
import { toast } from "sonner";

import type { UserDevice } from "@/api/types";
import { useT } from "@/i18n";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  classifyPlatform,
  PlatformIcon,
  platformKindLabel,
} from "@/components/admin/deviceOverrides";
import { DeviceList, deviceSelectionKey, lastSeenLabel } from "@/components/settings/DeviceList";
import { DeviceSettingGroups } from "@/components/settings/DeviceSettingGroups";
import { ProfileLaunchSettingsGroup } from "@/components/settings/ProfileLaunchSettingsGroup";
import { SubtitleAppearancePanelView } from "@/components/settings/SubtitleAppearancePanelView";
import {
  deviceDisplayName,
  useClearDeviceSettings,
  useForgetDevice,
  useMyDevices,
  useRenameDevice,
  type DeviceTarget,
} from "@/hooks/queries/devices";
import {
  settingsCapabilitiesSupportKey,
  useSettingsCapabilities,
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
  useStoredSettingValues,
} from "@/hooks/queries/settingValues";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { deviceSettingKeysForRevision } from "@/lib/settingsDisplay";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
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
  const t = useT();
  const actingAdmin = useIsActingAdmin();
  const { profile } = useCurrentProfile();
  const canSeeHousehold = actingAdmin || profile?.is_primary === true;

  const [household, setHousehold] = useState(false);
  const [search, setSearch] = useState("");
  const [profileFilter, setProfileFilter] = useState<string | null>(null);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
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
  // immediately, and the one most people came here for. The list is ordered by
  // last use, so its first entry is often another browser on the same account.
  // In the household view this device can appear once per profile that used
  // it, so prefer your own row. Fall back to the first device only when the
  // one you are on is not listed (never registered, or filtered out).
  const selected = useMemo(() => {
    if (selectable.length === 0) return null;
    return (
      selectable.find((device) => deviceSelectionKey(device) === selectedKey) ??
      defaultDevice(selectable, profile?.id)
    );
  }, [selectable, selectedKey, profile?.id]);

  useEffect(() => {
    if (selected && deviceSelectionKey(selected) !== selectedKey) {
      setSelectedKey(deviceSelectionKey(selected));
    }
  }, [selected, selectedKey]);

  return (
    <div className="space-y-5">
      {/* Hidden below xl while the detail view is open: on a phone that screen
          is about one device, and its own header says which. */}
      <PageHeader
        className={cn(showDetailOnMobile && "hidden xl:flex")}
        title={t("web.settings.device.title")}
        subtitle={t("web.settings.device.subtitle")}
      />

      {/* Kept by this browser itself, not the server, so it sits apart from
          the device list, which edits server-held settings for any device. */}
      <div className={cn(showDetailOnMobile && "hidden xl:block")}>
        <ProfileLaunchSettingsGroup />
      </div>

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
              selectedKey={selected ? deviceSelectionKey(selected) : null}
              onSelect={(device) => {
                setSelectedKey(deviceSelectionKey(device));
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
          <p className="text-muted-foreground text-sm">{t("web.settings.device.empty")}</p>
        )}
      </div>
    </div>
  );
}

function defaultDevice(devices: UserDevice[], ownProfileId: string | undefined): UserDevice | null {
  const current = devices.filter((device) => device.is_current_device);
  return (
    current.find((device) => device.profile_id === ownProfileId) ?? current[0] ?? devices[0] ?? null
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
  const t = useT();
  return (
    <div
      className="border-border/60 bg-surface inline-flex gap-1 rounded-xl border p-1"
      role="group"
      aria-label={t("web.settings.device.household.label")}
    >
      <SwitchButton active={!household} onClick={() => onChange(false)}>
        {t("web.settings.device.household.mine")}
      </SwitchButton>
      <SwitchButton active={household} onClick={() => onChange(true)}>
        <Users className="h-3.5 w-3.5" />
        {household && count > 0
          ? t("web.settings.device.household.everyoneCount", { count: String(count) })
          : t("web.settings.device.household.everyone")}
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
  const t = useT();
  // Acting for someone else changes the copy throughout: the banner, the reset
  // labels, and every mutation's identity.
  const forSomeoneElse = Boolean(device.profile_id) && device.profile_id !== actingProfileId;
  // An English possessive ("Sam's"): DeviceSettingGroups builds its own
  // sentences around it, and the messages below take it as {owner}, since the
  // catalog rejects an ASCII apostrophe after a placeholder.
  const ownerLabel = forSomeoneElse ? `${device.profile_name}'s` : "your";
  const targetProfileId = forSomeoneElse ? device.profile_id : undefined;

  const capabilities = useSettingsCapabilities();
  const supportedKeys = useMemo(
    () =>
      deviceSettingKeysForRevision(capabilities.data?.manifest_revision).filter((key) =>
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

  // While a profile value outranks the device's own (ui.title_art's "apply to
  // all devices"), the effective read answers with the profile row only. The
  // device row it passes over is read here so it stays visible and resettable.
  const profileWideKeys = supportedKeys.filter(
    (key) =>
      SETTING_DEFINITIONS[key].resolutionOrder[0] === "profile" &&
      settings[key]?.source === "profile",
  );
  const { data: storedOnDevice } = useStoredSettingValues({
    keys: profileWideKeys,
    identity,
    enabled: canUseDeviceSettings,
  });

  // The subtitle appearance panel edits its object locally so dragging a
  // colour or opacity feels instant, persisting each patch as it lands. The
  // server value reasserts itself whenever the panel is (re)opened.
  const [appearanceOpen, setAppearanceOpen] = useState(false);
  const [appearance, setAppearance] = useState<SubtitleAppearance | null>(null);
  const appearanceSetting = settings[SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE];
  const appearanceChangedHere = appearanceSetting?.scope === "profile_device";

  const changedCount = device.changed_count;
  const kind = classifyPlatform(device.device_platform);
  // One name for the device everywhere on this screen, so a confirmation never
  // names "Apple TV" while the heading says "Living Room".
  const displayName = deviceDisplayName(device);
  const canRename = capabilities.data?.supports_device_rename === true;
  const [renaming, setRenaming] = useState(false);
  const scopeDetail = [
    forSomeoneElse
      ? t("web.settings.device.scopeDetailOther", { owner: ownerLabel })
      : t("web.settings.device.scopeDetailOwn"),
    device.is_current_device ? null : t("web.settings.device.scopeNextUse"),
  ]
    .filter(Boolean)
    .join(" ");
  const scopeStrong = (chunks: React.ReactNode) => (
    <strong className="text-foreground font-semibold">{chunks}</strong>
  );

  return (
    <div className="space-y-4">
      <button
        type="button"
        onClick={onBack}
        className="text-muted-foreground hover:text-foreground -ml-1 inline-flex min-h-11 items-center gap-1 text-sm font-medium xl:hidden"
      >
        <ChevronLeft className="h-4 w-4" />
        {t("web.settings.device.allDevices")}
      </button>

      <section className="surface-panel rounded-[1.5rem] border-0 px-4 py-4 shadow-none sm:px-5">
        <div className="flex items-start gap-3">
          <span className="bg-surface-raised flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl">
            <PlatformIcon kind={kind} className="h-5 w-5" />
          </span>
          <div className="min-w-0 flex-1">
            {renaming ? (
              <DeviceNameEditor
                device={device}
                target={{ deviceId: device.device_id, profileId: targetProfileId }}
                onDone={() => setRenaming(false)}
              />
            ) : (
              // Wraps rather than truncating: on a phone the name is the whole
              // subject of the screen, and "Living Room Apple…" is not it.
              <h3 className="text-lg leading-tight font-semibold tracking-tight">{displayName}</h3>
            )}
            {device.custom_name &&
            device.device_name &&
            device.custom_name !== device.device_name ? (
              // The reported name still identifies the hardware, which is what
              // someone checks before forgetting a device.
              <p className="text-muted-foreground mt-0.5 text-[13px] leading-snug">
                {t("web.settings.device.reportsItselfAs", { name: device.device_name })}
              </p>
            ) : null}
            <p className="text-muted-foreground mt-0.5 text-[13px] leading-snug">
              {[
                platformKindLabel(kind),
                device.is_current_device
                  ? t("web.settings.device.usingNow")
                  : lastSeenLabel(device.last_seen_at, now),
                changedCount > 0
                  ? t("web.settings.device.changedCount", {
                      count: changedCount,
                      count_text: String(changedCount),
                    })
                  : t("web.settings.device.nothingChanged"),
              ].join(" · ")}
            </p>
          </div>
        </div>
        <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:flex-wrap">
          {canRename && !renaming ? (
            <Button
              variant="outline"
              className="min-h-11 w-full sm:h-8 sm:min-h-0 sm:w-auto sm:px-3 sm:text-sm"
              onClick={() => setRenaming(true)}
            >
              <Pencil className="h-3.5 w-3.5" />
              {t("web.settings.device.rename")}
            </Button>
          ) : null}
          {changedCount > 0 ? (
            <Button
              variant="outline"
              className="min-h-11 w-full sm:h-8 sm:min-h-0 sm:w-auto sm:px-3 sm:text-sm"
              disabled={clearDevice.isPending}
              onClick={() => {
                if (
                  !window.confirm(
                    forSomeoneElse
                      ? t("web.settings.device.resetConfirmOther", {
                          count: changedCount,
                          count_text: String(changedCount),
                          owner: ownerLabel,
                          device: displayName,
                        })
                      : t("web.settings.device.resetConfirm", {
                          count: changedCount,
                          count_text: String(changedCount),
                          device: displayName,
                        }),
                  )
                ) {
                  return;
                }
                clearDevice.mutate(
                  { deviceId: device.device_id, profileId: targetProfileId },
                  {
                    onSuccess: () => toast.success(t("web.settings.device.resetDone")),
                    onError: (error) =>
                      toast.error(
                        error instanceof Error
                          ? error.message
                          : t("web.settings.device.resetFailed"),
                      ),
                  },
                );
              }}
            >
              {t("web.settings.device.useProfileSettings")}
            </Button>
          ) : null}
          {!device.is_current_device ? (
            <Button
              variant="ghost"
              className="text-muted-foreground hover:text-destructive min-h-11 self-start px-0 text-[13px] sm:h-8 sm:min-h-0 sm:px-3 sm:text-sm"
              disabled={forgetDevice.isPending}
              onClick={() => {
                if (
                  !window.confirm(t("web.settings.device.forgetConfirm", { device: displayName }))
                ) {
                  return;
                }
                forgetDevice.mutate(
                  { deviceId: device.device_id, profileId: targetProfileId },
                  {
                    onSuccess: () => toast.success(t("web.settings.device.forgotten")),
                    onError: (error) =>
                      toast.error(
                        error instanceof Error
                          ? error.message
                          : t("web.settings.device.forgetFailed"),
                      ),
                  },
                );
              }}
            >
              <Trash2 className="h-3.5 w-3.5" />
              {t("web.settings.device.forget")}
            </Button>
          ) : null}
        </div>
      </section>

      {forSomeoneElse ? (
        <Callout tone="warning" icon={<Users className="h-4 w-4" />}>
          {t("web.settings.device.otherProfileWarning", {
            owner: ownerLabel,
            name: device.profile_name,
            strong: (chunks) => <strong className="text-foreground font-semibold">{chunks}</strong>,
          })}
        </Callout>
      ) : null}

      {/* The scope sentence is required copy and must not be paraphrased away,
          but five lines of prose is a wall on a phone. The mandated clause
          leads; the elaboration is there for anyone who wants it. */}
      <Callout tone="info" icon={<Info className="h-4 w-4" />}>
        <span className="block">
          {forSomeoneElse
            ? t("web.settings.device.scopeOther", { owner: ownerLabel, strong: scopeStrong })
            : t("web.settings.device.scopeOwn", { strong: scopeStrong })}
        </span>
        <span className="text-muted-foreground mt-1 block text-[13px]">{scopeDetail}</span>
      </Callout>

      {capabilitiesUnavailable ? (
        <div
          role="alert"
          className="border-border/70 bg-surface space-y-3 rounded-[1.7rem] border p-5"
        >
          <div>
            <p className="text-sm font-semibold">
              {t("web.settings.device.capabilitiesFailed.title")}
            </p>
            <p className="text-muted-foreground mt-1 text-[13px] leading-relaxed">
              {t("web.settings.device.capabilitiesFailed.body")}
            </p>
          </div>
          <Button
            type="button"
            variant="outline"
            disabled={capabilities.isFetching}
            onClick={() => void capabilities.refetch()}
          >
            {capabilities.isFetching
              ? t("web.settings.device.capabilitiesFailed.checking")
              : t("web.settings.device.capabilitiesFailed.retry")}
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
          storedOnDevice={storedOnDevice}
          disabled={setValue.isPending || clearValue.isPending}
          onChange={(key: SettingKey, value: unknown) =>
            setValue.mutate(
              { key, value, identity },
              {
                onError: (error) =>
                  toast.error(
                    error instanceof Error ? error.message : t("web.settings.device.saveFailed"),
                  ),
              },
            )
          }
          onReset={(key: SettingKey) =>
            clearValue.mutate(
              { key, identity },
              {
                onError: (error) =>
                  toast.error(
                    error instanceof Error
                      ? error.message
                      : t("web.settings.device.settingResetFailed"),
                  ),
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
                toast.error(
                  error instanceof Error ? error.message : t("web.settings.device.saveFailed"),
                ),
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
                toast.error(
                  error instanceof Error
                    ? error.message
                    : t("web.settings.device.settingResetFailed"),
                ),
            },
          );
          setAppearanceOpen(false);
        }}
        resetLabel={
          forSomeoneElse
            ? t("web.settings.device.appearance.resetOther", { owner: ownerLabel })
            : t("web.settings.device.appearance.resetOwn")
        }
        eyebrow={
          device.custom_name || device.device_name || t("web.settings.device.appearance.thisDevice")
        }
        status={
          setValue.isPending
            ? t("web.settings.device.saving")
            : t("web.settings.device.appearance.savedAutomatically")
        }
      />

      {forSomeoneElse ? (
        <Callout tone="muted" icon={<ShieldCheck className="h-4 w-4" />}>
          {t("web.settings.device.privacyNote", {
            strong: (chunks) => <strong className="text-foreground font-semibold">{chunks}</strong>,
          })}
        </Callout>
      ) : null}
    </div>
  );
}

/**
 * Names the device in place of its heading. Saving an empty field clears the
 * chosen name, so the device shows the name it reports again.
 */
function DeviceNameEditor({
  device,
  target,
  onDone,
}: {
  device: UserDevice;
  target: DeviceTarget;
  onDone: () => void;
}) {
  const t = useT();
  const rename = useRenameDevice();
  const [value, setValue] = useState(device.custom_name ?? "");

  const save = () => {
    const name = value.trim() || null;
    if (name === (device.custom_name ?? null)) {
      onDone();
      return;
    }
    rename.mutate(
      { device: target, name },
      {
        onSuccess: () => {
          toast.success(
            name ? t("web.settings.device.renamed") : t("web.settings.device.nameReset"),
          );
          onDone();
        },
        onError: (error) =>
          toast.error(
            error instanceof Error ? error.message : t("web.settings.device.renameFailed"),
          ),
      },
    );
  };

  return (
    <form
      className="flex flex-col gap-2 sm:flex-row sm:items-center"
      onSubmit={(event) => {
        event.preventDefault();
        save();
      }}
    >
      <Input
        aria-label={t("web.settings.device.nameLabel")}
        autoFocus
        value={value}
        placeholder={device.device_name || t("web.settings.device.nameLabel")}
        // The server counts code points, not the UTF-16 units maxLength
        // counts, so an emoji takes one of the 120 rather than two.
        onChange={(event) => setValue(Array.from(event.target.value).slice(0, 120).join(""))}
        onKeyDown={(event) => {
          // Closing mid-save would drop the result, error included.
          if (event.key === "Escape" && !rename.isPending) onDone();
        }}
        className="h-11 sm:h-8 sm:max-w-xs"
      />
      <div className="flex gap-2">
        <Button type="submit" className="min-h-11 sm:h-8 sm:min-h-0" disabled={rename.isPending}>
          {rename.isPending ? t("web.settings.device.saving") : t("common.save")}
        </Button>
        <Button
          type="button"
          variant="ghost"
          className="min-h-11 sm:h-8 sm:min-h-0"
          disabled={rename.isPending}
          onClick={onDone}
        >
          {t("common.cancel")}
        </Button>
      </div>
    </form>
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
