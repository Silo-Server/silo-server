import type { ReactNode } from "react";

import type { AdminUser } from "@/api/types";
import { TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAdminUserCapabilities, useAdminUserSettingCounts } from "@/hooks/queries/admin/users";
import {
  useAdminUserDownloadSummary,
  useAdminUserLiveSessions,
} from "@/hooks/queries/admin/userActivity";
import { edgeMask, useScrollStrip } from "@/hooks/useScrollStrip";
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
 * scrolls sideways on a narrow screen, fades the edge that has more tabs past
 * it, and keeps the active tab in view. Render it inside the page's `Tabs`.
 */
export function UserDetailTabBar({ user, active }: { user: AdminUser; active: UserDetailTab }) {
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
  // The group name only labels sources, which a count doesn't need.
  const { terms: requestTerms } = useAccountRequestTerms(user, undefined);
  const custom = countCustomPolicyRows(user) + countCustomRequestTerms(requestTerms);
  const watching = (live.data?.length ?? 0) > 0;
  const preferencesLoaded = !settingCounts.isLoading && !settingCounts.isError;
  const preferences = settingCounts.account + settingCounts.device;

  const tabs: Array<{ value: UserDetailTab; label: string; extra?: ReactNode }> = [
    { value: "overview", label: "Overview" },
    {
      value: "access",
      label: "Access & limits",
      extra: custom > 0 ? <Count>{custom} custom</Count> : null,
    },
    { value: "sign-in", label: "Sign-in" },
    {
      value: "activity",
      label: "Activity",
      extra: watching ? (
        <span role="img" aria-label="Watching now" className="bg-success size-2 rounded-full" />
      ) : null,
    },
    {
      value: "downloads",
      label: "Downloads",
      extra: accountDownloads && downloads.data ? <Count>{downloads.data.total}</Count> : null,
    },
    {
      value: "preferences",
      label: "Preferences",
      extra: preferencesLoaded ? <Count>{preferences}</Count> : null,
    },
  ];

  return (
    <div
      ref={scroller}
      onScroll={measure}
      className={cn("border-border/70 -mx-1 overflow-x-auto border-b px-1", edgeMask(edges))}
    >
      <TabsList
        variant="line"
        className="h-auto w-max min-w-full flex-nowrap justify-start gap-1 rounded-none bg-transparent p-0 pb-1.5"
      >
        {tabs.map((tab) => (
          <TabsTrigger key={tab.value} value={tab.value} className="flex-none px-3 py-1.5">
            {tab.label}
            {tab.extra}
          </TabsTrigger>
        ))}
      </TabsList>
    </div>
  );
}
