import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { EyeOff, Search, Undo2 } from "lucide-react";

import type { UnmatchedLibraryItem } from "@/api/types";
import MatchItemDialog from "@/components/MatchItemDialog";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import {
  CARD_ACTION_CLASS,
  CardActions,
  CardField,
  CardFields,
  CardListMessage,
  DiagnosticsCard,
  DiagnosticsCardList,
  DiagnosticsSearch,
  PaginationBar,
  TOUCH_CONTROL_CLASS,
} from "@/components/admin/libraries/DiagnosticsListParts";
import { useLibrariesTableLayout } from "@/components/admin/libraries/diagnosticsListState";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  UNMATCHED_PAGE_SIZE,
  useSetUnmatchedItemIgnored,
  useUnmatchedLibraryItems,
} from "@/hooks/queries/admin/libraries";
import { useDebounce } from "@/hooks/useDebounce";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

// IGNORABLE_UNMATCHED_TYPES are the content types an admin can mark as
// intentionally unmatched.
const IGNORABLE_UNMATCHED_TYPES = new Set(["movie", "series"]);

export function UnmatchedItemsSection() {
  const t = useT();
  const showTable = useLibrariesTableLayout();
  const [open, setOpen] = useState(false);
  const navigationVersion = useRef(0);
  useEffect(
    () => () => {
      navigationVersion.current++;
    },
    [],
  );
  const [page, setPage] = useState(0);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 250);
  // "ignored" lists the items an admin marked as intentionally unmatched.
  const [view, setView] = useState<"unmatched" | "ignored">("unmatched");
  const ignoredView = view === "ignored";
  const [matchItem, setMatchItem] = useState<UnmatchedLibraryItem | null>(null);
  const setIgnored = useSetUnmatchedItemIgnored();
  // The listing is an infinite query that retains each loaded page's cursor:
  // `page` indexes the loaded pages, Next fetches only when the next page is
  // not loaded yet, and a refetch refreshes the loaded pages in place.
  const { data, hasNextPage, fetchNextPage, isFetching } = useUnmatchedLibraryItems(
    debouncedSearch,
    ignoredView,
  );
  // Unfiltered totals of both views, for the header count and for hiding the
  // section; each shares its cache entry with the listing while no search
  // is active.
  const awaitingTotal = useUnmatchedLibraryItems("", false).data?.pages[0]?.total;
  const ignoredTotal = useUnmatchedLibraryItems("", true).data?.pages[0]?.total;
  const loadedPages = data?.pages ?? [];
  const total = loadedPages[0]?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / UNMATCHED_PAGE_SIZE));
  const clamped = Math.min(page, Math.max(loadedPages.length - 1, 0), totalPages - 1);
  const rangeStart = total === 0 ? 0 : clamped * UNMATCHED_PAGE_SIZE + 1;
  const rangeEnd = Math.min((clamped + 1) * UNMATCHED_PAGE_SIZE, total);
  const items = loadedPages[clamped]?.items ?? [];
  const canNext = !isFetching && (clamped + 1 < loadedPages.length || hasNextPage);
  const showPage = (index: number) => {
    const version = ++navigationVersion.current;
    if (index < loadedPages.length) {
      setPage(index);
      return;
    }
    // The page is not loaded yet: extend the cursor chain up to it, one
    // request per missing page, then show it.
    void (async () => {
      let pages = loadedPages.length;
      let more = hasNextPage;
      while (pages <= index && more) {
        const result = await fetchNextPage();
        if (navigationVersion.current !== version) return;
        const loaded = result.data?.pages.length ?? pages;
        if (loaded === pages) break;
        pages = loaded;
        more = result.hasNextPage;
      }
      setPage(Math.max(0, pages - 1));
    })();
  };

  // Hide the section only while it is closed and nothing is unmatched or
  // ignored. Once open it stays mounted, so the search box and the empty
  // states stay put while a search or an Ignore refetches the listings.
  if (!open && !awaitingTotal && !ignoredTotal && search.trim() === "") return null;

  const emptyMessage = ignoredView
    ? search.trim()
      ? t("web.admin.libraries.unmatched.emptyIgnoredSearch")
      : t("web.admin.libraries.unmatched.emptyIgnored")
    : search.trim()
      ? t("web.admin.libraries.unmatched.emptyAwaitingSearch")
      : t("web.admin.libraries.unmatched.emptyAwaiting");

  // The row's Match and Ignore/Restore actions, sized by the layout showing them.
  const rowActions = (u: UnmatchedLibraryItem, buttonClassName: string) => (
    <>
      <Button
        variant="outline"
        size="sm"
        className={cn("gap-1", buttonClassName)}
        onClick={() => setMatchItem(u)}
      >
        <Search className="h-3 w-3" />
        {t("web.admin.libraries.match")}
      </Button>
      {/* Books keep their own enrichment, so only a movie or series can be ignored. */}
      {ignoredView || IGNORABLE_UNMATCHED_TYPES.has(u.content_type) ? (
        <Button
          variant="ghost"
          size="sm"
          className={cn("gap-1", buttonClassName)}
          disabled={setIgnored.isPending && setIgnored.variables?.contentId === u.content_id}
          aria-label={t(
            ignoredView
              ? "web.admin.libraries.unmatched.restoreLabel"
              : "web.admin.libraries.unmatched.ignoreLabel",
            { title: u.title },
          )}
          title={
            ignoredView
              ? t("web.admin.libraries.unmatched.restoreTitle")
              : t("web.admin.libraries.unmatched.ignoreTitle")
          }
          onClick={() =>
            setIgnored.mutate({
              contentId: u.content_id,
              title: u.title,
              ignored: !ignoredView,
            })
          }
        >
          {ignoredView ? <Undo2 className="h-3 w-3" /> : <EyeOff className="h-3 w-3" />}
          {ignoredView
            ? t("web.admin.libraries.unmatched.restore")
            : t("web.admin.libraries.unmatched.ignore")}
        </Button>
      ) : null}
    </>
  );

  // The linked title and its year, as the table's first cell and a card's heading.
  const titleWithYear = (u: UnmatchedLibraryItem) => (
    <>
      <Link
        to={`/item/${u.content_id}`}
        className="hover:text-primary underline-offset-2 hover:underline"
      >
        {u.title}
      </Link>
      {u.year ? (
        <span className="text-muted-foreground ms-1 text-xs">
          {t("web.admin.libraries.unmatched.year", { year: u.year })}
        </span>
      ) : null}
    </>
  );

  return (
    <CollapsibleDiagnosticsSection
      title={t("web.admin.libraries.unmatched.title")}
      description={t("web.admin.libraries.unmatched.description")}
      count={awaitingTotal}
      icon={<Search className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={setOpen}
    >
      <div className="mb-2 flex flex-col gap-2 sm:flex-row">
        <Select
          value={view}
          onValueChange={(value) => {
            navigationVersion.current++;
            setView(value === "ignored" ? "ignored" : "unmatched");
            setPage(0);
          }}
        >
          <SelectTrigger
            aria-label={t("web.admin.libraries.unmatched.viewLabel")}
            className={cn("h-8 w-full text-xs sm:w-[180px]", TOUCH_CONTROL_CLASS, "max-lg:text-sm")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="unmatched" className="max-lg:min-h-11">
              {t("web.admin.libraries.unmatched.viewAwaiting")}
            </SelectItem>
            <SelectItem value="ignored" className="max-lg:min-h-11">
              {t("web.admin.libraries.unmatched.viewIgnored")}
            </SelectItem>
          </SelectContent>
        </Select>
        <DiagnosticsSearch
          className="flex-1"
          placeholder={
            ignoredView
              ? t("web.admin.libraries.unmatched.searchIgnored")
              : t("web.admin.libraries.unmatched.searchAwaiting")
          }
          value={search}
          onValueChange={(value) => {
            // Search is server-side and spans the whole table; jump back to
            // the first page so results start at the top as the query changes.
            navigationVersion.current++;
            setSearch(value);
            setPage(0);
          }}
        />
      </div>
      {showTable ? (
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>{t("web.admin.libraries.column.title")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.library")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.type")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.status")}</TableHead>
                <TableHead className="w-[190px]">
                  {t("web.admin.libraries.column.actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground text-center text-sm">
                    {emptyMessage}
                  </TableCell>
                </TableRow>
              ) : (
                items.map((u) => (
                  <TableRow key={u.content_id}>
                    <TableCell className="font-medium">{titleWithYear(u)}</TableCell>
                    <TableCell className="text-sm">{u.library_name}</TableCell>
                    <TableCell>
                      <Badge variant="outline">{u.content_type}</Badge>
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary">{u.status}</Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">{rowActions(u, "h-7 text-xs")}</div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      ) : items.length === 0 ? (
        <CardListMessage>{emptyMessage}</CardListMessage>
      ) : (
        <DiagnosticsCardList>
          {items.map((u) => (
            <DiagnosticsCard key={u.content_id}>
              <h3 className="font-medium break-words">{titleWithYear(u)}</h3>
              <CardFields>
                <CardField label={t("web.admin.libraries.column.library")}>
                  {u.library_name}
                </CardField>
                <CardField label={t("web.admin.libraries.column.type")}>
                  <Badge variant="outline">{u.content_type}</Badge>
                </CardField>
                <CardField label={t("web.admin.libraries.column.status")}>
                  <Badge variant="secondary" className="max-w-full whitespace-normal">
                    {u.status}
                  </Badge>
                </CardField>
              </CardFields>
              <CardActions>{rowActions(u, CARD_ACTION_CLASS)}</CardActions>
            </DiagnosticsCard>
          ))}
        </DiagnosticsCardList>
      )}
      {total > UNMATCHED_PAGE_SIZE && (
        <PaginationBar
          total={total}
          rangeStart={rangeStart}
          rangeEnd={rangeEnd}
          canPrev={clamped > 0}
          canNext={canNext}
          first={() => showPage(0)}
          prev={() => showPage(Math.max(0, clamped - 1))}
          next={() => showPage(clamped + 1)}
          last={() => showPage(totalPages - 1)}
        />
      )}
      {matchItem && (
        <MatchItemDialog
          item={{
            content_id: matchItem.content_id,
            library_id: matchItem.library_id,
            title: matchItem.title,
            year: matchItem.year,
            type: matchItem.content_type,
          }}
          open={true}
          onOpenChange={(open) => {
            if (!open) setMatchItem(null);
          }}
        />
      )}
    </CollapsibleDiagnosticsSection>
  );
}
