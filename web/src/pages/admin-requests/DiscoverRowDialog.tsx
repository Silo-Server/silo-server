import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  isRequestEditorConflict,
  type DiscoverRow,
  type DiscoverRowBody,
} from "@/api/v2/adminRequests";
import { StepCount, StepDialog } from "@/components/calm/StepDialog";
import { MDBListBrowser } from "@/components/CollectionTemplateGallery/MDBListBrowser";
import { SourceCard } from "@/components/collections/fields/SourceCard";
import { TMDBChartField } from "@/components/collections/fields/TMDBChartField";
import { TMDBListURLField } from "@/components/collections/TMDBListURLField";
import { KindCard } from "@/components/homeRows/addRow/RowCard";
import { MoreOptions } from "@/components/homeRows/addRow/MoreOptions";
import { RowPreview } from "@/components/homeRows/addRow/RowPreview";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useAdminCollectionCapabilities } from "@/hooks/queries/admin/collections";
import { useAdminRequestCapabilities } from "@/hooks/queries/admin/requests";
import { useDiscoverRowPreview } from "@/hooks/queries/admin/requestDiscover";
import { RowRefusedError, type DiscoverRowsAdapter } from "@/hooks/queries/admin/useDiscoverRows";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { RowChangedError } from "@/lib/homeRows/types";
import { useCollectionTemplates } from "@/lib/collectionTemplates";
import {
  cleanMDBListLink,
  isMDBListLink,
  popularPicks,
  shownLink,
  type SyncedPick,
} from "@/lib/collections/synced";
import { chartSummary } from "@/lib/collections/tmdbSources";
import { isValidTMDBListURL } from "@/lib/tmdbList";
import { cn } from "@/lib/utils";

import { DiscoverFilterFields, Field, OptionSelect } from "./DiscoverFilterFields";
import {
  DEFAULT_ROW_LIMIT,
  defaultTitle,
  fromTMDBChart,
  isBrandRow,
  isBuiltInRow,
  MAX_ROW_LIMIT,
  MDBLIST_ORDERS,
  mdblistOrder,
  type MDBListOrder,
  missingStockRows,
  moreFilterCount,
  NEW_ROW_TYPES,
  newRow,
  rowBody,
  rowProblem,
  ROW_KINDS,
  savedTitle,
  toTMDBChart,
} from "./discoverLayoutModel";

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

/** What a stock row shows, on its Bring back card. */
function stockSentence(row: DiscoverRowBody) {
  return row.chart
    ? `TMDB chart: ${chartSummary(toTMDBChart(row.chart))}.`
    : ROW_KINDS[row.type].sentence;
}

/** Step 1: a new row of a type, or a built-in or brand row the page dropped. */
function KindPicker({
  missing,
  narrow,
  onPick,
}: {
  missing: DiscoverRowBody[];
  narrow: boolean;
  onPick: (row: DiscoverRowBody) => void;
}) {
  const grid = cn("grid gap-2.5", !narrow && "grid-cols-2");
  return (
    <div className="grid content-start gap-6 overflow-y-auto px-5 pt-[18px] pb-6 sm:px-7">
      <section className="grid gap-3">
        <h3 className="text-[13px] font-semibold">New row</h3>
        <div className={grid}>
          {NEW_ROW_TYPES.map((type) => (
            <KindCard
              key={type}
              {...ROW_KINDS[type]}
              compact={narrow}
              onPick={() => onPick(newRow(type))}
            />
          ))}
        </div>
      </section>
      {missing.length > 0 ? (
        <section className="grid gap-3">
          <h3 className="flex items-baseline gap-2.5 text-[13px] font-semibold">
            Bring back
            <span className="text-muted-foreground font-normal">
              Rows Discover starts with that this page no longer has
            </span>
          </h3>
          <div className={grid}>
            {missing.map((row) => (
              <KindCard
                key={row.id}
                {...ROW_KINDS[row.type]}
                label={row.title}
                sentence={stockSentence(row)}
                compact={narrow}
                onPick={() => onPick({ ...row })}
              />
            ))}
          </div>
        </section>
      ) : null}
    </div>
  );
}

/**
 * Where a row's titles come from. A new row picks its source here; a saved
 * one shows it on a card with Change, as a saved synced collection does.
 */
