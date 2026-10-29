import { useMemo, useState } from "react";
import { Link, Navigate, Outlet, useLocation } from "react-router";
import { ChevronLeft } from "lucide-react";

import { PageColumn, PageFrame, PageHeader } from "@/components/page/PageHeader";
import PageBack from "@/components/PageBack";
import { CurrentProfileLink } from "@/components/settings/CurrentProfileLink";
import { SettingsIndexList } from "@/components/settings/SettingsIndexList";
import { SettingsSearchInput } from "@/components/settings/SettingsSearchInput";
import {
  countSettingsSearchItems,
  filterSettingsSearchGroups,
} from "@/components/settings/settingsSearch";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useUserSettingsGroups } from "@/hooks/useUserSettingsGroups";
import { resolveSettingsDocumentTitle } from "@/lib/documentTitle";
import {
  USER_SETTINGS_GROUPS,
  userSettingsHref,
  type UserSettingsNavGroup,
} from "@/lib/userSettingsNav";

/** Where `Layout` swaps the app sidebar for the settings sidebar. */
const DESKTOP_MEDIA_QUERY = "(min-width: 64rem)";

/**
 * Settings pages that manage their own multi-column layout, so the shell's
 * reading-width cap would squeeze them instead of helping.
 */
const WIDE_SETTINGS_PAGES = new Set(["devices"]);

/**
 * Every settings page that exists, allowed or not. Used to tell "this page is
 * one this profile may not open" apart from "this segment is not a settings
 * page at all", which is left to the router as before.
 */
const ALL_SETTINGS_PATHS = new Set(
  USER_SETTINGS_GROUPS.flatMap((group) => group.items.map((item) => item.path)),
);

/**
 * Personal settings. On desktop the settings sidebar (mounted by `Layout` in
 * place of the app sidebar) is the navigation, so the root opens the first
 * page. On a phone the root is the list of pages and each page links back to
 * it.
 */
export default function SettingsLayout() {
  const location = useLocation();
  const groups = useUserSettingsGroups();
  const isDesktop = useMediaQuery(DESKTOP_MEDIA_QUERY);
  const activeSegment = location.pathname.split("/")[2] || null;
  const visiblePaths = useMemo(
    () => new Set(groups.flatMap((group) => group.items.map((item) => item.path))),
    [groups],
  );

  useDocumentTitle(resolveSettingsDocumentTitle(location.pathname));

  // A page this profile may not open goes back to the index instead of
  // rendering. The sidebar already omits it; this covers a typed URL or a
  // link from elsewhere, so hiding a page is not only cosmetic.
  if (activeSegment && ALL_SETTINGS_PATHS.has(activeSegment) && !visiblePaths.has(activeSegment)) {
    return <Navigate to="/settings" replace />;
  }

  if (!activeSegment) {
    const firstPage = groups[0]?.items[0];
    if (isDesktop && firstPage) {
      return <Navigate to={userSettingsHref(firstPage.path)} replace />;
    }
    return <SettingsIndex groups={groups} />;
  }

  const wideSetting = WIDE_SETTINGS_PAGES.has(activeSegment);

  return (
    <div className="lg:pt-8">
      <Link
        to="/settings"
        className="text-muted-foreground hover:text-foreground focus-visible:ring-ring mb-5 inline-flex w-fit items-center gap-1.5 rounded-lg pr-2 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none lg:hidden"
      >
        <ChevronLeft className="h-4 w-4" aria-hidden="true" />
        All settings
      </Link>
      <PageFrame>
        <PageColumn width={wideSetting ? "full" : "settings"}>
          <Outlet />
        </PageColumn>
      </PageFrame>
    </div>
  );
}

function SettingsIndex({ groups }: { groups: readonly UserSettingsNavGroup[] }) {
  const [search, setSearch] = useState("");
  const filteredGroups = useMemo(
    () => filterSettingsSearchGroups(groups, search),
    [groups, search],
  );

  return (
    <div className="mx-auto w-full max-w-3xl space-y-6">
      <PageBack to="/" up floating />
      {/* The top margin clears the floating back button above it. */}
      <PageHeader
        className="mt-10 sm:mt-12"
        title="Settings"
        subtitle="Make Silo work the way you like."
      />
      <CurrentProfileLink className="surface-panel-subtle min-h-14 rounded-2xl px-4 py-3" />
      <SettingsSearchInput
        value={search}
        onChange={setSearch}
        resultCount={countSettingsSearchItems(filteredGroups)}
        showShortcutHint
      />
      <SettingsIndexList
        groups={filteredGroups.map((group) => ({
          label: group.label,
          items: group.items.map((item) => ({
            id: item.path,
            label: item.label,
            description: item.description,
            icon: item.icon,
            href: userSettingsHref(item.path),
          })),
        }))}
        ariaLabel="Settings pages"
        idPrefix="settings-index"
      />
    </div>
  );
}
