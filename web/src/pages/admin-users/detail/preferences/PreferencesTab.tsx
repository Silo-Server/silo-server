import { useMemo } from "react";
import { useSearchParams } from "react-router";
import { Clapperboard, Globe, Layers, LibraryBig, MonitorSmartphone } from "lucide-react";

import type { AdminUser } from "@/api/types";
import { PlatformIcon, classifyPlatform } from "@/components/admin/deviceOverrides";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminUserDevices } from "@/hooks/queries/admin/userActivity";
import { useAdminUserCapabilities, useAdminUserSettings } from "@/hooks/queries/admin/users";
import { cn } from "@/lib/utils";

import { userDetailTabSearch } from "../userDetailTabs";
import { buildPreferenceLevels, type PreferenceLevel } from "./levels";
import { LevelSettings } from "./LevelSettings";

function LevelIcon({ level }: { level: PreferenceLevel }) {
  const className = "h-3.5 w-3.5 shrink-0";
  switch (level.kind) {
    case "account":
      return <Globe className={className} />;
    case "profile":
      return <Layers className={className} />;
    case "device":
      return <PlatformIcon kind={classifyPlatform(level.device?.platform)} className={className} />;
    case "client":
      return <MonitorSmartphone className={className} />;
    case "library":
      return <LibraryBig className={className} />;
    case "series":
      return <Clapperboard className={className} />;
  }
}

/**
 * Settings the account's people saved in their apps, by level: each profile's
 * settings for all devices, and what a device, app family, library, or series
 * replaces for that profile. Pick a level on the left; the right side shows
 * and edits only what it sets.
 */
export function PreferencesTab({ user }: { user: AdminUser }) {
  const [searchParams, setSearchParams] = useSearchParams();
  const settings = useAdminUserSettings(user.id);
  const profiles = useAdminUserProfiles(user.id);
  const libraries = useAdminLibraries();
  const accountDevices = useAdminUserCapabilities().data?.account_devices === true;
  const devices = useAdminUserDevices(user.id, accountDevices);

  const groups = useMemo(
    () =>
      buildPreferenceLevels({
        entries: settings.data,
        profiles: profiles.data ?? [],
        devices: devices.data ?? [],
        libraryNames: new Map((libraries.data ?? []).map((library) => [library.id, library.name])),
      }),
    [settings.data, profiles.data, devices.data, libraries.data],
  );
  const levels = useMemo(() => groups.flatMap((group) => group.levels), [groups]);
  const requested = searchParams.get("level");
  const selected =
    levels.find((level) => level.id === requested) ??
    levels.find((level) => level.kind === "profile") ??
    levels[0];
  const select = (id: string) => setSearchParams(userDetailTabSearch("preferences", { level: id }));
  const profileEntries =
    levels.find((level) => level.kind === "profile" && level.profileId === selected?.profileId)
      ?.entries ?? [];
  // Every scope this profile stores, for "Replaces …" to see app-family,
  // device, and library values that can come between a level and the profile.
  const selectedProfileId = selected?.profileId;
  const profileAllEntries = useMemo(
    () =>
      levels
        .filter((level) => level.profileId === selectedProfileId)
        .flatMap((level) => level.entries),
    [levels, selectedProfileId],
  );

  if (settings.isLoading) {
    return <p className="text-muted-foreground py-8 text-center text-sm">Loading settings...</p>;
  }
  if (settings.isError) {
    return (
      <p role="alert" className="text-destructive py-8 text-center text-sm">
        Couldn&apos;t load this user&apos;s settings.
      </p>
    );
  }
  if (!selected) {
    return (
      <p className="surface-panel text-muted-foreground rounded-2xl py-10 text-center text-sm">
        This account has no profiles with settings yet.
      </p>
    );
  }

  return (
    <div className="grid items-start gap-4 min-[761px]:grid-cols-[minmax(13rem,18rem)_minmax(0,1fr)]">
      <Select value={selected.id} onValueChange={select}>
        <SelectTrigger aria-label="Setting level" className="w-full min-[761px]:hidden">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {groups.map((group) => (
            <SelectGroup key={group.key}>
              <SelectLabel>{group.title}</SelectLabel>
              {group.levels.map((level) => (
                <SelectItem key={level.id} value={level.id}>
                  {/* The trigger shows only the item, so it carries the profile too. */}
                  {`${level.profileName ? `${level.profileName} · ` : ""}${level.name} (${level.entries.length})`}
                </SelectItem>
              ))}
            </SelectGroup>
          ))}
        </SelectContent>
      </Select>

      <nav
        aria-label="Setting levels"
        className="surface-panel rounded-2xl border-0 p-2 max-[760px]:hidden"
      >
        {groups.map((group) => (
          <div key={group.key} role="group" aria-label={group.title} className="pb-1">
            <div className="text-muted-foreground px-3 pt-2.5 pb-1.5 text-[11px] font-semibold tracking-[0.08em] uppercase">
              {group.title}
            </div>
            {group.levels.map((level) => {
              const active = level.id === selected.id;
              const top = level.kind === "profile" || level.kind === "account";
              return (
                <button
                  key={level.id}
                  type="button"
                  aria-current={active ? "true" : undefined}
                  onClick={() => select(level.id)}
                  className={cn(
                    "flex w-full items-center gap-2.5 rounded-lg py-1.5 pr-3 text-left text-sm transition-colors",
                    top ? "pl-3" : "pl-7",
                    active
                      ? "bg-accent text-foreground font-medium"
                      : "text-foreground/85 hover:bg-accent/50",
                  )}
                >
                  <span className="text-muted-foreground">
                    <LevelIcon level={level} />
                  </span>
                  <span className="min-w-0 flex-1 truncate">{level.name}</span>
                  <span className="text-muted-foreground text-xs tabular-nums">
                    {level.entries.length}
                  </span>
                </button>
              );
            })}
          </div>
        ))}
      </nav>

      <LevelSettings
        key={selected.id}
        userId={user.id}
        level={selected}
        profileEntries={profileEntries}
        profileAllEntries={profileAllEntries}
      />
    </div>
  );
}
