import { useEffect, useId, useMemo, useState } from "react";
import { Link } from "react-router";
import { Info, Lock, Pencil } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";

import { MDBListBrowser } from "@/components/CollectionTemplateGallery/MDBListBrowser";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioCardItem, RadioGroup } from "@/components/ui/radio-group";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useUserCollectionTemplates } from "@/hooks/queries/userCollectionImports";
import { useCollectionTemplates } from "@/lib/collectionTemplates";
import {
  ALL_MY_LIBRARIES_LABEL,
  CHANGE_LINK,
  CHART_RULES_NOTE,
  CHART_SET_WHEN_MADE,
  LINK_CHANGES_AT_NEXT_SYNC,
  LIST_DECIDES,
  LIST_DECIDES_ITEMS,
  MDBLIST_LINK,
  MDBLIST_LINK_INVALID,
  PASTE_TMDB_LIST_LINK,
  PERSONAL_SCHEDULE_LOCKED,
  SYNCED_CAPTION,
  SYNCED_HEADING,
  SYNCED_OFF,
  TMDB_LIST_HELP,
  TMDB_LIST_LINK,
  TRAKT_SCHEDULE_STOPPED,
  YOU_DECIDE,
  ineligibleLibrariesLine,
  youDecideItems,
} from "@/lib/collections/copy";
import type {
  CollectionDraft,
  CollectionView,
  ListDraft,
  ScopeKind,
} from "@/lib/collections/scope";
import { SERVER_SCOPE } from "@/lib/collections/scope";
import {
  applyPick,
  chartPick,
  cleanMDBListLink,
  eligibleLibraryKinds,
  isMDBListLink,
  linkPick,
  popularPicks,
  savedListMediaKind,
  TAB_OF_SOURCE,
  type ImportSource,
  type SyncedDraft,
  type SyncedList,
  type SyncedPick,
  type SyncedTab,
} from "@/lib/collections/synced";
import {
  CHART_MEDIA_LABEL,
  CHART_WINDOW_LABEL,
  chartHasTimeWindow,
  chartLockReason,
  chartMediaTypes,
  chartSummary,
  defaultChart,
  normalizeChart,
  TMDB_CHARTS,
  type TMDBChart,
  type TMDBChartMediaType,
  type TMDBChartPreset,
  type TMDBChartTimeWindow,
} from "@/lib/collections/tmdbSources";
import { isValidTMDBListURL } from "@/lib/tmdbList";

import { SYNCED_SOURCE_LABEL } from "@/lib/collections/types";

import { FranchiseIdField } from "../fields/FranchiseIdField";
import { LibrariesLine } from "../fields/LibrariesLine";
import { OrderBlock } from "../fields/OrderBlock";
import { ScheduleField } from "../fields/ScheduleField";
import { TMDBListURLField } from "../TMDBListURLField";
import { LockedSourceSummary } from "./LockedSourceSummary";
import { SyncFailedCallout, SyncStatusBlock } from "./SyncStatusBlock";

const TAB_LABEL: Record<SyncedTab, string> = {
  mdblist: "MDBList",
  tmdb_chart: "TMDB chart",
  tmdb_list: "TMDB list",
};
const TAB_ORDER: readonly SyncedTab[] = ["mdblist", "tmdb_chart", "tmdb_list"];

interface LibraryOption {
  id: number;
  name: string;
  type?: string;
}

/** Whether a library can hold the list's titles; mixed and untyped libraries hold anything. */
function fits(library: LibraryOption, kinds: readonly string[] | undefined) {
  return !kinds || !library.type || library.type === "mixed" || kinds.includes(library.type);
}

/** The ticked libraries that can hold titles of `kinds`; unknown ids stay. */
function fittingLibraryIds(
  libraryIds: number[],
  libraries: readonly LibraryOption[],
  kinds: readonly string[] | undefined,
) {
  return libraryIds.filter((libraryId) => {
    const library = libraries.find((entry) => entry.id === libraryId);
    return !library || fits(library, kinds);
  });
}

