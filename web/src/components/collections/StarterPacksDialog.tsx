import {
  Fragment,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type Ref,
} from "react";
import { Link } from "react-router";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ChevronDown,
  ChevronRight,
  House,
  Layers3,
  LibraryBig,
  Loader2,
  Pin,
  Plus,
  RefreshCw,
} from "lucide-react";

import type { Library } from "@/api/types";
import { templateResultFromV2 } from "@/api/adminCollections";
import { StepDialog } from "@/components/calm/StepDialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  collectionJobQuery,
  starterPackDryRunQuery,
  useQueueCollectionTemplateBundleApply,
} from "@/hooks/queries/admin/collections";
import { adminSectionsQuery } from "@/hooks/queries/sections";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import {
  useCollectionTemplateBundles,
  type ApplyCollectionTemplateBundleResponse,
} from "@/lib/collectionTemplates";
import {
  KEEP_CURRENT,
  SYNC_LOAD_THRESHOLD,
  appliedRows,
  currentHero,
  defaultPackLibraryIds,
  effectiveHeroes,
  failureReason,
  featuredRequest,
  heroTemplates,
  packAdded,
  packLibraries,
  packPlan,
  packResultHeading,
  plural,
  starterPacksOf,
  type HeroChoices,
  type PackLibraryRow,
  type PackListEntry,
  type StarterPack,
} from "@/lib/collections/starterPacks";
import { cn } from "@/lib/utils";

/** What the admin changed on the open pack; a fresh pack starts from the defaults. */
interface PackDraft {
  packId: string;
  libraryIds: number[] | null;
  heroesOn: boolean;
  heroes: Partial<HeroChoices>;
}

function freshDraft(packId: string): PackDraft {
  return { packId, libraryIds: null, heroesOn: false, heroes: {} };
}

function libraryPageLabel(libraryName: string) {
  return `the ${libraryName} page`;
}

/**
 * Starter packs: the server's template bundles, one pack applied at a time.
 * Each pack shows a dry run per library before anything changes; hero banners
 * are opt-in, so a pack never replaces a page's hero unless asked. After Add
 * the dialog stays open on the pack list, tags the pack Added and reports the
 * job's result.
 */
