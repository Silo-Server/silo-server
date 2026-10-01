import { SideNavItem, SideNavSection } from "@/components/SideNav";
import { ADMIN_SETTINGS_NAV, type AdminSettingsSearchItem } from "@/lib/adminSettingsSearch";
import { settingsPageHref } from "@/hooks/admin/useSettingsOverview";

export interface SettingsPageRailProps {
  /** Page id of the page being shown, e.g. `general`. */
  activeId: string;
  /** Pages to show — pass the search-filtered list; defaults to every page. */
  items?: readonly AdminSettingsSearchItem[];
}

/**
 * The admin settings sibling nav: every settings page one hop away, rendered
 * with the same `SideNavItem` rail the user settings page uses so the two
 * settings surfaces read identically. Desktop only — on smaller screens the
 * Overview (via the All settings link) is the directory.
 */
export function SettingsPageRail({ activeId, items = ADMIN_SETTINGS_NAV }: SettingsPageRailProps) {
  return (
    <nav aria-label="Settings pages" className="sticky top-6 space-y-5 py-5 pr-3 pl-5">
      <SideNavSection label="Settings" idPrefix="admin-settings-nav">
        {items.map((item) => (
          <SideNavItem
            key={item.id}
            label={item.label}
            icon={item.icon}
            href={settingsPageHref(item.id)}
            active={item.id === activeId}
          />
        ))}
      </SideNavSection>
      {items.length === 0 ? (
        <p className="text-muted-foreground px-2 text-sm">No matching settings</p>
      ) : null}
    </nav>
  );
}
