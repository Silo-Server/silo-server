import { ArrowLeft } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { SideNavItem, SideNavSection } from "@/components/SideNav";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { SettingsSearchInput } from "@/components/settings/SettingsSearchInput";
import type { SidebarSwap } from "@/hooks/useSidebarSwap";
import { cn } from "@/lib/utils";

const DESKTOP_MEDIA_QUERY = "(min-width: 64rem)";

export interface SettingsSidebarItem {
  id: string;
  label: string;
  icon: LucideIcon;
  href: string;
}

export interface SettingsSidebarGroup {
  label: string;
  items: readonly SettingsSidebarItem[];
}

export interface SettingsSidebarProps {
  /** Shown above the search box, e.g. "Settings" or "Server settings". */
  title: string;
  backTo: string;
  backLabel: string;
  /** Groups to list, already narrowed by `search`. */
  groups: readonly SettingsSidebarGroup[];
  activeId: string | null;
  search: string;
  onSearchChange: (value: string) => void;
  resultCount: number;
  /** See `SettingsSearchInput`; off where another surface owns ⌘K. */
  captureShortcut?: boolean;
  /** Prefix for group heading ids, so two settings areas never collide. */
  idPrefix: string;
  /** Rendered under the title: whose settings these are. */
  scope?: ReactNode;
  /** Set when this sidebar just replaced the shell's own; see useSidebarSwap. */
  entrance?: SidebarSwap;
  className?: string;
}

/**
 * The sidebar personal settings put in place of the app's navigation: a way
 * back out, search, and every settings page grouped. `Layout` renders it at
 * the app sidebar's width, so entering settings swaps what the column holds
 * instead of adding a second column beside it.
 */
export function SettingsSidebar({
  title,
  backTo,
  backLabel,
  groups,
  activeId,
  search,
  onSearchChange,
  resultCount,
  captureShortcut,
  idPrefix,
  scope,
  entrance,
  className,
}: SettingsSidebarProps) {
  return (
    <aside
      data-settings-sidebar=""
      data-sidebar-entrance={entrance}
      className={cn(
        "border-sidebar-border/70 bg-sidebar/92 fixed top-0 bottom-0 left-0 z-40 flex flex-col border-r backdrop-blur-2xl",
        className,
      )}
    >
      <div className="space-y-4 px-3 pt-6 pb-4">
        <ViewTransitionLink
          to={backTo}
          up
          className="text-muted-foreground hover:text-foreground hover:bg-accent/70 focus-visible:ring-ring/60 flex w-fit items-center gap-2 rounded-lg px-2 py-1.5 text-[13px] font-medium transition-colors focus-visible:ring-[3px] focus-visible:outline-none"
        >
          <ArrowLeft className="h-4 w-4" aria-hidden="true" />
          {backLabel}
        </ViewTransitionLink>
        <p className="px-2 text-[22px] leading-tight font-extrabold tracking-[-0.03em]">{title}</p>
        {scope}
        <SettingsSearchInput
          value={search}
          onChange={onSearchChange}
          resultCount={resultCount}
          captureShortcut={captureShortcut}
          // The shells keep this sidebar mounted but hidden below lg, where the
          // page's own search box is the one on screen.
          shortcutMediaQuery={DESKTOP_MEDIA_QUERY}
        />
      </div>

      <nav aria-label={title} className="sidebar-scroll flex-1 space-y-5 overflow-y-auto px-3 pb-6">
        {groups.map((group) => (
          <SideNavSection key={group.label} label={group.label} idPrefix={idPrefix}>
            {group.items.map((item) => (
              <SideNavItem
                key={item.id}
                label={item.label}
                icon={item.icon}
                href={item.href}
                active={item.id === activeId}
              />
            ))}
          </SideNavSection>
        ))}
        {groups.length === 0 ? (
          <p className="text-muted-foreground px-2 text-sm">No matching settings</p>
        ) : null}
      </nav>
    </aside>
  );
}