function SourceControl({
  row,
  editing,
  onChange,
  onPick,
}: {
  row: DiscoverRowBody;
  editing: boolean;
  onChange: (row: DiscoverRowBody) => void;
  /** An MDBList pick, which also names the row. */
  onPick: (pick: SyncedPick) => void;
}) {
  const id = useId();
  const [changing, setChanging] = useState(!editing);
  const picker = useRef<HTMLDivElement>(null);
  // Change chart / Change list moves focus into the picker it opens.
  useEffect(() => {
    if (changing && editing) {
      picker.current
        ?.querySelector<HTMLElement>('[role="radio"][aria-checked="true"], input, [role="radio"]')
        ?.focus();
    }
  }, [changing, editing]);
  // The list link as typed; the row keeps it only once it is a list link.
  const [typedLink, setTypedLink] = useState(editing ? (row.list?.url ?? "") : "");
  const [pickId, setPickId] = useState<string>();
  const serverCapabilities = useAdminCollectionCapabilities(row.type === "mdblist");
  const templates = useCollectionTemplates(row.type === "mdblist");
  const picks = useMemo(
    () =>
      // A row reads its list on Discover's own schedule, not the template's.
      popularPicks(templates.data?.categories ?? [], ["mdblist"]).map((group) => ({
        ...group,
        templates: group.templates.map((template) => ({
          ...template,
          default_sync_schedule: undefined,
        })),
      })),
    [templates.data],
  );
  const kind = ROW_KINDS[row.type];

  if (isBrandRow(row)) return <SourceCard title={kind.label} detail={kind.sentence} />;
  if (row.chart && isBuiltInRow(row)) {
    return (
      <div className="grid gap-2">
        <SourceCard title={kind.label} detail={chartSummary(toTMDBChart(row.chart))} />
        <p className="text-muted-foreground text-[13px]">
          A built-in row keeps its chart, so apps from before custom rows still show it.
        </p>
      </div>
    );
  }
  if (row.discover) return null;
  if (!changing) {
    const detail = row.chart
      ? chartSummary(toTMDBChart(row.chart))
      : shownLink(row.list?.url ?? "");
    return (
      <SourceCard
        title={kind.label}
        detail={detail}
        action={row.chart ? "Change chart" : "Change list"}
        onChange={() => setChanging(true)}
      />
    );
  }
  let field: ReactNode;
  if (row.chart) {
    field = (
      <TMDBChartField
        chart={toTMDBChart(row.chart)}
        onChart={(chart) => onChange({ ...row, chart: fromTMDBChart(chart) })}
      />
    );
  } else if (row.type === "tmdb_list") {
    field = (
      <TMDBListURLField
        id={`${id}-tmdb-list`}
        label="TMDB list link"
        help="Paste a public list from themoviedb.org. Silo reads it again about every 12 hours."
        value={typedLink}
        onChange={(raw) => {
          setTypedLink(raw);
          onChange({ ...row, list: { url: isValidTMDBListURL(raw) ? raw.trim() : "" } });
        }}
      />
    );
  } else {
    field = (
      <MDBListBrowser
        scopeKind="server"
        picks={picks}
        searchEnabled={serverCapabilities.data?.mdblist_search ?? false}
        selectedId={pickId}
        onPick={(pick) => {
          if (pick.list?.source !== "mdblist") return;
          setPickId(pick.list.pickId);
          setTypedLink("");
          onPick(pick);
        }}
        link={typedLink}
        onLinkChange={(raw) => {
          const url = cleanMDBListLink(raw);
          setTypedLink(raw);
          setPickId(undefined);
          onChange({ ...row, list: { ...row.list, url: isMDBListLink(url) ? url : "" } });
        }}
      />
    );
  }
  return (
    <div ref={picker} className="contents">
      {field}
    </div>
  );
}

/**
 * Step 2 and Edit row, laid out like a Home row's: the live preview, where
 * the titles come from, the row's name, then More options. Filter and list
 * rows put their control above the preview, as Home's rule rows do.
 */