export function StarterPacksDialog({
  libraries,
  initialLibraryId,
  onClose,
}: {
  libraries: Library[];
  /** The library selected on the page, chosen first when the pack fits it. */
  initialLibraryId: number | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const narrow = useMediaQuery("(max-width: 1023px)");
  const titleRef = useRef<HTMLHeadingElement>(null);
  const railId = useId();
  const panelId = `${railId}-panel`;
  const tabId = (packId: string) => `${railId}-tab-${packId}`;
  const resultRef = useRef<HTMLElement>(null);
  const bundles = useCollectionTemplateBundles();
  const packs = useMemo(() => starterPacksOf(bundles.data?.bundles ?? []), [bundles.data]);
  const [draft, setDraft] = useState<PackDraft | null>(null);
  const pack = packs.find((entry) => entry.id === draft?.packId) ?? packs[0];
  const current = pack && draft?.packId === pack.id ? draft : freshDraft(pack?.id ?? "");
  const update = (patch: Partial<PackDraft>) => setDraft({ ...current, ...patch });

  const fitting = useMemo(() => (pack ? packLibraries(pack, libraries) : []), [pack, libraries]);
  const libraryIds = useMemo(
    () =>
      current.libraryIds?.filter((id) => fitting.some((library) => library.id === id)) ??
      (pack ? defaultPackLibraryIds(pack, libraries, initialLibraryId) : []),
    [current.libraryIds, fitting, pack, libraries, initialLibraryId],
  );
  const chosen = useMemo(
    () => fitting.filter((library) => libraryIds.includes(library.id)),
    [fitting, libraryIds],
  );
  const heroes = useMemo(
    () => (pack ? effectiveHeroes(pack, chosen, current.heroes) : null),
    [pack, chosen, current.heroes],
  );
  const featured = current.heroesOn && heroes ? featuredRequest(heroes) : undefined;
  const dryRunQuery = starterPackDryRunQuery(pack?.id ?? "", {
    library_ids: libraryIds,
    featured,
  });
  const dryRun = useQuery({ ...dryRunQuery, enabled: !!pack && libraryIds.length > 0 });
  const plan = useMemo(
    () => (pack && dryRun.data ? packPlan(dryRun.data, pack, libraryIds) : null),
    [pack, dryRun.data, libraryIds],
  );
  const newCount = plan?.reduce((sum, row) => sum + row.added.length, 0) ?? 0;

  // One apply at a time: the pack it was for, and the job running it.
  const [run, setRun] = useState<{ pack: StarterPack; jobId: string } | null>(null);
  const [checking, setChecking] = useState(false);
  const [added, setAdded] = useState<ReadonlySet<string>>(new Set());
  const queue = useQueueCollectionTemplateBundleApply();
  const job = useQuery(collectionJobQuery(run?.jobId ?? null));
  const finished = job.data?.terminal ? job.data : null;
  const finishedResult = finished?.template_result
    ? templateResultFromV2(finished.template_result)
    : undefined;
  const finishedAdded =
    finished?.state === "succeeded" &&
    (!finishedResult || !run || packAdded(finishedResult, run.pack));
  const running = checking || queue.isPending || (run !== null && !finished);

  const handledJob = useRef<string | null>(null);
  useEffect(() => {
    if (!finished || !run || handledJob.current === finished.id) return;
    handledJob.current = finished.id;
    const succeeded = finished.state === "succeeded";
    if (succeeded && finishedAdded) setAdded((prev) => new Set(prev).add(run.pack.id));
    // The heroes are set now; leaving the switch on would offer to set them again.
    // Either way the pack is checked again, so the table shows what is there now:
    // turning the switch off changes the check's key, which runs it by itself.
    // The page refreshes the collections and Home rows the job changed.
    const heroesOff = succeeded && draft?.packId === run.pack.id && draft.heroesOn;
    if (heroesOff) setDraft({ ...draft, heroesOn: false });
    else void queryClient.invalidateQueries({ queryKey: dryRunQuery.queryKey, exact: true });
    resultRef.current?.focus();
  }, [finished, finishedAdded, run, draft, dryRunQuery.queryKey, queryClient]);

  async function add() {
    if (!pack) return;
    setChecking(true);
    try {
      // Check again right before adding, so the table matches what happens.
      const fresh = await queryClient.fetchQuery({ ...dryRunQuery, staleTime: 0 });
      const freshNew = packPlan(fresh, pack, libraryIds).some((row) => row.added.length > 0);
      if (!freshNew && !featured) return;
      const accepted = await queue.mutateAsync({
        bundleId: pack.id,
        body: { library_ids: libraryIds, delete_existing: false, featured },
      });
      setRun({ pack, jobId: accepted.id });
    } catch {
      // The table shows a failed check; the queue hook toasts its own errors.
    } finally {
      setChecking(false);
    }
  }

  const canAdd =
    !running && dryRun.isSuccess && !dryRun.isFetching && (newCount > 0 || featured !== undefined);
  let addLabel = "Add";
  if (running) addLabel = "Adding…";
  else if (newCount > 0) addLabel = `Add ${plural(newCount, "collection")}`;
  else if (featured) addLabel = "Set hero banners";
  else if (dryRun.isSuccess) addLabel = "Nothing new to add";

  const progress = job.data?.progress;
  const footerStart = running ? (
    <span
      role="status"
      className="text-muted-foreground inline-flex items-center gap-2 text-[13px]"
    >
      <Loader2 aria-hidden className="size-4 animate-spin" />
      Adding {run?.pack.title ?? pack?.title}…
      {progress && progress.total > 0 ? ` ${progress.current} of ${progress.total}` : null}
    </span>
  ) : (
    <span className="text-muted-foreground inline-flex min-w-0 items-center gap-2 text-[13px]">
      <RefreshCw aria-hidden className="size-4 shrink-0 max-sm:hidden" />
      {/* Phones only fit the buttons; screen readers still hear the note. */}
      <span className="truncate max-sm:sr-only">
        They sync for the first time right after. Then you can add another pack.
      </span>
    </span>
  );

  const selectPack = (id: string) => setDraft(freshDraft(id));

  return (
    <StepDialog
      size="picker"
      onClose={onClose}
      onOpenFocus={() => titleRef.current?.focus()}
      title="Starter packs"
      titleRef={titleRef}
      description="Add a ready-made set of synced lists to your libraries. Nothing changes until you press Add."
      footerStart={footerStart}
      actions={
        <Button type="button" disabled={!canAdd} onClick={() => void add()}>
          {newCount > 0 && !running ? <Plus aria-hidden className="size-4" /> : null}
          {addLabel}
        </Button>
      }
    >
      <div className="border-border grid min-h-0 flex-1 border-t lg:grid-cols-[236px_minmax(0,1fr)]">
        {!narrow ? (
          <div className="border-border overflow-y-auto border-r p-3">
            {packs.length > 0 && pack ? (
              <PackRail
                packs={packs}
                active={pack.id}
                added={added}
                locked={running}
                tabId={tabId}
                panelId={panelId}
                onSelect={selectPack}
              />
            ) : null}
          </div>
        ) : null}
        <div
          className="grid min-h-0 content-start gap-5 overflow-y-auto px-5 py-5 sm:px-6"
          {...(!narrow && pack
            ? { role: "tabpanel", id: panelId, "aria-labelledby": tabId(pack.id) }
            : {})}
        >
          {bundles.isError ? (
            <p role="alert" className="text-sm">
              The starter packs didn't load. Close this and try again.
            </p>
          ) : !pack ? (
            <p role="status" className="text-muted-foreground text-sm">
              {bundles.isPending ? "Loading starter packs…" : "This server has no starter packs."}
            </p>
          ) : (
            <>
              {narrow ? (
                <PackSelect
                  packs={packs}
                  active={pack.id}
                  added={added}
                  locked={running}
                  onSelect={selectPack}
                />
              ) : null}
              <div>
                <h3 className="text-lg font-semibold tracking-[-0.02em]">{pack.title}</h3>
                <p className="text-muted-foreground mt-1 text-[13.5px]">{pack.description}</p>
              </div>

              {run && finished && run.pack.id === pack.id ? (
                <ApplyResult
                  ref={resultRef}
                  pack={run.pack}
                  succeeded={finished.state === "succeeded"}
                  added={finishedAdded}
                  failure={finished.failure?.detail}
                  result={finishedResult}
                />
              ) : null}

              {fitting.length === 0 ? (
                <p className="text-sm">
                  None of your libraries can take this pack.{" "}
                  <span className="text-muted-foreground">
                    Its lists are for movie and TV libraries. Add one in{" "}
                    <Link to="/admin/libraries" className="text-foreground underline">
                      Libraries
                    </Link>
                    .
                  </span>
                </p>
              ) : (
                <>
                  <LibraryChips
                    libraries={fitting}
                    selectedIds={libraryIds}
                    locked={running}
                    onChange={(ids) => update({ libraryIds: ids })}
                  />
                  <WhatWillHappen
                    plan={plan}
                    result={dryRun.data}
                    noLibraries={libraryIds.length === 0}
                    loading={dryRun.isPending || (dryRun.isFetching && !dryRun.data)}
                    failed={dryRun.isError}
                    onRetry={() => void dryRun.refetch()}
                    heroesOn={current.heroesOn}
                    newCount={newCount}
                  />
                  {heroes ? (
                    <HeroBanners
                      pack={pack}
                      libraries={chosen}
                      heroes={heroes}
                      on={current.heroesOn}
                      locked={running}
                      onToggle={(on) => update({ heroesOn: on })}
                      onChange={(next) => update({ heroes: next })}
                    />
                  ) : null}
                </>
              )}
            </>
          )}
        </div>
      </div>
    </StepDialog>
  );
}

function AddedTag() {
  return (
    <span className="rounded-full bg-emerald-500/15 px-2 py-0.5 text-[11px] font-medium text-emerald-600 dark:text-emerald-400">
      Added
    </span>
  );
}

/** The packs as vertical tabs: name and count, Everything last after a line. */
function PackRail({
  packs,
  active,
  added,
  locked,
  tabId,
  panelId,
  onSelect,
}: {
  packs: StarterPack[];
  active: string;
  added: ReadonlySet<string>;
  /** While a pack is being added, the open pack stays put. */
  locked: boolean;
  tabId: (packId: string) => string;
  panelId: string;
  onSelect: (id: string) => void;
}) {
  const activeIndex = Math.max(
    0,
    packs.findIndex((pack) => pack.id === active),
  );
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (locked) return;
    let next: number | null = null;
    if (event.key === "ArrowUp") next = (activeIndex - 1 + packs.length) % packs.length;
    else if (event.key === "ArrowDown") next = (activeIndex + 1) % packs.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = packs.length - 1;
    if (next === null) return;
    event.preventDefault();
    onSelect(packs[next]!.id);
    tabs.current[next]?.focus();
  }

  return (
    <div
      role="tablist"
      aria-label="Packs"
      aria-orientation="vertical"
      onKeyDown={handleKeyDown}
      className="grid content-start gap-0.5"
    >
      {packs.map((pack, index) => (
        <Fragment key={pack.id}>
          {pack.everything && index > 0 ? (
            <div role="none" aria-hidden className="bg-border mx-1 my-2 h-px" />
          ) : null}
          <button
            ref={(element) => {
              tabs.current[index] = element;
            }}
            type="button"
            role="tab"
            id={tabId(pack.id)}
            aria-controls={panelId}
            aria-selected={index === activeIndex}
            tabIndex={index === activeIndex ? 0 : -1}
            disabled={locked && index !== activeIndex}
            // The active tab stays enabled while locked, so guard its click too.
            onClick={() => {
              if (!locked) onSelect(pack.id);
            }}
            className="text-muted-foreground hover:text-foreground aria-selected:bg-accent aria-selected:text-foreground aria-selected:ring-border focus-visible:ring-ring/50 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-2 gap-y-1 rounded-[10px] px-3 py-[9px] text-left text-sm outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-60 aria-selected:ring-1 aria-selected:ring-inset"
          >
            <span className="truncate">{pack.title}</span>
            <span className="text-xs tabular-nums opacity-80">
              {pack.templates.length}
              <span className="sr-only"> {pack.templates.length === 1 ? "list" : "lists"}</span>
            </span>
            {added.has(pack.id) ? (
              <span className="col-span-2">
                <AddedTag />
              </span>
            ) : null}
          </button>
        </Fragment>
      ))}
    </div>
  );
}

