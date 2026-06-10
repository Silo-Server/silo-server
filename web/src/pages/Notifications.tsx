import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  useNotificationsList,
  useMarkRead,
  useDismissNotification,
  useUnreadCount,
} from "@/hooks/queries/notifications";
import { useAuth } from "@/hooks/useAuth";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { timeAgo } from "@/lib/timeAgo";
import type { AppNotification } from "@/api/types";

// ---------------------------------------------------------------------------
// Category tab definitions
// ---------------------------------------------------------------------------

type CategoryValue = "" | "request" | "content" | "announcement" | "system" | "admin";

interface Tab {
  label: string;
  value: CategoryValue;
  adminOnly?: boolean;
}

const TABS: Tab[] = [
  { label: "All", value: "" },
  { label: "Requests", value: "request" },
  { label: "Content", value: "content" },
  { label: "Announcements", value: "announcement" },
  { label: "System", value: "system" },
  { label: "Admin", value: "admin", adminOnly: true },
];

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function Notifications() {
  const { user } = useAuth();
  const [activeTab, setActiveTab] = useState<CategoryValue>("");

  useDocumentTitle("Notifications");

  const filters =
    activeTab === "" ? {} : { category: activeTab };

  const list = useInfiniteList(filters);
  const markRead = useMarkRead();
  const dismiss = useDismissNotification();
  const { data: unreadCount = 0 } = useUnreadCount();

  // Mark-on-view: fire once when page 1 arrives, reset when tab changes
  const firstPage = list.data?.pages?.[0];
  const markedRef = useRef(false);
  const prevTabRef = useRef(activeTab);

  useEffect(() => {
    // Reset marked flag when tab changes
    if (prevTabRef.current !== activeTab) {
      prevTabRef.current = activeTab;
      markedRef.current = false;
    }
  }, [activeTab]);

  useEffect(() => {
    if (markedRef.current || !firstPage) return;
    const unreadIds = (firstPage.items ?? []).filter((n) => !n.read_at).map((n) => n.id);
    if (unreadIds.length > 0) markRead.mutate({ ids: unreadIds });
    markedRef.current = true;
  }, [firstPage, markRead]);

  const visibleTabs = TABS.filter((t) => !t.adminOnly || user?.role === "admin");

  const allItems: AppNotification[] = (list.data?.pages ?? []).flatMap(
    (p) => p.items ?? [],
  );

  const isEmpty = allItems.length === 0;

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      {/* Header row */}
      <div className="page-header">
        <h1 className="page-title text-[clamp(1.75rem,4vw,2.75rem)]">Notifications</h1>
        <Button
          variant="outline"
          size="sm"
          disabled={unreadCount === 0}
          onClick={() => markRead.mutate({ all: true })}
        >
          Mark all read
        </Button>
      </div>

      {/* Category tabs */}
      <div className="flex flex-wrap gap-1.5" role="tablist">
        {visibleTabs.map((tab) => (
          <button
            key={tab.value}
            type="button"
            role="tab"
            aria-selected={activeTab === tab.value}
            className={[
              "rounded-full px-3.5 py-1.5 text-sm font-medium transition-colors",
              activeTab === tab.value
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground hover:text-foreground",
            ].join(" ")}
            onClick={() => setActiveTab(tab.value)}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {/* List */}
      {isEmpty ? (
        <div className="surface-panel flex flex-col items-center justify-center gap-3 rounded-[2rem] py-16 text-center">
          <p className="text-sm font-medium">
            {activeTab === "" ? "No notifications yet" : "Nothing in this category"}
          </p>
        </div>
      ) : (
        <ul className="space-y-2">
          {allItems.map((n) => {
            const isUnread = !n.read_at;
            const relTime = timeAgo(n.created_at, "") ?? n.created_at;
            const inner = (
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium leading-snug">{n.title}</p>
                {n.body ? (
                  <p className="text-muted-foreground mt-0.5 text-xs">{n.body}</p>
                ) : null}
                <p className="text-muted-foreground mt-1 text-xs">{relTime}</p>
              </div>
            );

            return (
              <li
                key={n.id}
                data-unread={isUnread ? "true" : undefined}
                className={[
                  "surface-panel group relative flex items-start gap-3 rounded-[1.25rem] px-4 py-3 transition-colors",
                  isUnread ? "bg-primary/5" : "",
                ].join(" ")}
              >
                {n.link ? (
                  <Link
                    to={n.link}
                    className="min-w-0 flex-1 after:absolute after:inset-0 after:rounded-[1.25rem]"
                  >
                    {inner}
                  </Link>
                ) : (
                  inner
                )}
                <button
                  type="button"
                  aria-label="Dismiss"
                  className="relative z-10 ml-auto mt-0.5 flex-shrink-0 rounded-md p-1 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 [@media(pointer:coarse)]:opacity-100"
                  onClick={(e) => {
                    e.stopPropagation();
                    e.preventDefault();
                    dismiss.mutate(n.id);
                  }}
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </li>
            );
          })}
        </ul>
      )}

      {/* Load more */}
      {list.hasNextPage ? (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            onClick={() => list.fetchNextPage()}
          >
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Thin wrapper so we get a stable shape regardless of filter state
// ---------------------------------------------------------------------------
function useInfiniteList(filters: { category?: string }) {
  return useNotificationsList(filters);
}