function RowFields({
  row,
  editing,
  titleTyped,
  onChange,
  onPick,
}: {
  row: DiscoverRowBody;
  editing: boolean;
  titleTyped: boolean;
  onChange: (row: DiscoverRowBody, typedTitle?: boolean) => void;
  onPick: (pick: SyncedPick) => void;
}) {
  const nameId = useId();
  const hideId = useId();
  // A server from before list orders refuses list.order, so the choice waits on its capability.
  const listOrders = useAdminRequestCapabilities().data?.discover_list_order ?? false;
  const kind = ROW_KINDS[row.type];
  const brand = isBrandRow(row);
  // The server needs a name to preview a row, but the name doesn't change its
  // titles: a fixed one keeps typing a name from reading the source again.
  const previewRow = { ...row, title: kind.label };
  const preview = useDiscoverRowPreview(brand || rowProblem(previewRow) ? null : previewRow);
  let offText = "Pick a chart to see its titles.";
  if (row.list) offText = "Pick a list or paste its link to see its titles.";
  if (row.discover) offText = "Choose the region the streaming services are in.";
  const previewStrip = brand ? null : (
    <RowPreview
      title={savedTitle(row)}
      sectionType={row.type}
      icon={kind.icon}
      group={kind.group}
      state={preview}
      liveLabel={row.list ? "Live preview of the list" : "Live preview from TMDB"}
      offText={offText}
    />
  );
  const control = <SourceControl row={row} editing={editing} onChange={onChange} onPick={onPick} />;
  const controlFirst = Boolean(row.list || row.discover);
  const moreCount = row.discover ? moreFilterCount(row.discover) : 0;
  const summary = [
    moreCount ? `${moreCount} more ${moreCount === 1 ? "filter" : "filters"}` : null,
    row.hide_available ? "Missing titles only" : null,
  ]
    .filter(Boolean)
    .join(" · ");
  let nameHelp: string | null = null;
  if (!titleTyped && row.chart) nameHelp = "Follows the chart until you type your own name.";
  else if (!titleTyped && row.type === "mdblist") nameHelp = "Starts as the list's name.";

  return (
    // minmax(0,1fr): a truncated line or a wide control can't widen the form past a phone screen.
    <div className="grid grid-cols-[minmax(0,1fr)] gap-5">
      {controlFirst ? (
        <>
          {control}
          {row.discover ? (
            <DiscoverFilterFields
              slot="primary"
              filter={row.discover}
              onChange={(discover) => onChange({ ...row, discover })}
            />
          ) : null}
          {row.type === "mdblist" && row.list && listOrders ? (
            <Field
              label="Order"
              help="MDBList's feed doesn't carry the sort saved on the list's page. Pick the one it shows."
            >
              <OptionSelect
                label="Order"
                value={mdblistOrder(row)}
                options={MDBLIST_ORDERS}
                onChange={(order) =>
                  onChange({
                    ...row,
                    list: {
                      url: row.list?.url ?? "",
                      order: order === "list" ? undefined : (order as MDBListOrder),
                    },
                  })
                }
              />
            </Field>
          ) : null}
          {previewStrip}
        </>
      ) : (
        <>
          {previewStrip}
          {control}
        </>
      )}
      <div className="grid gap-2">
        <Label htmlFor={nameId}>Row name</Label>
        <Input
          id={nameId}
          className="h-10 text-[15px]"
          maxLength={80}
          value={row.title}
          placeholder={defaultTitle(row) || "Name this row"}
          onChange={(event) => onChange({ ...row, title: event.target.value }, true)}
        />
        {nameHelp ? <p className="text-muted-foreground text-[13px]">{nameHelp}</p> : null}
      </div>
      {brand ? null : (
        <MoreOptions
          itemLimit={row.limit ?? DEFAULT_ROW_LIMIT}
          summary={summary || undefined}
          onItemLimitChange={(limit) => onChange({ ...row, limit })}
        >
          {row.discover ? (
            <DiscoverFilterFields
              slot="more"
              filter={row.discover}
              onChange={(discover) => onChange({ ...row, discover })}
            />
          ) : null}
          <div className="flex items-center justify-between gap-4">
            <div>
              <Label htmlFor={hideId}>Leave out titles the library has</Label>
              <p className="text-muted-foreground mt-1 text-[13px]">
                Viewers see only titles they can still request.
              </p>
            </div>
            <Switch
              id={hideId}
              checked={row.hide_available ?? false}
              onCheckedChange={(checked) => onChange({ ...row, hide_available: checked === true })}
            />
          </div>
        </MoreOptions>
      )}
    </div>
  );
}

/**
 * Add row (step 1: what the row shows; step 2: preview, source, name, More
 * options) and Edit row (step 2 with Delete row…) for Requests > Discover
 * Page, in Home rows' step dialog. Saving writes the layout right away.
 */