/** A row of mutually exclusive choices, as in Show: Movies / TV shows / Both. */
function Segmented<Value extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: Value;
  options: ReadonlyArray<{ value: Value; label: string; disabled?: boolean }>;
  onChange: (value: Value) => void;
}) {
  const id = useId();
  return (
    <div className="grid content-start gap-2">
      <span id={id} className="text-[14px] font-semibold">
        {label}
      </span>
      <RadioGroupPrimitive.Root
        aria-labelledby={id}
        value={value}
        onValueChange={(next) => onChange(next as Value)}
        orientation="horizontal"
        className="bg-muted/50 inline-flex w-fit gap-1 rounded-xl p-1"
      >
        {options.map((option) => (
          <RadioGroupPrimitive.Item
            key={option.value}
            value={option.value}
            disabled={option.disabled}
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 data-[state=checked]:bg-background data-[state=checked]:text-foreground h-9 rounded-lg px-3.5 text-[13.5px] font-medium outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-45"
          >
            {option.label}
          </RadioGroupPrimitive.Item>
        ))}
      </RadioGroupPrimitive.Root>
    </div>
  );
}

/** The TMDB chart tab: chart cards, then Show and "Trending over" as TMDB allows them. */
function ChartTab({
  chart,
  onChart,
  isServer,
}: {
  chart: TMDBChart | null;
  onChart: (chart: TMDBChart) => void;
  isServer: boolean;
}) {
  const id = useId();
  const allowed = chart ? chartMediaTypes(chart.preset) : [];
  const lock = chart ? chartLockReason(chart.preset) : null;
  const shows: TMDBChartMediaType[] =
    chart?.preset === "trending" ? ["movie", "tv", "all"] : ["movie", "tv"];
  return (
    <div className="grid gap-4">
      <div className="grid gap-2">
        <span id={`${id}-chart`} className="text-[14px] font-semibold">
          Chart
        </span>
        <RadioGroup
          aria-labelledby={`${id}-chart`}
          value={chart?.preset ?? ""}
          onValueChange={(value) => {
            const preset = value as TMDBChartPreset;
            onChart(chart ? normalizeChart({ ...chart, preset }) : defaultChart(preset));
          }}
          className="grid-cols-2 gap-2.5 sm:grid-cols-4"
        >
          {TMDB_CHARTS.map((entry) => (
            <RadioCardItem
              key={entry.preset}
              value={entry.preset}
              label={entry.label}
              hint={entry.hint}
            />
          ))}
        </RadioGroup>
      </div>
      {chart ? (
        <div className="grid gap-2">
          <div className="flex flex-wrap gap-x-8 gap-y-3">
            <Segmented
              label="Show"
              value={chart.mediaType}
              options={shows.map((value) => ({
                value,
                label: CHART_MEDIA_LABEL[value],
                disabled: !allowed.includes(value),
              }))}
              onChange={(mediaType) => onChart({ ...chart, mediaType })}
            />
            {chartHasTimeWindow(chart.preset) ? (
              <Segmented<TMDBChartTimeWindow>
                label="Trending over"
                value={chart.timeWindow ?? "day"}
                options={(["day", "week"] as const).map((value) => ({
                  value,
                  label: CHART_WINDOW_LABEL[value],
                }))}
                onChange={(timeWindow) => onChart({ ...chart, timeWindow })}
              />
            ) : null}
          </div>
          {lock ? (
            <p className="text-muted-foreground flex items-center gap-1.5 text-[13px]">
              <Lock aria-hidden className="size-3.5" />
              {lock}
            </p>
          ) : null}
        </div>
      ) : null}
      <p className="text-muted-foreground text-[13px]">
        {CHART_RULES_NOTE}
        {isServer ? (
          <>
            {" "}
            Genre and franchise sets come from{" "}
            <Link
              to={`${SERVER_SCOPE.paths.list()}?dialog=starter-packs`}
              className="text-foreground font-medium underline underline-offset-4"
            >
              Starter packs
            </Link>
            .
          </>
        ) : null}
      </p>
    </div>
  );
}

/**
 * A new Synced list: the list it follows, picked from MDBList (search,
 * popular picks or a pasted link), a TMDB chart or a TMDB list link. Tabs
 * follow the scope's `import_sources`. A pick fills the draft's details only
 * where they haven't been changed by hand.
 */
