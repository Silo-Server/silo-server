import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import type { AdminUser } from "@/api/types";
import { TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAdminUserCapabilities, useAdminUserSettingCounts } from "@/hooks/queries/admin/users";
import {
  useAdminUserDownloadSummary,
  useAdminUserLiveSessions,
} from "@/hooks/queries/admin/userActivity";
import { cn } from "@/lib/utils";

import { countCustomPolicyRows, countCustomRequestTerms } from "./access/policySources";
import { useAccountRequestTerms } from "./access/RequestsCard";
import type { UserDetailTab } from "./userDetailTabs";

function Count({ children }: { children: ReactNode }) {
  return <span className="text-muted-foreground font-normal tabular-nums">{children}</span>;
}

/** Fades the strip's edges that have more tabs past them. */
function edgeMask({ left, right }: { left: boolean; right: boolean }): string | undefined {
  if (left && right) {
    return "[mask-image:linear-gradient(to_right,transparent,black_2rem,black_calc(100%-2rem),transparent)]";
  }
  if (right) return "[mask-image:linear-gradient(to_right,black_calc(100%-2rem),transparent)]";
  if (left) return "[mask-image:linear-gradient(to_right,transparent,black_2rem)]";
  return undefined;
}

/**
 * The page's tab strip. Each tab carries a hint of what is inside: how many
 * limits the account sets, a dot while it is watching, and item counts. It
 * scrolls sideways on a narrow screen, fades the edge that has more tabs past
 * it, and keeps the active tab in view. Render it inside the page's `Tabs`.
 */
export function UserDetailTabBar({ user, active }: { user: AdminUser; active: UserDetailTab }) {
  const scroller = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ left: false, right: false });
  const measure = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    const left = el.scrollLeft > 1;
    const right = el.scrollLeft + el.clientWidth < el.scrollWidth - 1;
    setEdges((prev) => (prev.left === left && prev.right === right ? prev : { left, right }));
  }, []);

  // Bring the active tab into view (a deep link to a far tab on a phone),
  // scrolling only the strip, never the page.
  const reveal = useCallback(() => {
    const el = scroller.current;
    const trigger = el?.querySelector<HTMLElement>(`[role="tab"][data-state="active"]`);
    if (!el || !trigger) return;
    const start =
      trigger.getBoundingClientRect().left - el.getBoundingClientRect().left + el.scrollLeft;
    const end = start + trigger.getBoundingClientRect().width;
    if (start < el.scrollLeft) el.scrollLeft = start - 16;
    else if (end > el.scrollLeft + el.clientWidth) el.scrollLeft = end - el.clientWidth + 16;
    measure();
  }, [measure]);

  useEffect(() => {
    reveal();
  }, [active, reveal]);

  // The strip widens after first paint as the tab counts load, which can push
  // the active tab back past the edge: reveal it again whenever the tab list
  // changes size. The viewport changing size only moves the fades.
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const viewport = new ResizeObserver(measure);
    viewport.observe(el);
    const list = el.firstElementChild;
    const content = new ResizeObserver(reveal);
    if (list) content.observe(list);
    return () => {
      viewport.disconnect();
      content.disconnect();
    };
  }, [measure, reveal]);

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