export function DiscoverRowDialog({
  adapter,
  editRow,
  onClose,
  onSaved,
  onDelete,
}: {
  adapter: DiscoverRowsAdapter;
  /** Edit row when set, Add row otherwise. */
  editRow: DiscoverRow | null;
  onClose: () => void;
  /** After a row is added or saved, with the new row's id. */
  onSaved: (newId: string | undefined) => void;
  onDelete: (row: DiscoverRow) => void;
}) {
  const editing = editRow !== null;
  const narrow = useMediaQuery("(max-width: 1023px)");
  const [step, setStep] = useState<"pick" | "form">(editing ? "form" : "pick");
  const [row, setRow] = useState<DiscoverRowBody | null>(editRow ? rowBody(editRow) : null);
  // The stored row an edit started from; a save refuses when another admin
  // changed it since. Reload rows moves it to the row as it is now.
  const [base, setBase] = useState<DiscoverRowBody | null>(editRow ? rowBody(editRow) : null);
  const [rebase, setRebase] = useState(false);
  if (rebase && editRow) {
    setRebase(false);
    const stored = adapter.discoverRows.find((entry) => entry.id === editRow.id);
    setBase(stored ? rowBody(stored) : null);
  }
  const [titleTyped, setTitleTyped] = useState(editing);
  // Counts picks, so each one starts its source control afresh.
  const [picks, setPicks] = useState(0);
  const [conflict, setConflict] = useState(false);
  const [problems, setProblems] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const missing = useMemo(() => missingStockRows(adapter.discoverRows), [adapter.discoverRows]);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  function update(next: DiscoverRowBody, typedTitle = false) {
    setRow(next);
    setProblems([]);
    if (typedTitle) setTitleTyped(next.title.trim() !== "");
  }

  function pick(next: DiscoverRowBody) {
    setRow(next);
    setTitleTyped(false);
    setProblems([]);
    setPicks((count) => count + 1);
    setStep("form");
    headingRef.current?.focus();
  }

  function pickList(listPick: SyncedPick) {
    if (!row || listPick.list?.source !== "mdblist") return;
    const limit = listPick.limit ? Math.min(listPick.limit, MAX_ROW_LIMIT) : row.limit;
    update({
      ...row,
      list: { ...row.list, url: listPick.list.url },
      limit,
      title: titleTyped ? row.title : listPick.name,
    });
  }

  async function submit() {
    if (!row || busy || conflict || rowProblem(row)) return;
    setBusy(true);
    setProblems([]);
    try {
      const id = await adapter.saveRow({ ...row, title: savedTitle(row) }, base);
      if (!mounted.current) return;
      onSaved(editing ? undefined : id);
      onClose();
    } catch (error) {
      if (!mounted.current) return;
      if (isRequestEditorConflict(error) || error instanceof RowChangedError) setConflict(true);
      else if (error instanceof RowRefusedError) setProblems(error.problems);
      else
        toast.error(
          errorMessage(error, editing ? "Could not save this row" : "Could not add this row"),
        );
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  async function reload() {
    setBusy(true);
    try {
      await adapter.reload();
      if (mounted.current) {
        setConflict(false);
        setRebase(true);
      }
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  let title = "Add a row to Discover";
  let description: ReactNode =
    "Pick what the row shows. Next you'll see a preview and can name it.";
  if (step === "form" && editing) {
    title = "Edit row";
    description = "Changes apply to everyone's Discover page.";
  } else if (step === "form" && row) {
    // A row brought back is named by its own title.
    title = row.id ? row.title : ROW_KINDS[row.type].label;
    description = row.id ? stockSentence(row) : ROW_KINDS[row.type].sentence;
  }

  let footerStart: ReactNode = (
    <StepCount step={step === "pick" ? 1 : 2}>
      {step === "pick" ? "New rows go to the bottom of Discover" : "Goes to the bottom of Discover"}
    </StepCount>
  );
  if (editRow) {
    footerStart = (
      <Button
        type="button"
        variant="ghost"
        className="text-destructive hover:text-destructive -ml-3"
        disabled={busy}
        onClick={() => onDelete(editRow)}
      >
        <Trash2 aria-hidden className="size-4" />
        Delete row…
      </Button>
    );
  }

  const notice =
    conflict || problems.length > 0 ? (
      <div
        role="alert"
        className={cn(
          "flex items-center gap-3 border-t px-5 py-3 text-sm sm:px-7",
          conflict ? "border-warning/40 bg-warning/10" : "border-destructive/40 bg-destructive/10",
        )}
      >
        <p className="min-w-0 flex-1">
          {conflict
            ? "The Discover page changed since you opened this row. Your changes are kept. Reload the rows, then save again."
            : problems.join(" ")}
        </p>
        {conflict ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => void reload()}
          >
            Reload rows
          </Button>
        ) : null}
      </div>
    ) : null;

  return (
    <StepDialog
      size={step === "pick" ? "choice" : "form"}
      onClose={onClose}
      onOpenFocus={() => headingRef.current?.focus()}
      back={
        step === "form" && !editing ? { label: "All rows", onClick: () => setStep("pick") } : null
      }
      title={title}
      titleRef={headingRef}
      description={description}
      notice={notice}
      footerStart={footerStart}
      actions={
        step === "form" ? (
          <Button
            type="button"
            disabled={busy || conflict || !row || rowProblem(row) !== null}
            onClick={() => void submit()}
          >
            {editing ? (
              "Save"
            ) : (
              <>
                <Plus aria-hidden className="size-4" />
                Add row
              </>
            )}
          </Button>
        ) : null
      }
    >
      {step === "pick" ? (
        <div className="border-border flex min-h-0 flex-1 flex-col border-t">
          <KindPicker missing={missing} narrow={narrow} onPick={pick} />
        </div>
      ) : row ? (
        <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-6 sm:px-7">
          <RowFields
            key={picks}
            row={row}
            editing={editing}
            titleTyped={titleTyped}
            onChange={update}
            onPick={pickList}
          />
        </div>
      ) : null}
    </StepDialog>
  );
}