function NewListContents({
  scopeKind,
  draft,
  onChange,
  libraries,
  capabilities,
  initialTab,
}: PanelBase & { draft: CollectionDraft & { synced: SyncedDraft }; initialTab?: SyncedTab }) {
  const id = useId();
  const isServer = scopeKind === "server";
  const adminTemplates = useCollectionTemplates(isServer);
  const personalTemplates = useUserCollectionTemplates(!isServer);
  const catalog = (isServer ? adminTemplates : personalTemplates).data;
  const importSources = useMemo(() => capabilities?.import_sources ?? [], [capabilities]);
  const picks = useMemo(
    () => popularPicks(catalog?.categories ?? [], importSources),
    [catalog, importSources],
  );
  const tabs = TAB_ORDER.filter((tab) =>
    importSources.some((source) => TAB_OF_SOURCE[source as ImportSource] === tab),
  );
  const [tab, setTab] = useState<SyncedTab>();
  const opening = initialTab && tabs.includes(initialTab) ? initialTab : tabs[0];
  const activeTab = tab && tabs.includes(tab) ? tab : opening;

  const { synced } = draft;
  const list = synced.list;
  const kinds = eligibleLibraryKinds(list?.mediaKind);
  const offered = libraries.filter((library) => fits(library, kinds));
  const left = libraries.filter((library) => !fits(library, kinds)).map((library) => library.name);
  const only = list?.mediaKind === "movie" || list?.mediaKind === "tv" ? list.mediaKind : null;

  /** Follows `pick`; libraries that can't hold its titles are unticked. */
  function follow(
    pick: SyncedPick,
    links: Partial<Pick<SyncedDraft, "mdblistLink" | "tmdbListLink">>,
  ) {
    const pickKinds = eligibleLibraryKinds(pick.list?.mediaKind);
    onChange((current) => {
      const applied = applyPick(current, pick);
      return {
        ...applied,
        libraryIds: fittingLibraryIds(applied.libraryIds, libraries, pickKinds),
        synced: { ...applied.synced!, ...links },
      };
    });
  }

  function onMDBListLink(raw: string) {
    const url = cleanMDBListLink(raw);
    const next: SyncedList | null = isMDBListLink(url)
      ? { source: "mdblist", url, mediaKind: "mixed" }
      : null;
    follow(linkPick(next), { mdblistLink: raw });
  }

  function onTMDBListLink(raw: string) {
    const next: SyncedList | null = isValidTMDBListURL(raw)
      ? { source: "tmdb_list", url: raw.trim(), mediaKind: "mixed" }
      : null;
    follow(linkPick(next), { tmdbListLink: raw });
  }

  const templates = picks.flatMap((group) => group.templates);
  const updateSynced = (fields: Partial<SyncedDraft>) =>
    onChange((current) => ({ ...current, synced: { ...current.synced!, ...fields } }));

  return (
    <>
      {activeTab ? (
        <Tabs value={activeTab} onValueChange={(next) => setTab(next as SyncedTab)}>
          <TabsList aria-label="Where the list comes from" className="h-11 w-full sm:w-fit">
            {tabs.map((entry) => (
              <TabsTrigger key={entry} value={entry} className="px-3.5">
                {TAB_LABEL[entry]}
              </TabsTrigger>
            ))}
          </TabsList>
          <TabsContent value="mdblist" className="mt-3">
            <MDBListBrowser
              scopeKind={scopeKind}
              picks={picks}
              searchEnabled={capabilities?.mdblist_search ?? false}
              selectedId={list?.pickId}
              onPick={(pick) => follow(pick, { mdblistLink: "", tmdbListLink: "" })}
              link={synced.mdblistLink}
              onLinkChange={onMDBListLink}
            />
          </TabsContent>
          <TabsContent value="tmdb_chart" className="mt-3">
            <ChartTab
              isServer={isServer}
              chart={list?.source === "tmdb_chart" ? list.chart : null}
              onChart={(chart) =>
                follow(chartPick(chart, templates, scopeKind), {
                  mdblistLink: "",
                  tmdbListLink: "",
                })
              }
            />
          </TabsContent>
          <TabsContent value="tmdb_list" className="mt-3">
            <TMDBListURLField
              id={`${id}-tmdb-list`}
              label={PASTE_TMDB_LIST_LINK}
              help={TMDB_LIST_HELP}
              value={synced.tmdbListLink}
              onChange={onTMDBListLink}
            />
          </TabsContent>
        </Tabs>
      ) : capabilities ? (
        <p className="bg-muted/60 flex items-start gap-2.5 rounded-xl px-3 py-2.5 text-[13px]">
          <Info aria-hidden className="text-muted-foreground mt-0.5 size-4 shrink-0" />
          {SYNCED_OFF}
        </p>
      ) : (
        <Skeleton aria-hidden className="h-11 rounded-xl" />
      )}

      <ListSettings
        isServer={isServer}
        draft={draft}
        onChange={onChange}
        libraries={offered}
        librariesNote={only && left.length > 0 ? ineligibleLibrariesLine(only, left) : undefined}
        limit={synced.limit}
        onLimitChange={(limit) => updateSynced({ limit })}
        schedule={synced.schedule}
        onScheduleChange={(schedule) => updateSynced({ schedule })}
        timeZone={capabilities?.schedule_time_zone}
      />
    </>
  );
}

