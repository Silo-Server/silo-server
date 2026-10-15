import { useMemo, useState } from "react";
import { useLocation } from "react-router";

import { CurrentProfileLink } from "@/components/settings/CurrentProfileLink";
import { SettingsSidebar } from "@/components/settings/SettingsSidebar";
import {
  countSettingsSearchItems,
  filterSettingsSearchGroups,
} from "@/components/settings/settingsSearch";
import { useSidebarEntrance, type SidebarSwap } from "@/hooks/useSidebarSwap";
import { useUserSettingsGroups } from "@/hooks/useUserSettingsGroups";
import { userSettingsHref } from "@/lib/userSettingsNav";

/**
 * The app sidebar while a personal settings page is open. `Layout` mounts it
 * in place of `AppSidebar`, at the same 260px width.
 */
export default function UserSettingsSidebar({
  backTo,
  swap,
}: {
  backTo: string;
  swap?: SidebarSwap | null;
}) {
  const location = useLocation();
  const entrance = useSidebarEntrance(swap);
  const groups = useUserSettingsGroups();
  const [search, setSearch] = useState("");
  const filteredGroups = useMemo(
    () => filterSettingsSearchGroups(groups, search),
    [groups, search],
  );
  // The root redirects to the first page; marking it now saves a frame with
  // nothing selected.
  const activeId = location.pathname.split("/")[2] || groups[0]?.items[0]?.path || null;

  return (
    <SettingsSidebar
      title="Settings"
      backTo={backTo}
      backLabel="Back to App"
      groups={filteredGroups.map((group) => ({
        label: group.label,
        items: group.items.map((item) => ({
          id: item.path,
          label: item.label,
          icon: item.icon,
          href: userSettingsHref(item.path),
        })),
      }))}
      activeId={activeId}
      search={search}
      onSearchChange={setSearch}
      resultCount={countSettingsSearchItems(filteredGroups)}
      idPrefix="settings-nav"
      scope={<CurrentProfileLink />}
      entrance={entrance}
      className="w-[260px]"
    />
  );
}
