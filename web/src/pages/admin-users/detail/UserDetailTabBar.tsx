import type { ReactNode } from "react";

import type { AdminUser } from "@/api/types";
import { TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAdminUserCapabilities, useAdminUserSettingCounts } from "@/hooks/queries/admin/users";
import {
  useAdminUserDownloadSummary,
  useAdminUserLiveSessions,
  useAdminUserProfileActivity,
} from "@/hooks/queries/admin/userActivity";
import { edgeMask, useScrollStrip } from "@/hooks/useScrollStrip";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

import { countCustomPolicyRows, countCustomRequestTerms } from "./access/policySources";
import { useAccountRequestTerms } from "./access/RequestsCard";
import type { UserDetailTab } from "./userDetailTabs";

function Count({ children }: { children: ReactNode }) {
  return <span className="text-muted-foreground font-normal tabular-nums">{children}</span>;
}

/**
 * The page's tab strip. Each tab carries a hint of what is inside: how many
 * limits the account sets, a dot while it is watching, and item counts. It
 * scrolls sideways on a narrow screen (with no scrollbar below `lg`), fades
 * the edge that has more tabs past it, and keeps the active tab in view.
 * Render it inside the page's `Tabs`.
 */
export function UserDetailTabBar({ user, active }: { user: AdminUser; active: UserDetailTab }) {
  const t = useT();
  const {
    ref: scroller,
    edges,
    measure,
  } = useScrollStrip<HTMLDivElement>(`[role="tab"][data-state="active"]`, active);

  const capabilities = useAdminUserCapabilities().data;
  const accountDownloads = capabilities?.account_downloads === true;
  const live = useAdminUserLiveSessions(user.id);
  const downloads = useAdminUserDownloadSummary(user.id, accountDownloads);
  const settingCounts = useAdminUserSettingCounts(user.id);
  const profiles = useAdminUserProfileActivity(user.id);
  // The group name only labels sources, which a count doesn't need.
  const { terms: requestTerms } = useAccountRequestTerms(user, undefined);
  const custom = countCustomPolicyRows(user) + countCustomRequestTerms(requestTerms);
  const watching = (live.data?.length ?? 0) > 0;
  const preferencesLoaded = !settingCounts.isLoading && !settingCounts.isError;
  const preferences = settingCounts.account + settingCounts.device;

  const tabs: Array<{ value: UserDetailTab; label: string; extra?: ReactNode }> = [
    { value: "overview", label: t("web.admin.users.detail.tabs.overview") },
    {
      value: "profiles",
      label: t("web.admin.users.detail.tabs.profiles"),
      extra: profiles.data ? <Count>{profiles.data.length}</Count> : null,
    },
    {
      value: "access",
      label: t("web.admin.users.detail.tabs.access"),
      extra:
        custom > 0 ? (
          <Count>{t("web.admin.users.detail.tabs.customCount", { count: custom })}</Count>
        ) : null,
    },
    { value: "sign-in", label: t("web.admin.users.detail.tabs.signIn") },
    {
      value: "activity",
      label: t("web.admin.users.detail.tabs.activity"),
      extra: watching ? (
        <span
          role="img"
          aria-label={t("web.admin.users.detail.common.watchingNow")}
          className="bg-success size-2 rounded-full"
        />
      ) : null,
    },
    {
      value: "downloads",
      label: t("web.admin.users.detail.tabs.downloads"),
      extra: accountDownloads && downloads.data ? <Count>{downloads.data.total}</Count> : null,
    },
    {
      value: "preferences",
      label: t("web.admin.users.detail.tabs.preferences"),
      extra: preferencesLoaded ? <Count>{preferences}</Count> : null,
    },
  ];

  return (
    <div
      ref={scroller}
      onScroll={measure}
      className={cn(
        "border-border/70 -mx-1 overflow-x-auto border-b px-1 max-lg:[scrollbar-width:none] max-lg:[&::-webkit-scrollbar]:hidden",
        edgeMask(edges),
      )}
    >
      <TabsList
        variant="line"
        className="h-auto w-max min-w-full flex-nowrap justify-start gap-1 rounded-none bg-transparent p-0 pb-1.5"
      >
        {tabs.map((tab) => (
          <TabsTrigger
            key={tab.value}
            value={tab.value}
            className="flex-none px-3 py-1.5 max-lg:min-h-11"
          >
            {tab.label}
            {tab.extra}
          </TabsTrigger>
        ))}
      </TabsList>
    </div>
  );
}