/** Where a list's titles go, how they're ordered, and when it syncs: the end of both modes. */
function ListSettings({
  isServer,
  draft,
  onChange,
  libraries,
  librariesNote,
  librariesLocked,
  eligibleKinds,
  limit,
  limitLocked,
  onLimitChange,
  schedule,
  onScheduleChange,
  scheduleLocked,
  scheduleLockedReason,
  timeZone,
}: {
  isServer: boolean;
  draft: CollectionDraft;
  onChange: (update: (draft: CollectionDraft) => CollectionDraft) => void;
  libraries: readonly LibraryOption[];
  librariesNote?: string;
  librariesLocked?: boolean;
  eligibleKinds?: string[];
  limit: number | undefined;
  limitLocked?: boolean;
  onLimitChange: (limit: number | undefined) => void;
  schedule: string;
  onScheduleChange: (schedule: string) => void;
  scheduleLocked?: boolean;
  /** Why the schedule can't change, once that's known. */
  scheduleLockedReason?: string;
  timeZone?: { utc_offset: string; name?: string };
}) {
  const id = useId();
  return (
    <>
      <div className="border-border/70 border-t pt-4">
        <LibrariesLine
          lead="Match into"
          libraries={[...libraries]}
          value={draft.libraryIds}
          allLabel={isServer ? undefined : ALL_MY_LIBRARIES_LABEL}
          eligibleKinds={eligibleKinds}
          ineligibleReason={eligibleKinds ? ONLY_KIND_REASON[eligibleKinds[0]!] : undefined}
          disabled={librariesLocked}
          onChange={(libraryIds) => onChange((current) => ({ ...current, libraryIds }))}
          warning={
            librariesNote ? (
              <p className="text-muted-foreground text-[13px]">{librariesNote}</p>
            ) : null
          }
        />
      </div>

      <OrderBlock
        mode="synced"
        sortConfig={draft.rawSortConfig}
        limit={limit}
        limitLocked={limitLocked}
        allowPersonalized={!isServer}
        onSortChange={(rawSortConfig) => onChange((current) => ({ ...current, rawSortConfig }))}
        onLimitChange={onLimitChange}
      />

      <section aria-labelledby={`${id}-sync`} className="border-border/70 grid gap-3 border-t pt-4">
        <h3 id={`${id}-sync`} className="text-[14.5px] font-semibold">
          Sync
        </h3>
        {isServer ? (
          <ScheduleField
            scope="server"
            value={schedule}
            timeZone={timeZone}
            disabled={scheduleLocked}
            onChange={onScheduleChange}
          />
        ) : (
          <ScheduleField
            scope="personal"
            value={schedule}
            disabled={scheduleLocked}
            onChange={onScheduleChange}
          />
        )}
        {scheduleLockedReason ? (
          <p className="text-muted-foreground flex items-center gap-1.5 text-[13px]">
            <Lock aria-hidden className="size-3.5 shrink-0" />
            {scheduleLockedReason}
          </p>
        ) : null}
      </section>
    </>
  );
}

/** Why a library of the other kind can't be picked for a list of one kind. */
const ONLY_KIND_REASON: Record<string, string> = {
  movies: "This list only has movies.",
  series: "This list only has TV shows.",
};

