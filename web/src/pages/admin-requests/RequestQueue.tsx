import { useEffect, useMemo, useState } from "react";
import type { MouseEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { Maximize2, RefreshCw, Search, X } from "lucide-react";
import type { MediaRequest } from "@/api/types";
import { listAdminRequestOptionsV2, type AdminRequestCounts } from "@/api/v2/adminRequests";
import { BulkSelectionCheckbox } from "@/components/BulkSelectionCheckbox";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import {
  useAdminCancelMediaRequest,
  useAdminRequestCounts,
  useAdminRequestQueue,
  useApproveMediaRequest,
  useBulkRequestAction,
  useDeclineMediaRequest,
  useRefreshRequestQueue,
  useRetryMediaRequest,
} from "@/hooks/queries/admin/requests";
import { useDebounce } from "@/hooks/useDebounce";
import { numberText, useT, type MessageKey, type Translate } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";
import { formatDateTime } from "@/lib/datetime";
import { formatMediaType, formatSeasonList, requestDetailHref } from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";
import { RequestActionButtons, type RequestQueueActionHandlers } from "./RequestActionButtons";
import { RequestQueueDialog } from "./RequestQueueDialog";
import { ApproveOptionsDialog, type ApprovePrompt } from "./ApproveOptionsDialog";
import { BulkProgress } from "./BulkProgress";
import { OptionCells, TargetList } from "./QueueCells";
import {
  EmptyPanel,
  ReasonDialog,
  RequestPoster,
  RequestStateSummary,
  RowsSkeleton,
} from "./queueParts";
import {
  defaultRequestQueueView,
  parseRequestQueueUser,
  parseRequestQueueView,
  queueColumnsStyle,
  REQUEST_QUEUE_VIEWS,
  requestOptionColumns,
  type RequestQueueAction,
  type RequestQueueMediaFilter,
  type RequestQueueView,
} from "./requestQueueModel";

const SEARCH_DEBOUNCE_MS = 300;
const SEPARATOR = " · ";

const MEDIA_FILTERS: { value: RequestQueueMediaFilter; label: MessageKey }[] = [
  { value: "all", label: "web.admin.requests.queue.media.all" },
  { value: "movie", label: "web.admin.requests.queue.media.movies" },
  { value: "series", label: "web.admin.requests.queue.media.series" },
];

/** A decline or cancellation waiting for its optional reason. */
type ReasonPrompt =
  | { action: "decline" | "cancel"; request: MediaRequest }
  | { action: "bulk-decline"; requests: MediaRequest[]; onDone: BulkDone };

/** Told which requests of a bulk action failed, once every answer is in. */
type BulkDone = (failedIds: string[]) => void;

/**
 * The request queue: one tab per view with its count, a search and type
 * filter, and the rows of the chosen view with the actions it allows.
 * `?view=` names the view and `?user=` limits the rows to one account.
 */
export function RequestQueue() {
  const t = useT();
  const [searchParams, setSearchParams] = useSearchParams();
  const urlView = parseRequestQueueView(searchParams.get("view"));
  const userId = parseRequestQueueUser(searchParams.get("user"));
  const counts = useAdminRequestCounts();
  const refresh = useRefreshRequestQueue();

  // Without a view in the link, open on what needs approval, or on what is
  // under way when nothing does, and name that view in the link.
  const countsSettled = counts.data !== undefined || counts.isError;
  useEffect(() => {
    if (urlView || !countsSettled) return;
    const next = new URLSearchParams(searchParams);
    next.set("view", counts.data ? defaultRequestQueueView(counts.data) : "needs_approval");
    setSearchParams(next, { replace: true });
  }, [urlView, countsSettled, counts.data, searchParams, setSearchParams]);

  const [search, setSearch] = useState("");
  const q = useDebounce(search.trim(), SEARCH_DEBOUNCE_MS);
  const [mediaType, setMediaType] = useState<RequestQueueMediaFilter>("all");
  const users = useAdminUsers();
  const usernames = useMemo(
    () => new Map((users.data ?? []).map((user) => [user.id, user.username])),
    [users.data],
  );

  const queueFilter = (view: RequestQueueView) => ({
    view,
    q,
    mediaType: mediaType === "all" ? undefined : mediaType,
    requestedByUserId: userId,
  });

  // The dialog holds the open request's id, not a copy: it shows the view's
  // current row, or this page's own action's answer when that is newer (the
  // request may have left the view). When neither exists, because another
  // admin moved the request out of the view first, the dialog closes rather
  // than offer actions that no longer apply.
  const [opened, setOpened] = useState<{ id: string; acted?: MediaRequest } | null>(null);
  const openedRows = useAdminRequestQueue(queueFilter(urlView ?? "needs_approval"), {
    enabled: false,
  });
  const openRequest = opened
    ? newerRequest(
        openedRows.data?.pages.flatMap((page) => page.items).find((row) => row.id === opened.id),
        opened.acted,
      )
    : null;
  if (opened !== null && openRequest === null) setOpened(null);
  const [prompt, setPrompt] = useState<ReasonPrompt | null>(null);
  const [approvePrompt, setApprovePrompt] = useState<ApprovePrompt | null>(null);
  const [busy, setBusy] = useState<ReadonlyMap<string, RequestQueueAction>>(() => new Map());
  const approve = useApproveMediaRequest();
  const decline = useDeclineMediaRequest();
  const retry = useRetryMediaRequest();
  const cancel = useAdminCancelMediaRequest();
  const bulk = useBulkRequestAction();

  function updateParams(change: (params: URLSearchParams) => void) {
    const next = new URLSearchParams(searchParams);
    change(next);
    setSearchParams(next, { replace: true });
  }

  /** Runs one request's action, keeping its buttons busy until the server answers. */
  function track(
    request: MediaRequest,
    action: RequestQueueAction,
    send: () => Promise<MediaRequest>,
  ) {
    setBusy((current) => new Map(current).set(request.id, action));
    send()
      .then((updated) => {
        // The dialog follows the request, which may have left this view.
        setOpened((open) => (open?.id === updated.id ? { id: open.id, acted: updated } : open));
      })
      // The mutation already said what went wrong. Whatever the dialog knew
      // is now suspect: it shows the refetched row, or closes.
      .catch(() => setOpened((open) => (open?.id === request.id ? { id: open.id } : open)))
      .finally(() =>
        setBusy((current) => {
          const next = new Map(current);
          next.delete(request.id);
          return next;
        }),
      );
  }

  /**
   * Approves a request, first asking for the download server settings when
   * its server has any to choose. A server whose choices cannot be read is
   * approved with its own settings.
   */
  function approveRequest(request: MediaRequest) {
    track(request, "approve", async () => {
      const options = await listAdminRequestOptionsV2(request.id).catch(() => []);
      if (!options || options.length === 0) return approve.mutateAsync({ id: request.id });
      // A prompt already open keeps the dialog: replacing it would put this
      // request under settings chosen for the other. Approve is pressed
      // again once that dialog closes.
      setApprovePrompt((open) => open ?? { request, options });
      return request;
    });
  }

  const handlers: RequestQueueActionHandlers = {
    locked: bulk.isRunning,
    busyAction: (id) => busy.get(id),
    run: (action: RequestQueueAction, request: MediaRequest) => {
      switch (action) {
        case "approve":
          return approveRequest(request);
        case "retry":
          return track(request, action, () => retry.mutateAsync(request.id));
        case "decline":
        case "cancel":
          return setPrompt({ action, request });
      }
    },
  };

  function confirmPrompt(reason: string) {
    if (!prompt) return;
    const note = reason || undefined;
    if (prompt.action === "bulk-decline") {
      const { onDone } = prompt;
      bulk.run(
        { action: "decline", requests: prompt.requests, reason: note },
        { onSuccess: ({ failures }) => onDone(failures.map((failure) => failure.id)) },
      );
    } else {
      const { request } = prompt;
      track(request, prompt.action, () =>
        (prompt.action === "decline" ? decline : cancel).mutateAsync({
          id: request.id,
          reason: note,
        }),
      );
    }
    setPrompt(null);
  }

  const username = (id?: number) => (id === undefined ? undefined : usernames.get(id));
  const filterByUser = (id: number) => updateParams((params) => params.set("user", String(id)));
  const filtered = q !== "" || mediaType !== "all" || userId !== undefined;

  return (
    <div className="space-y-4">
      <Tabs
        value={urlView ?? ""}
        onValueChange={(value) => updateParams((params) => params.set("view", value))}
        className="gap-4"
      >
        <div className="-mx-4 overflow-x-auto px-4 pb-1 [scrollbar-width:thin] sm:mx-0 sm:px-0">
          <TabsList aria-label={t("web.admin.requests.queue.viewsLabel")} className="w-max">
            {REQUEST_QUEUE_VIEWS.map((view) => (
              <TabsTrigger key={view.value} value={view.value} className="px-3">
                {t(view.label)} <ViewCount view={view.value} counts={counts.data} />
              </TabsTrigger>
            ))}
          </TabsList>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-[220px] flex-1 sm:max-w-sm">
            <Search
              className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2"
              aria-hidden="true"
            />
            <Input
              type="search"
              aria-label={t("web.admin.requests.queue.searchLabel")}
              placeholder={t("web.admin.requests.queue.searchPlaceholder")}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              className="h-8 ps-9 text-[13px]"
            />
          </div>
          <div
            role="group"
            aria-label={t("web.admin.requests.queue.mediaTypeLabel")}
            className="flex gap-1"
          >
            {MEDIA_FILTERS.map((option) => (
              <Button
                key={option.value}
                type="button"
                size="sm"
                variant={mediaType === option.value ? "secondary" : "ghost"}
                aria-pressed={mediaType === option.value}
                onClick={() => setMediaType(option.value)}
              >
                {t(option.label)}
              </Button>
            ))}
          </div>
          {userId !== undefined ? (
            <Badge variant="outline" className="h-7 gap-1.5 pe-1 text-xs">
              {t("web.admin.requests.queue.requestedByFilter", {
                name: username(userId) ?? accountFallback(t, userId),
              })}
              <button
                type="button"
                aria-label={t("web.admin.requests.queue.clearUserFilter")}
                className="hover:bg-accent rounded-sm p-0.5"
                onClick={() => updateParams((params) => params.delete("user"))}
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ) : null}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="ms-auto"
            disabled={refresh.isRefreshing}
            onClick={refresh.refresh}
          >
            <RefreshCw aria-hidden="true" className={cn(refresh.isRefreshing && "animate-spin")} />
            {t("web.admin.requests.queue.refresh")}
          </Button>
        </div>

        {urlView ? (
          REQUEST_QUEUE_VIEWS.map((view) => (
            <TabsContent key={view.value} value={view.value}>
              <QueueList
                view={view.value}
                emptyText={t(view.empty)}
                filter={queueFilter(view.value)}
                filtered={filtered}
                username={username}
                handlers={handlers}
                bulk={bulk}
                onOpen={(request) => setOpened({ id: request.id })}
                onFilterUser={filterByUser}
                onBulkDecline={(requests, onDone) =>
                  setPrompt({ action: "bulk-decline", requests, onDone })
                }
              />
            </TabsContent>
          ))
        ) : (
          <RowsSkeleton />
        )}
      </Tabs>

      <RequestQueueDialog
        request={openRequest}
        requesterName={username(openRequest?.requested_by_user_id)}
        handlers={handlers}
        onOpenChange={(open) => {
          if (!open) setOpened(null);
        }}
      />
      <ApproveOptionsDialog
        prompt={approvePrompt}
        onConfirm={(request, options) =>
          track(request, "approve", () => approve.mutateAsync({ id: request.id, options }))
        }
        onOpenChange={(open) => {
          if (!open) setApprovePrompt(null);
        }}
      />
      <ReasonDialog
        open={prompt !== null}
        {...reasonDialogText(t, prompt)}
        pending={bulk.isRunning}
        onConfirm={confirmPrompt}
        onOpenChange={(open) => {
          if (!open) setPrompt(null);
        }}
      />
    </div>
  );
}

/** The later of two copies of one request, or null when there is neither. */
function newerRequest(a: MediaRequest | undefined, b: MediaRequest | undefined) {
  if (!a || !b) return a ?? b ?? null;
  return Date.parse(b.updated_at) > Date.parse(a.updated_at) ? b : a;
}

/** An account's name when its username isn't known: "User 7". */
function accountFallback(t: Translate, id: number) {
  return t("web.admin.requests.userFallback", { id: id });
}

function reasonDialogText(t: Translate, prompt: ReasonPrompt | null) {
  if (prompt?.action === "bulk-decline") {
    const n = prompt.requests.length;
    const values = { count: n, count_text: n };
    return {
      title: t("web.admin.requests.reasonDialog.bulkDecline.title", values),
      description: t("web.admin.requests.reasonDialog.bulkDecline.description", values),
      confirmLabel: t("web.admin.requests.reasonDialog.bulkDecline.confirm", values),
    };
  }
  if (prompt?.action === "cancel" && prompt.request.outcome === "failed") {
    return {
      title: t("web.admin.requests.reasonDialog.close.title"),
      description: t("web.admin.requests.reasonDialog.close.description", {
        title: prompt.request.title,
      }),
      confirmLabel: t("web.admin.requests.reasonDialog.close.confirm"),
    };
  }
  if (prompt?.action === "cancel") {
    return {
      title: t("web.admin.requests.reasonDialog.cancel.title"),
      description: t("web.admin.requests.reasonDialog.cancel.description", {
        title: prompt.request.title,
      }),
      confirmLabel: t("web.admin.requests.reasonDialog.cancel.confirm"),
    };
  }
  return {
    title: t("web.admin.requests.reasonDialog.decline.title"),
    description: prompt
      ? t("web.admin.requests.reasonDialog.decline.description", { title: prompt.request.title })
      : "",
    confirmLabel: t("web.admin.requests.reasonDialog.decline.confirm"),
  };
}

function ViewCount({
  view,
  counts,
}: {
  view: RequestQueueView;
  counts: AdminRequestCounts | undefined;
}) {
  if (!counts) return null;
  const count = counts[view];
  return (
    <span
      className={cn(
        "rounded-full px-1.5 text-xs tabular-nums",
        count > 0 && view === "needs_approval"
          ? "bg-primary text-primary-foreground"
          : count > 0 && view === "failed"
            ? "bg-destructive text-white"
            : "bg-foreground/10",
      )}
    >
      {count}
    </span>
  );
}

function QueueList({
  view,
  emptyText,
  filter,
  filtered,
  username,
  handlers,
  bulk,
  onOpen,
  onFilterUser,
  onBulkDecline,
}: {
  view: RequestQueueView;
  emptyText: string;
  filter: Parameters<typeof useAdminRequestQueue>[0];
  filtered: boolean;
  username: (id?: number) => string | undefined;
  handlers: RequestQueueActionHandlers;
  bulk: ReturnType<typeof useBulkRequestAction>;
  onOpen: (request: MediaRequest) => void;
  onFilterUser: (id: number) => void;
  onBulkDecline: (requests: MediaRequest[], onDone: BulkDone) => void;
}) {
  const t = useT();
  const queue = useAdminRequestQueue(filter);
  const rows = useMemo(() => queue.data?.pages.flatMap((page) => page.items) ?? [], [queue.data]);
  // A column per setting someone chose for a request on this page, such as
  // a quality profile; none when nothing was chosen.
  const optionColumns = useMemo(() => requestOptionColumns(rows), [rows]);
  const columnsStyle = queueColumnsStyle(optionColumns.length);
  const selectable = view === "needs_approval";
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set());
  const selectedRows = rows.filter((row) => selected.has(row.id));
  // The ones that failed stay selected, so they can be tried again.
  const keepFailed: BulkDone = (failedIds) => setSelected(new Set(failedIds));

  function toggle(id: string, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function approveSelected() {
    bulk.run(
      { action: "approve", requests: selectedRows },
      { onSuccess: ({ failures }) => keepFailed(failures.map((failure) => failure.id)) },
    );
  }

  if (queue.isPending) return <RowsSkeleton />;
  if (queue.isError && rows.length === 0) {
    return (
      <EmptyPanel
        title={t("web.admin.requests.queue.loadFailed.title")}
        detail={
          queue.error instanceof Error
            ? queue.error.message
            : t("web.admin.requests.queue.loadFailed.detail")
        }
      >
        <Button variant="outline" size="sm" onClick={() => void queue.refetch()}>
          {t("common.tryAgain")}
        </Button>
      </EmptyPanel>
    );
  }

  const allSelected = rows.length > 0 && selectedRows.length === rows.length;

  return (
    <div className="space-y-3">
      {selectable ? (
        <BulkBar
          rows={rows}
          selectedRows={selectedRows}
          allSelected={allSelected}
          bulk={bulk}
          onSelectAll={(checked) => setSelected(new Set(checked ? rows.map((row) => row.id) : []))}
          onApprove={approveSelected}
          onDecline={() => onBulkDecline(selectedRows, keepFailed)}
        />
      ) : null}

      {rows.length === 0 ? (
        <EmptyPanel
          title={
            filtered
              ? t("web.admin.requests.queue.noMatches.title")
              : t("web.admin.requests.queue.allClear")
          }
          detail={filtered ? t("web.admin.requests.queue.noMatches.detail") : emptyText}
        />
      ) : (
        <div
          className={cn(
            "border-border bg-card overflow-hidden rounded-lg border transition-opacity",
            queue.isPlaceholderData && "opacity-60",
          )}
        >
          <div className="border-border text-muted-foreground hidden items-center gap-3 border-b px-4 py-2 text-xs font-medium lg:flex">
            {selectable ? <span className="w-4" /> : null}
            <span className="w-10" />
            <div className="grid flex-1 grid-cols-(--queue-columns) gap-x-6" style={columnsStyle}>
              <span>{t("web.admin.requests.queue.column.request")}</span>
              <span>{t("web.admin.requests.queue.column.state")}</span>
              <span>{t("web.admin.requests.queue.column.servers")}</span>
              {optionColumns.map((column) => (
                <span key={column} className="truncate">
                  {column}
                </span>
              ))}
            </div>
            <span className="w-60 shrink-0 text-end">
              <span className="sr-only">{t("web.admin.requests.queue.column.actions")}</span>
            </span>
          </div>
          <ul
            aria-label={t("web.admin.requests.queue.listLabel")}
            className="divide-border divide-y"
          >
            {rows.map((request) => (
              <QueueRow
                key={request.id}
                request={request}
                requester={username(request.requested_by_user_id)}
                optionColumns={optionColumns}
                selectable={selectable}
                selected={selected.has(request.id)}
                handlers={handlers}
                onSelect={(checked) => toggle(request.id, checked)}
                onOpen={() => onOpen(request)}
                onFilterUser={onFilterUser}
              />
            ))}
          </ul>
        </div>
      )}

      {queue.isError && rows.length > 0 ? (
        <p role="alert" className="text-destructive text-sm">
          {queue.error instanceof Error
            ? queue.error.message
            : t("web.admin.requests.queue.moreFailed")}
        </p>
      ) : null}
      {queue.hasNextPage ? (
        <Button
          variant="outline"
          disabled={queue.isFetchingNextPage}
          onClick={() => void queue.fetchNextPage()}
        >
          {queue.isFetchingNextPage
            ? t("web.admin.requests.queue.loadingMore")
            : t("web.admin.requests.queue.loadMore")}
        </Button>
      ) : null}
    </div>
  );
}

function BulkBar({
  rows,
  selectedRows,
  allSelected,
  bulk,
  onSelectAll,
  onApprove,
  onDecline,
}: {
  rows: MediaRequest[];
  selectedRows: MediaRequest[];
  allSelected: boolean;
  bulk: ReturnType<typeof useBulkRequestAction>;
  onSelectAll: (checked: boolean) => void;
  onApprove: () => void;
  onDecline: () => void;
}) {
  const t = useT();
  const count = selectedRows.length;
  const countValues = { count, count_text: count };
  const someSelected = count > 0 && !allSelected;
  return (
    <div className="space-y-2">
      <div className="flex min-h-9 flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            className="accent-primary size-4 cursor-pointer"
            checked={allSelected}
            ref={(element) => {
              if (element) element.indeterminate = someSelected;
            }}
            disabled={rows.length === 0 || bulk.isRunning}
            onChange={(event) => onSelectAll(event.target.checked)}
          />
          {count > 0
            ? t("web.admin.requests.bulk.selected", countValues)
            : t("web.admin.requests.bulk.selectAll", {
                count: rows.length,
                count_text: rows.length,
              })}
        </label>
        {count > 0 ? (
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={bulk.isRunning} onClick={onApprove}>
              {t("web.admin.requests.bulk.approve", countValues)}
            </Button>
            <Button size="sm" variant="outline" disabled={bulk.isRunning} onClick={onDecline}>
              {t("web.admin.requests.reasonDialog.bulkDecline.confirm", countValues)}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={bulk.isRunning}
              onClick={() => onSelectAll(false)}
            >
              {t("web.admin.requests.bulk.clear")}
            </Button>
          </div>
        ) : null}
      </div>
      {bulk.progress ? (
        <BulkProgress progress={bulk.progress} running={bulk.isRunning} onDismiss={bulk.reset} />
      ) : null}
    </div>
  );
}

/** Clicks on the row's own controls do their own thing; anywhere else opens the dialog. */
function fromControl(event: MouseEvent) {
  return (event.target as HTMLElement).closest("a, button, input, label") !== null;
}

function QueueRow({
  request,
  requester,
  optionColumns,
  selectable,
  selected,
  handlers,
  onSelect,
  onOpen,
  onFilterUser,
}: {
  request: MediaRequest;
  requester?: string;
  /** The chosen settings shown as columns, by label. */
  optionColumns: readonly string[];
  selectable: boolean;
  selected: boolean;
  handlers: RequestQueueActionHandlers;
  onSelect: (checked: boolean) => void;
  onOpen: () => void;
  onFilterUser: (id: number) => void;
}) {
  const t = useT();
  const requestedAgo = formatRelativeTime(request.created_at, { absoluteAfterDays: 30 });
  const userId = request.requested_by_user_id;
  return (
    <li
      className={cn(
        "hover:bg-accent/40 flex cursor-pointer flex-wrap items-start gap-3 px-4 py-3 transition-colors lg:flex-nowrap",
        selected && "bg-accent/30",
      )}
      onClick={(event) => {
        if (!fromControl(event)) onOpen();
      }}
    >
      {selectable ? (
        <div className="pt-3">
          <BulkSelectionCheckbox
            label={t("web.admin.requests.queue.selectRequest", { title: request.title })}
            selected={selected}
            onSelectionChange={(checked) => onSelect(checked)}
          />
        </div>
      ) : null}
      <RequestPoster request={request} className="w-10" />
      <div
        className="grid min-w-0 flex-1 gap-x-6 gap-y-2 lg:grid-cols-(--queue-columns)"
        style={queueColumnsStyle(optionColumns.length)}
      >
        <div className="min-w-0 space-y-1">
          <Link
            to={requestDetailHref(request.media_type, request.tmdb_id)}
            className="block truncate font-medium hover:underline"
          >
            {request.title}
          </Link>
          <p className="text-muted-foreground text-xs">
            {[
              request.year ? numberText(request.year) : null,
              formatMediaType(request.media_type),
              request.seasons?.length ? formatSeasonList(request.seasons) : null,
            ]
              .filter(Boolean)
              .join(" · ")}
          </p>
          <p className="text-muted-foreground text-xs">
            {userId !== undefined ? (
              <button
                type="button"
                className="hover:text-foreground hover:underline"
                title={t("web.admin.requests.queue.filterByUser")}
                onClick={() => onFilterUser(userId)}
              >
                {requester ?? accountFallback(t, userId)}
              </button>
            ) : (
              <span>{t("web.admin.requests.unknownAccount")}</span>
            )}
            {requestedAgo ? (
              <span title={formatDateTime(request.created_at)}>
                {SEPARATOR}
                {requestedAgo}
              </span>
            ) : null}
            {request.source === "watchlist" ? (
              <span>
                {SEPARATOR}
                {t("web.admin.requests.viaWatchlist")}
              </span>
            ) : null}
          </p>
        </div>
        <RequestStateSummary request={request} />
        <TargetList request={request} />
        <OptionCells request={request} columns={optionColumns} />
      </div>
      <div className="flex w-full items-center justify-end gap-2 lg:w-60 lg:shrink-0">
        <RequestActionButtons request={request} handlers={handlers} />
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8"
          aria-label={t("web.admin.requests.queue.details", { title: request.title })}
          onClick={onOpen}
        >
          <Maximize2 aria-hidden="true" />
        </Button>
      </div>
    </li>
  );
}
