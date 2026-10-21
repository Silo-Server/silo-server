import type { ReactNode } from "react";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  parseWatchlistTab,
  WATCHLIST_NOT_IN_LIBRARY_TAB,
  type WatchlistTab,
} from "@/lib/watchlistTitles";

function tabId(idBase: string, value: WatchlistTab): string {
  return `${idBase}-tab-${value}`;
}

function panelId(idBase: string, value: WatchlistTab): string {
  return `${idBase}-panel-${value}`;
}

/**
 * The watchlist page's tabs, in the Requests page's line style: the library
 * grid, and the titles the library doesn't have yet with their count. An
 * amber dot flags titles that need the viewer's attention.
 */
export default function WatchlistTabs({
  idBase,
  value,
  onValueChange,
  count,
  attention,
}: {
  /** Shared with the page's WatchlistTabPanel, which holds the selected tab's content. */
  idBase: string;
  value: WatchlistTab;
  onValueChange: (value: WatchlistTab) => void;
  /** Titles on the second tab; omitted while they load. */
  count?: number;
  attention: boolean;
}) {
  return (
    <Tabs value={value} onValueChange={(next) => onValueChange(parseWatchlistTab(next))}>
      <TabsList variant="line" className="border-border w-full justify-start border-b">
        <TabsTrigger
          value="library"
          id={tabId(idBase, "library")}
          aria-controls={panelId(idBase, "library")}
          className="flex-none px-3"
        >
          In your library
        </TabsTrigger>
        <TabsTrigger
          value={WATCHLIST_NOT_IN_LIBRARY_TAB}
          id={tabId(idBase, WATCHLIST_NOT_IN_LIBRARY_TAB)}
          aria-controls={panelId(idBase, WATCHLIST_NOT_IN_LIBRARY_TAB)}
          className="flex-none px-3"
        >
          <span className="sm:hidden">Not in library</span>
          <span className="hidden sm:inline">Not in your library yet</span>
          {count !== undefined && count > 0 ? (
            <span className="bg-muted/80 text-muted-foreground ml-1.5 inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[0.625rem] font-semibold tabular-nums">
              {count}
            </span>
          ) : null}
          {attention ? (
            <>
              <span
                aria-hidden
                data-testid="watchlist-attention-dot"
                className="ml-1.5 inline-block size-2 rounded-full bg-amber-400"
              />
              <span className="sr-only">, some need attention</span>
            </>
          ) : null}
        </TabsTrigger>
      </TabsList>
    </Tabs>
  );
}

/**
 * The content of the selected watchlist tab, labelled by its tab. The page
 * lays the content out apart from the tabs, so the tabs point here by ID
 * instead of through Radix's TabsContent. Without tabs (`tabs` false) the
 * children render as they are.
 */
export function WatchlistTabPanel({
  idBase,
  value,
  tabs = true,
  children,
}: {
  idBase: string;
  value: WatchlistTab;
  tabs?: boolean;
  children: ReactNode;
}) {
  if (!tabs) return <>{children}</>;
  return (
    <div
      role="tabpanel"
      id={panelId(idBase, value)}
      aria-labelledby={tabId(idBase, value)}
      className="space-y-6"
    >
      {children}
    </div>
  );
}