/** "mdblist.com/lists/u/top": a list link as the source card shows it. */
function shownLink(link: string) {
  return link
    .trim()
    .replace(/^https?:\/\//, "")
    .replace(/\/json$/, "");
}

/** What a saved list follows, as one line, with the button that changes it. */
function SourceCard({
  title,
  detail,
  action,
  onChange,
}: {
  title: string;
  detail: string;
  action: string;
  onChange: () => void;
}) {
  return (
    <div className="bg-muted/40 flex flex-wrap items-center gap-3 rounded-xl px-4 py-3">
      <div className="grid min-w-0 flex-1 gap-0.5">
        <span className="text-[14px] font-semibold">{title}</span>
        <span className="text-muted-foreground truncate text-[13px]">{detail}</span>
      </div>
      <Button type="button" size="sm" variant="outline" onClick={onChange}>
        {action}
      </Button>
    </div>
  );
}

/** A list followed by its link: the link, and Change link to paste another. */
function LinkSource({
  source,
  link,
  onChange,
}: {
  source: "mdblist" | "tmdb_list";
  link: string;
  onChange: (link: string) => void;
}) {
  const id = useId();
  const [changing, setChanging] = useState(false);
  const fieldId = `${id}-${source === "tmdb_list" ? "tmdb-list" : "mdblist"}`;
  // Change link moves focus into the field it opens.
  useEffect(() => {
    if (changing) document.getElementById(fieldId)?.focus();
  }, [changing, fieldId]);
  if (!changing) {
    return (
      <SourceCard
        title={SYNCED_SOURCE_LABEL[source]}
        detail={shownLink(link)}
        action={CHANGE_LINK}
        onChange={() => setChanging(true)}
      />
    );
  }
  if (source === "tmdb_list") {
    return (
      <TMDBListURLField
        id={fieldId}
        label={TMDB_LIST_LINK}
        help={LINK_CHANGES_AT_NEXT_SYNC}
        value={link}
        onChange={onChange}
      />
    );
  }
  const invalid = link.trim() !== "" && !isMDBListLink(cleanMDBListLink(link));
  return (
    <div className="grid gap-2">
      <Label htmlFor={fieldId}>{MDBLIST_LINK}</Label>
      <Input
        id={fieldId}
        value={link}
        placeholder="https://mdblist.com/lists/…"
        aria-invalid={invalid || undefined}
        aria-describedby={`${id}-mdblist-help`}
        onChange={(event) => onChange(event.target.value)}
      />
      <p
        id={`${id}-mdblist-help`}
        className={invalid ? "text-destructive text-xs" : "text-muted-foreground text-xs"}
      >
        {invalid ? MDBLIST_LINK_INVALID : LINK_CHANGES_AT_NEXT_SYNC}
      </p>
    </div>
  );
}

/**
 * A server TMDB list or chart: what it follows, and Change to pick another
 * chart or paste a list link.
 */
function TMDBSource({
  list,
  onChange,
}: {
  list: ListDraft;
  onChange: (fields: Partial<ListDraft>) => void;
}) {
  const id = useId();
  const [changing, setChanging] = useState(false);
  const isList = list.source === "tmdb_list";
  // Change moves focus to the chosen tab of what it opens.
  useEffect(() => {
    if (!changing) return;
    document
      .getElementById(`${id}-tabs`)
      ?.querySelector<HTMLElement>('[aria-selected="true"]')
      ?.focus();
  }, [changing, id]);
  if (!changing) {
    let detail = shownLink(list.link);
    if (!isList) detail = list.chart ? chartSummary(list.chart) : "";
    return (
      <SourceCard
        title={SYNCED_SOURCE_LABEL[list.source]}
        detail={detail}
        action={isList ? CHANGE_LINK : "Change chart"}
        onChange={() => setChanging(true)}
      />
    );
  }
  return (
    <Tabs
      value={isList ? "tmdb_list" : "tmdb_chart"}
      onValueChange={(next) => onChange({ source: next as "tmdb_chart" | "tmdb_list" })}
    >
      <TabsList
        id={`${id}-tabs`}
        aria-label="Where the list comes from"
        className="h-11 w-full sm:w-fit"
      >
        <TabsTrigger value="tmdb_chart" className="px-3.5">
          {TAB_LABEL.tmdb_chart}
        </TabsTrigger>
        <TabsTrigger value="tmdb_list" className="px-3.5">
          {TAB_LABEL.tmdb_list}
        </TabsTrigger>
      </TabsList>
      <TabsContent value="tmdb_chart" className="mt-3">
        <ChartTab isServer chart={list.chart ?? null} onChart={(chart) => onChange({ chart })} />
      </TabsContent>
      <TabsContent value="tmdb_list" className="mt-3">
        <TMDBListURLField
          id={`${id}-tmdb-list`}
          label={TMDB_LIST_LINK}
          help={LINK_CHANGES_AT_NEXT_SYNC}
          value={list.link}
          onChange={(link) => onChange({ link })}
        />
      </TabsContent>
    </Tabs>
  );
}

/** A source a profile can't change, as text: "TMDB chart: Trending movies, this week". */
function FixedSource({ list }: { list: ListDraft }) {
  const what =
    list.source === "tmdb_chart" && list.chart
      ? `${SYNCED_SOURCE_LABEL.tmdb_chart}: ${chartSummary(list.chart)}`
      : SYNCED_SOURCE_LABEL[list.source];
  return (
    <div className="flex flex-wrap items-baseline gap-x-6 gap-y-1 text-[13.5px]">
      <span className="text-muted-foreground">Follows</span>
      <span className="font-medium">{what}</span>
      <span className="text-muted-foreground flex items-center gap-1 text-[12.5px]">
        <Lock aria-hidden className="size-3.5" />
        {CHART_SET_WHEN_MADE}
      </span>
    </div>
  );
}

/** What a saved list's PATCH can change about the list it follows, per source. */
function SavedSource({
  isServer,
  list,
  name,
  sourceUrl,
  onChange,
}: {
  isServer: boolean;
  list: ListDraft;
  name: string;
  sourceUrl: string;
  onChange: (fields: Partial<ListDraft>) => void;
}) {
  switch (list.source) {
    case "mdblist":
      return (
        <LinkSource source="mdblist" link={list.link} onChange={(link) => onChange({ link })} />
      );
    case "tmdb_list":
    case "tmdb_chart":
      if (isServer) return <TMDBSource list={list} onChange={onChange} />;
      return list.source === "tmdb_list" ? (
        <LinkSource source="tmdb_list" link={list.link} onChange={(link) => onChange({ link })} />
      ) : (
        <FixedSource list={list} />
      );
    case "tmdb_franchise":
      return isServer ? (
        <FranchiseIdField
          value={list.franchiseId}
          name={name}
          onChange={(franchiseId) => onChange({ franchiseId })}
        />
      ) : (
        <FixedSource list={list} />
      );
    case "tmdb_discover":
    case "trakt":
      return (
        <LockedSourceSummary source={list.source} config={list.stored} sourceUrl={sourceUrl} />
      );
  }
}

/**
 * What the list controls and what the editor controls, side by side. Discover
 * and legacy Trakt lists say this in their locked summary instead.
 */
function WhoDecides({ scheduleEditable }: { scheduleEditable: boolean }) {
  const id = useId();
  const groups = [
    { key: "list", icon: Lock, title: LIST_DECIDES, items: LIST_DECIDES_ITEMS },
    { key: "you", icon: Pencil, title: YOU_DECIDE, items: youDecideItems(scheduleEditable) },
  ];
  return (
    <div className="grid gap-2.5 sm:grid-cols-2">
      {groups.map(({ key, icon: Icon, title, items }) => (
        <div key={key} className="bg-accent/55 rounded-xl px-3.5 py-3">
          <p
            id={`${id}-${key}`}
            className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold"
          >
            <Icon aria-hidden className="text-muted-foreground size-3.5" />
            {title}
          </p>
          <ul
            aria-labelledby={`${id}-${key}`}
            className="text-muted-foreground grid gap-0.5 text-[12.5px]"
          >
            {items.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}

/** A saved list's sync state and Sync now, as the editor tracks them. */
export interface SavedListSync {
  view: CollectionView;
  syncing: boolean;
  /** Titles the last sync run here skipped. */
  skipped?: number;
  /** Unsaved changes to what the sync reads: Sync now waits for Save. */
  saveFirst?: boolean;
  onSyncNow?: () => void;
  /** Counts Discards; each puts the source card back. */
  discards?: number;
}

/**
 * A saved Synced list: a failed sync at the top, the sync status strip, the
 * list it follows (a link, a chart, a TMDB collection ID, or a locked summary
 * for Discover and legacy Trakt lists), then its libraries, order and schedule.
 */
function SavedListContents({
  scopeKind,
  draft,
  onChange,
  libraries,
  capabilities,
  saved,
}: PanelBase & { draft: CollectionDraft & { list: ListDraft }; saved: SavedListSync }) {
  const isServer = scopeKind === "server";
  const { list } = draft;
  const { view, syncing } = saved;
  const sync = view.sync ?? { status: "", message: "", schedule: "" };
  const mediaKind = savedListMediaKind(list);
  const kinds = eligibleLibraryKinds(mediaKind);
  const only = mediaKind === "movie" || mediaKind === "tv" ? mediaKind : null;
  const left = libraries.filter((library) => !fits(library, kinds)).map((library) => library.name);
  const trakt = list.source === "trakt";
  const chosen = libraries.filter((library) => draft.libraryIds.includes(library.id));
  // The server won't restart a stopped Trakt schedule, and a profile's
  // schedule stays put until the server says it may change.
  let scheduleLockedReason: string | undefined;
  if (trakt && !sync.schedule.trim()) scheduleLockedReason = TRAKT_SCHEDULE_STOPPED;
  if (!isServer && capabilities?.sync_schedule_editable === false) {
    scheduleLockedReason = PERSONAL_SCHEDULE_LOCKED;
  }
  const scheduleLocked =
    Boolean(scheduleLockedReason) || (!isServer && !capabilities?.sync_schedule_editable);
  // A new source unticks libraries that can't hold its titles, as on create.
  const updateList = (fields: Partial<ListDraft>) =>
    onChange((current) => {
      const next = { ...current.list!, ...fields };
      if (!("chart" in fields || "source" in fields)) return { ...current, list: next };
      const nextKinds = eligibleLibraryKinds(savedListMediaKind(next));
      return {
        ...current,
        list: next,
        libraryIds: fittingLibraryIds(current.libraryIds, libraries, nextKinds),
      };
    });
  return (
    <>
      {sync.status === "failed" && !syncing ? (
        <SyncFailedCallout
          sync={sync}
          itemCount={view.itemCount}
          syncing={syncing}
          saveFirst={saved.saveFirst}
          onSyncNow={saved.onSyncNow}
        />
      ) : null}
      <SyncStatusBlock
        sync={sync}
        itemCount={view.itemCount}
        syncing={syncing}
        skipped={saved.skipped}
        libraryNames={chosen.map((library) => library.name)}
        canSync={Boolean(saved.onSyncNow)}
      />
      <SavedSource
        key={saved.discards}
        isServer={isServer}
        list={list}
        name={view.name}
        sourceUrl={view.raw.source_url ?? ""}
        onChange={updateList}
      />
      {list.source === "tmdb_discover" || trakt ? null : (
        <WhoDecides scheduleEditable={!scheduleLocked} />
      )}
      <ListSettings
        // Discard puts back the settings' own state too, such as an open custom schedule.
        key={`settings-${saved.discards ?? 0}`}
        isServer={isServer}
        draft={draft}
        onChange={onChange}
        libraries={libraries}
        librariesNote={only && left.length > 0 ? ineligibleLibrariesLine(only, left) : undefined}
        librariesLocked={trakt}
        eligibleKinds={kinds}
        limit={list.limit}
        limitLocked={trakt}
        onLimitChange={(limit) => updateList({ limit })}
        schedule={list.schedule}
        onScheduleChange={(schedule) => updateList({ schedule })}
        scheduleLocked={scheduleLocked}
        scheduleLockedReason={scheduleLockedReason}
        timeZone={capabilities?.schedule_time_zone}
      />
    </>
  );
}

interface PanelBase {
  scopeKind: ScopeKind;
  onChange: (update: (draft: CollectionDraft) => CollectionDraft) => void;
  libraries: readonly LibraryOption[];
  capabilities?: {
    import_sources: readonly string[];
    mdblist_search: boolean;
    schedule_time_zone?: { utc_offset: string; name?: string };
    sync_schedule_editable?: boolean;
  };
}

export type SyncedListPanelProps = PanelBase &
  (
    | { draft: CollectionDraft & { synced: SyncedDraft }; initialTab?: SyncedTab; saved?: never }
    | { draft: CollectionDraft & { list: ListDraft }; saved: SavedListSync }
  );

/**
 * A Synced list's Contents, "The list it follows". A new list picks its
 * source here; a saved one shows its sync status and what it follows, as far
 * as its source can change. Both end with the libraries it matches into, its
 * order and its sync schedule.
 */
export function SyncedListPanel(props: SyncedListPanelProps) {
  const id = useId();
  return (
    <section
      aria-labelledby={`${id}-heading`}
      className="surface-panel grid content-start gap-5 rounded-[22px] p-5 sm:p-6"
    >
      <div>
        <h2 id={`${id}-heading`} className="text-[17px] font-semibold">
          {SYNCED_HEADING}
        </h2>
        <p className="text-muted-foreground mt-1 text-[13.5px]">{SYNCED_CAPTION}</p>
      </div>
      {props.saved ? <SavedListContents {...props} /> : <NewListContents {...props} />}
    </section>
  );
}