/** Phones pick the pack from a select at the top instead of the rail. */
function PackSelect({
  packs,
  active,
  added,
  locked,
  onSelect,
}: {
  packs: StarterPack[];
  active: string;
  added: ReadonlySet<string>;
  locked: boolean;
  onSelect: (id: string) => void;
}) {
  return (
    <Select value={active} onValueChange={onSelect} disabled={locked}>
      <SelectTrigger aria-label="Pack" className="h-11 w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {packs.map((pack) => (
          <SelectItem key={pack.id} value={pack.id}>
            {pack.title} · {plural(pack.templates.length, "list")}
            {added.has(pack.id) ? " · Added" : ""}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function LibraryChips({
  libraries,
  selectedIds,
  locked,
  onChange,
}: {
  libraries: Library[];
  selectedIds: number[];
  locked: boolean;
  onChange: (ids: number[]) => void;
}) {
  const id = useId();
  return (
    <div role="group" aria-labelledby={`${id}-label`}>
      <span id={`${id}-label`} className="mb-2 block text-sm leading-none font-medium">
        Add to these libraries
      </span>
      <div className="flex flex-wrap gap-2">
        {libraries.map((library) => {
          const checked = selectedIds.includes(library.id);
          return (
            <label
              key={library.id}
              className={cn(
                "border-border has-focus-visible:ring-ring/50 hover:bg-accent/60 inline-flex h-10 cursor-pointer items-center gap-2.5 rounded-[12px] border pr-3.5 pl-3 text-sm font-medium transition-colors has-focus-visible:ring-[3px] has-disabled:cursor-not-allowed has-disabled:opacity-60",
                checked && "bg-accent border-foreground/55",
              )}
            >
              <Checkbox
                checked={checked}
                disabled={locked}
                className="size-[18px] rounded-[5px] focus-visible:ring-0"
                onCheckedChange={(next) =>
                  onChange(
                    next === true
                      ? [...selectedIds, library.id]
                      : selectedIds.filter((selected) => selected !== library.id),
                  )
                }
              />
              {library.name}
            </label>
          );
        })}
      </div>
    </div>
  );
}

function Tag({ children, pin = false }: { children: string; pin?: boolean }) {
  return (
    <span className="border-border text-muted-foreground inline-flex h-5 shrink-0 items-center gap-1 rounded-full border px-2 text-[11px] font-medium">
      {pin ? <Pin aria-hidden className="size-3" /> : null}
      {children}
    </span>
  );
}

function heroEntryLabel(entry: ApplyCollectionTemplateBundleResponse["featured"][number]) {
  const page = entry.surface === "home" ? "Home" : libraryPageLabel(entry.library_name ?? "");
  return `Hero banner on ${page}`;
}

/** The dry run as a table: one row per library, expandable to every list. */
function WhatWillHappen({
  plan,
  result,
  noLibraries,
  loading,
  failed,
  onRetry,
  heroesOn,
  newCount,
}: {
  plan: PackLibraryRow[] | null;
  result: ApplyCollectionTemplateBundleResponse | undefined;
  noLibraries: boolean;
  loading: boolean;
  failed: boolean;
  onRetry: () => void;
  heroesOn: boolean;
  newCount: number;
}) {
  const id = useId();
  // Libraries the admin opened or closed. The first library starts open, the others closed.
  const [toggled, setToggled] = useState<ReadonlySet<number>>(new Set());
  const isOpen = (libraryId: number, index: number) => (index === 0) !== toggled.has(libraryId);
  const toggle = (libraryId: number) =>
    setToggled((prev) => {
      const next = new Set(prev);
      if (!next.delete(libraryId)) next.add(libraryId);
      return next;
    });

  let body;
  if (noLibraries) {
    body = <p className="text-muted-foreground text-sm">Pick at least one library.</p>;
  } else if (failed) {
    body = (
      <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
        <span>Couldn't check this pack</span>
        <Button type="button" size="sm" variant="outline" onClick={onRetry}>
          Retry
        </Button>
      </div>
    );
  } else if (loading || !plan || !result) {
    body = (
      <div role="status" className="grid gap-2">
        <span className="text-muted-foreground text-[13px]">Checking…</span>
        <Skeleton className="h-9 w-full rounded-lg" />
        <Skeleton className="h-9 w-full rounded-lg" />
      </div>
    );
  } else {
    const heroRows = heroesOn
      ? [
          ...result.featured.map((entry) => ({ entry, failed: false })),
          ...result.featured_failed.map((entry) => ({ entry, failed: true })),
        ]
      : [];
    body = (
      <>
        <div className="border-border overflow-x-auto rounded-xl border">
          <table aria-labelledby={`${id}-label`} className="w-full text-left text-sm">
            <thead className="text-muted-foreground text-[12.5px]">
              <tr>
                <th scope="col" className="px-3.5 py-2.5 font-medium">
                  Library
                </th>
                <th scope="col" className="px-3.5 py-2.5 font-medium">
                  New
                </th>
                <th scope="col" className="px-3.5 py-2.5 font-medium">
                  Already there
                </th>
                <th scope="col" className="px-3.5 py-2.5 font-medium">
                  Not for this library
                </th>
              </tr>
            </thead>
            <tbody>
              {plan.flatMap((row, index) => {
                const open = isOpen(row.libraryId, index);
                const pinned = row.added.filter((entry) => entry.pinned).length;
                const listId = `${id}-lists-${row.libraryId}`;
                const rows = [
                  <tr key={row.libraryId} className="border-border border-t">
                    <th scope="row" className="px-3.5 py-2.5 font-medium">
                      <button
                        type="button"
                        aria-expanded={open}
                        aria-controls={listId}
                        onClick={() => toggle(row.libraryId)}
                        className="focus-visible:ring-ring/50 inline-flex items-center gap-1.5 rounded font-medium outline-none focus-visible:ring-[3px]"
                      >
                        {open ? (
                          <ChevronDown aria-hidden className="size-4" />
                        ) : (
                          <ChevronRight aria-hidden className="size-4" />
                        )}
                        {row.libraryName}
                      </button>
                    </th>
                    <td className="px-3.5 py-2.5">
                      <span className="inline-flex flex-wrap items-center gap-1.5">
                        <span className="whitespace-nowrap">
                          <b className="font-semibold">{row.added.length}</b> new
                        </span>
                        {pinned > 0 ? <Tag pin>{`${pinned} pinned first`}</Tag> : null}
                      </span>
                    </td>
                    <td className="px-3.5 py-2.5 tabular-nums">{row.existing.length}</td>
                    <td className="text-muted-foreground px-3.5 py-2.5">
                      {row.notForLibrary || "None"}
                    </td>
                  </tr>,
                ];
                if (open) {
                  rows.push(
                    <tr key={`${row.libraryId}-lists`} id={listId}>
                      <td colSpan={4} className="px-3.5 pb-3">
                        <PackLists row={row} />
                      </td>
                    </tr>,
                  );
                }
                return rows;
              })}
              {heroRows.map(({ entry, failed: heroFailed }) => (
                <tr
                  key={`${entry.surface}-${entry.library_id ?? "home"}`}
                  className="border-border border-t"
                >
                  <th scope="row" colSpan={2} className="px-3.5 py-2.5 font-medium">
                    {heroEntryLabel(entry)}
                  </th>
                  <td colSpan={2} className="px-3.5 py-2.5">
                    {entry.template_title}
                    {heroFailed ? ` · Can't set it: ${failureReason(entry.reason)}` : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {newCount > SYNC_LOAD_THRESHOLD ? (
          <p className="text-muted-foreground text-[13px]">
            That's {newCount} new lists. Their first syncs run in the background and can take a
            while to finish.
          </p>
        ) : null}
      </>
    );
  }

  return (
    <div className="grid gap-2.5">
      <div className="flex items-baseline justify-between gap-3">
        <span id={`${id}-label`} className="text-sm font-medium">
          What will happen
        </span>
        {result && !loading && !failed ? (
          <span className="text-muted-foreground text-xs">Checked just now</span>
        ) : null}
      </div>
      {body}
      <p className="text-muted-foreground flex items-start gap-2 text-[13px] leading-normal">
        <Layers3 aria-hidden className="mt-[3px] size-3.5 shrink-0" />
        <span>
          New lists land under <b className="text-foreground/85 font-medium">No heading</b> on each
          library's Collections tab, ready to move into a shelf in Arrange. “Already there” lists
          aren't touched.{" "}
          {heroesOn
            ? "Hero banners are added to the pages you picked below."
            : "No sections are added unless you turn on hero banners."}
        </span>
      </p>
    </div>
  );
}

function PackLists({ row }: { row: PackLibraryRow }) {
  const item = (entry: PackListEntry, tag: string | null, pin = false) => (
    <li
      key={`${entry.templateId}-${tag ?? "new"}`}
      className={cn(
        "border-border flex items-center gap-2 border-b py-1.5 text-[13px]",
        tag && !pin && "text-muted-foreground",
      )}
    >
      <span className="min-w-0 truncate">{entry.title}</span>
      {tag ? (
        <span className="ml-auto">
          <Tag pin={pin}>{tag}</Tag>
        </span>
      ) : null}
    </li>
  );
  return (
    <ul aria-label={`Lists for ${row.libraryName}`} className="grid gap-x-6 pl-6 sm:grid-cols-2">
      {row.added.map((entry) => item(entry, entry.pinned ? "Pinned first" : null, true))}
      {row.existing.map((entry) => item(entry, "Already there"))}
      {row.failed.map((entry) => item(entry, "Can't add"))}
    </ul>
  );
}

/**
 * "Also use the pack's hero banners": off by default, so the apply sends no
 * `featured` and existing heroes stay. Each line picks a list for that page
 * or keeps the current hero, and names the hero it replaces.
 */
function HeroBanners({
  pack,
  libraries,
  heroes,
  on,
  locked,
  onToggle,
  onChange,
}: {
  pack: StarterPack;
  libraries: Library[];
  heroes: HeroChoices;
  on: boolean;
  locked: boolean;
  onToggle: (on: boolean) => void;
  onChange: (heroes: HeroChoices) => void;
}) {
  const pages = useQueries({
    queries: [
      adminSectionsQuery("home"),
      ...libraries.map((library) => adminSectionsQuery("library", library.id)),
    ],
  });
  const heroOf = (index: number) => {
    const sections = pages[index]?.data?.sections;
    return sections ? { title: currentHero(sections) } : null;
  };
  const homeOptions = libraries.flatMap((library) =>
    heroTemplates(pack, library).map((template) => ({ library, template })),
  );
  const homeTitleCount = (templateId: string) =>
    homeOptions.filter((option) => option.template.id === templateId).length;

  return (
    <div className="border-border grid gap-3 rounded-xl border p-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <div className="text-sm font-medium">Also use the pack's hero banners</div>
          <div className="text-muted-foreground text-[13px]">
            Off keeps the hero banners you have now.
          </div>
        </div>
        <Switch
          checked={on}
          disabled={locked}
          onCheckedChange={onToggle}
          aria-label="Also use the pack's hero banners"
        />
      </div>
      <div className={cn("grid gap-2", !on && "opacity-55")}>
        {!on ? <div className="text-[13px] font-medium">If you turn it on</div> : null}
        <HeroLine
          testId="hero-home"
          icon={<House aria-hidden className="size-3.5" />}
          page="Home"
          label="Hero banner on Home"
          disabled={!on || locked}
          value={heroes.home}
          options={homeOptions.map(({ library, template }) => ({
            value: `${library.id}:${template.id}`,
            label:
              homeTitleCount(template.id) > 1
                ? `${template.title} · ${library.name}`
                : template.title,
          }))}
          current={heroOf(0)}
          onChange={(value) => onChange({ ...heroes, home: value })}
        />
        {libraries.map((library, index) => (
          <HeroLine
            key={library.id}
            testId={`hero-library-${library.id}`}
            icon={<LibraryBig aria-hidden className="size-3.5" />}
            page={`${library.name} page`}
            label={`Hero banner on ${libraryPageLabel(library.name)}`}
            disabled={!on || locked}
            value={heroes.libraries[library.id] ?? KEEP_CURRENT}
            options={heroTemplates(pack, library).map((template) => ({
              value: template.id,
              label: template.title,
            }))}
            current={heroOf(index + 1)}
            onChange={(value) =>
              onChange({ ...heroes, libraries: { ...heroes.libraries, [library.id]: value } })
            }
          />
        ))}
      </div>
    </div>
  );
}

function HeroLine({
  testId,
  icon,
  page,
  label,
  disabled,
  value,
  options,
  current,
  onChange,
}: {
  testId: string;
  icon: ReactNode;
  page: string;
  label: string;
  disabled: boolean;
  value: string;
  options: { value: string; label: string }[];
  /** The page's hero today, once its rows have loaded. */
  current: { title: string | null } | null;
  onChange: (value: string) => void;
}) {
  let note: ReactNode = null;
  if (current && !current.title) {
    note = "No hero banner there now";
  } else if (current?.title) {
    note = (
      <>
        {value === KEEP_CURRENT ? "Keeps " : "replaces "}
        <b className="text-foreground/85 font-medium">{current.title}</b>
      </>
    );
  }
  return (
    <div
      data-testid={testId}
      className="text-muted-foreground grid grid-cols-[16px_minmax(0,1fr)] items-center gap-x-2.5 gap-y-1 text-[13px] sm:grid-cols-[16px_120px_minmax(0,1fr)]"
    >
      {icon}
      <span>{page}</span>
      <span className="col-span-2 flex flex-wrap items-center gap-2 sm:col-span-1">
        <Select value={value} onValueChange={onChange} disabled={disabled}>
          <SelectTrigger aria-label={label} className="h-[30px] w-auto max-w-full text-[13px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={KEEP_CURRENT}>Keep current</SelectItem>
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {note ? <span>{note}</span> : null}
      </span>
    </div>
  );
}

/** What the finished job did, per library; focus lands here when it ends. */
function ApplyResult({
  ref,
  pack,
  succeeded,
  added,
  failure,
  result,
}: {
  ref: Ref<HTMLElement>;
  pack: StarterPack;
  succeeded: boolean;
  /** Something new landed, or nothing failed. */
  added: boolean;
  failure: string | undefined;
  result: ApplyCollectionTemplateBundleResponse | undefined;
}) {
  const id = useId();
  const heading = packResultHeading(pack.title, succeeded, added);
  const rows = result ? appliedRows(result, pack) : [];
  const lines: string[] = [];
  if (!succeeded) {
    lines.push(failure ?? "The job stopped before it finished.");
  } else {
    for (const row of rows) {
      if (row.added.length > 0)
        lines.push(`Added ${plural(row.added.length, "list")} to ${row.libraryName}.`);
      for (const entry of row.failed)
        lines.push(`Couldn't add ${entry.title} to ${row.libraryName}: ${entry.reason}.`);
    }
    if (rows.every((row) => row.added.length === 0)) lines.unshift("Nothing new was added.");
    for (const entry of result?.featured ?? [])
      lines.push(`${heroEntryLabel(entry)}: ${entry.template_title}.`);
    for (const entry of result?.featured_failed ?? [])
      lines.push(
        `Couldn't set the ${heroEntryLabel(entry).toLowerCase()}: ${failureReason(entry.reason)}.`,
      );
    if ((result?.sync_queued?.length ?? 0) > 0)
      lines.push("First syncs run in the background; lists fill in as each one finishes.");
  }
  return (
    <section
      ref={ref}
      tabIndex={-1}
      aria-labelledby={`${id}-title`}
      className={cn(
        "grid gap-1 rounded-xl border p-4 text-[13px] outline-none focus-visible:ring-[3px]",
        succeeded ? "border-border bg-muted/30" : "border-destructive/40 bg-destructive/5",
      )}
    >
      <h4 id={`${id}-title`} className="text-sm font-medium">
        {heading}
      </h4>
      {lines.map((line) => (
        <p key={line}>{line}</p>
      ))}
    </section>
  );
}
