import {
  useCallback,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Lock } from "lucide-react";

import type {
  RestoreItem,
  RestoreSection,
  RestoreSectionId,
  RestoreSet,
} from "@/api/v2/adminBackupRestore";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { prefersReducedMotion } from "@/hooks/useImmediateSidebarCollapse";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { Disclosure } from "./Disclosure";
import { ItemRow, SameRow, type ToggleKeys } from "./RestoreRows";
import {
  FILTER_THRESHOLD,
  STATE_ORDER,
  countStates,
  groupByLibrary,
  groupSectionItems,
  groupsByLibrary,
  libraryNames,
  matchesFilter,
  nothingToRestoreText,
  rowSecondary,
  sectionProgress,
  stateCountsLabel,
  stateCountsText,
  type BlockedGroup,
  type ItemState,
  type LibraryNames,
  type StateCounts,
} from "./restoreGroups";
import {
  SECTION_TITLES,
  chooseItems,
  indexItems,
  itemKey,
  neverRestoredSettings,
  restorePlan,
  unchooseItems,
  type SelectionKeys,
} from "./restoreModel";

const BAR_COLORS: Record<ItemState, string> = {
  differs: "bg-[var(--settings-accent)]",
  new: "bg-info",
  blocked: "bg-amber-500",
  same: "bg-muted-foreground/30",
};

/**
 * A section's items by state as one thin bar, with the counts in words
 * beside it. Spans only: it sits inside the section header's paragraph.
 */
function DifferenceBar({ counts }: { counts: StateCounts }) {
  const total = STATE_ORDER.reduce((sum, state) => sum + counts[state], 0);
  if (total === 0) return null;
  return (
    <span className="mt-1.5 flex min-w-0 items-center gap-2">
      <span
        role="img"
        aria-label={stateCountsLabel(counts)}
        className="flex h-1.5 w-24 shrink-0 gap-px overflow-hidden rounded-full sm:w-32"
      >
        {STATE_ORDER.map((state) =>
          counts[state] > 0 ? (
            <span key={state} className={BAR_COLORS[state]} style={{ flexGrow: counts[state] }} />
          ) : null,
        )}
      </span>
      <span
        aria-hidden="true"
        className="text-muted-foreground min-w-0 truncate text-xs tabular-nums"
      >
        {stateCountsText(counts)}
      </span>
    </span>
  );
}

function sectionAnchor(id: RestoreSectionId) {
  return `restore-section-${id}`;
}

function scrollToSection(id: RestoreSectionId) {
  document.getElementById(sectionAnchor(id))?.scrollIntoView({
    block: "start",
    behavior: prefersReducedMotion() ? "auto" : "smooth",
  });
}

/** Each section with how much of it is chosen; links to the section. */
function SectionIndex({
  sections,
  selection,
  variant,
}: {
  sections: readonly RestoreSection[];
  selection: SelectionKeys;
  variant: "list" | "chips";
}) {
  if (variant === "chips") {
    return (
      <nav aria-label="Sections" className="-mx-1 overflow-x-auto px-1 pb-1 lg:hidden">
        <ul className="flex w-max gap-2">
          {sections.map((section) => (
            <li key={section.id}>
              <button
                type="button"
                onClick={() => scrollToSection(section.id)}
                className="border-border hover:bg-accent focus-visible:ring-ring/50 rounded-full border px-3 py-1 text-xs whitespace-nowrap outline-none focus-visible:ring-[3px]"
              >
                {SECTION_TITLES[section.id]}
                <span className="text-muted-foreground">
                  {" "}
                  · {sectionProgress(section, selection)}
                </span>
              </button>
            </li>
          ))}
        </ul>
      </nav>
    );
  }
  return (
    <nav aria-label="Sections" className="hidden lg:block">
      <ul className="space-y-0.5">
        {sections.map((section) => (
          <li key={section.id}>
            <button
              type="button"
              onClick={() => scrollToSection(section.id)}
              className="hover:bg-accent focus-visible:ring-ring/50 flex w-full items-baseline justify-between gap-3 rounded-md px-2 py-1 text-left text-[13px] outline-none focus-visible:ring-[3px]"
            >
              <span className="truncate">{SECTION_TITLES[section.id]}</span>
              <span className="text-muted-foreground shrink-0 text-xs tabular-nums">
                {sectionProgress(section, selection)}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </nav>
  );
}

interface RowsProps {
  sectionId: RestoreSectionId;
  items: readonly RestoreItem[];
  selection: SelectionKeys;
  onToggle: ToggleKeys;
  names: LibraryNames;
  /** Name each row's library: the list isn't under library headings. */
  withLibrary?: boolean;
}

function Rows({ sectionId, items, selection, onToggle, names, withLibrary = false }: RowsProps) {
  return (
    <ul>
      {items.map((item) => (
        <ItemRow
          key={item.id}
          sectionId={sectionId}
          item={item}
          checked={selection.has(itemKey(sectionId, item.id))}
          onToggle={onToggle}
          secondary={rowSecondary(item, names, withLibrary)}
        />
      ))}
    </ul>
  );
}

/** A library's restorable items under a heading that folds them away. */
function LibraryGroupRows({
  name,
  items,
  ...rows
}: Omit<RowsProps, "withLibrary"> & { name: string }) {
  const listId = useId();
  const [open, setOpen] = useState(true);
  return (
    <div className="pt-2">
      <Disclosure
        open={open}
        onOpenChange={setOpen}
        controls={listId}
        className="text-foreground gap-1.5 text-[13px] font-medium"
      >
        {name}
        <span className="text-muted-foreground font-normal tabular-nums">
          {stateCountsText(countStates(items))}
        </span>
      </Disclosure>
      {open ? (
        <div id={listId} className="pl-5">
          <Rows items={items} {...rows} />
        </div>
      ) : null}
    </div>
  );
}

/** One reason items can't be restored: a line, with the items behind a disclosure. */
function BlockedGroupRows({ group, ...rows }: Omit<RowsProps, "items"> & { group: BlockedGroup }) {
  const listId = useId();
  const [open, setOpen] = useState(false);
  return (
    <div className="border-border/60 border-t py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="flex items-center gap-1.5 text-[13px]">
          <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-amber-500" />
          {group.title}
        </span>
        <Disclosure open={open} onOpenChange={setOpen} controls={listId}>
          {open ? "Hide them" : "Show them"}
        </Disclosure>
      </div>
      {open ? (
        <div id={listId} className="pt-1 pl-3">
          <Rows items={group.items} withLibrary {...rows} />
        </div>
      ) : null}
    </div>
  );
}

/** Items already the same here, folded away; rendered only when open. */
function SameRows({
  items,
  names,
  label,
}: {
  items: readonly RestoreItem[];
  names: LibraryNames;
  label: string;
}) {
  const listId = useId();
  const [open, setOpen] = useState(false);
  return (
    <div className="border-border/60 border-t py-2">
      <Disclosure open={open} onOpenChange={setOpen} controls={listId}>
        {label}
      </Disclosure>
      {open ? (
        <ul
          id={listId}
          className="bg-background/60 mt-1.5 max-h-80 overflow-y-auto rounded-md border px-3 py-1"
        >
          {items.map((item) => (
            <SameRow key={item.id} item={item} secondary={rowSecondary(item, names, true)} />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

/** Settings that belong to this server, behind one line. */
function NeverRestored({ keys }: { keys: readonly string[] }) {
  const listId = useId();
  const [open, setOpen] = useState(false);
  if (keys.length === 0) return null;
  return (
    <div className="border-border/60 border-t py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-muted-foreground flex items-center gap-1.5 text-[13px]">
          <Lock aria-hidden="true" className="size-3.5 shrink-0" />
          Settings that belong to this server are never restored.
        </span>
        <Disclosure open={open} onOpenChange={setOpen} controls={listId}>
          {open ? "Hide them" : "Show them"}
        </Disclosure>
      </div>
      {open ? (
        <p id={listId} className="text-muted-foreground pt-1 pl-5 text-xs leading-relaxed">
          {neverRestoredSettings(keys).map((key, position) => (
            <span key={key}>
              {position > 0 ? ", " : null}
              <code className="font-mono">{key}</code>
            </span>
          ))}
        </p>
      ) : null}
    </div>
  );
}

function SectionBlock({
  section,
  selection,
  onToggle,
  names,
  bottom,
}: {
  section: RestoreSection;
  selection: SelectionKeys;
  onToggle: ToggleKeys;
  names: LibraryNames;
  /** Shown under the groups: the never-restored note. */
  bottom?: ReactNode;
}) {
  const filterId = useId();
  const sameListId = useId();
  const [query, setQuery] = useState("");
  const [showAll, setShowAll] = useState(false);
  const counts = useMemo(() => countStates(section.items), [section]);
  const visible = useMemo(
    () =>
      query.trim()
        ? section.items.filter((item) => matchesFilter(item, query, names))
        : section.items,
    [section, query, names],
  );
  const groups = useMemo(() => groupSectionItems(visible), [visible]);
  const libraryGroups = useMemo(
    () => (groupsByLibrary(section.id) ? groupByLibrary(groups.restorable, names) : null),
    [section.id, groups.restorable, names],
  );

  const restorable = counts.differs + counts.new;
  const expanded = restorable > 0 || showAll;
  const filterable = section.items.length > FILTER_THRESHOLD && expanded;
  const rows = { sectionId: section.id, selection, onToggle, names };

  // "All" chooses what's shown: every restorable item, or those the filter left.
  const allKeys = groups.restorable.map((item) => itemKey(section.id, item.id));
  const chosenShown = groups.restorable.filter((item) =>
    selection.has(itemKey(section.id, item.id)),
  ).length;
  const allState =
    chosenShown === 0 ? false : chosenShown === groups.restorable.length ? true : "indeterminate";
  const allId = `${sectionAnchor(section.id)}-all`;

  const nothingMatches =
    visible.length === 0 && query.trim() !== "" ? (
      <p className="text-muted-foreground py-3 text-[13px]">No matches.</p>
    ) : null;

  return (
    <div id={sectionAnchor(section.id)} className="scroll-mt-6">
      <SettingsGroup
        title={SECTION_TITLES[section.id]}
        description={
          section.count === 0 ? "Nothing in this backup." : <DifferenceBar counts={counts} />
        }
        actions={
          groups.restorable.length > 0 ? (
            <span className="flex items-center gap-2">
              <Checkbox
                id={allId}
                checked={allState}
                onCheckedChange={(value) => onToggle(allKeys, value === true)}
              />
              <Label htmlFor={allId} className="text-muted-foreground text-xs">
                {query.trim() ? "All shown" : "All"}
              </Label>
            </span>
          ) : undefined
        }
        flush
        className="min-w-0"
      >
        <div className="min-w-0 lg:@[52rem]/settings-section:pt-3.5">
          {section.items.length > 0 && restorable === 0 ? (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2">
              <span className="text-muted-foreground text-[13px]">
                {nothingToRestoreText(counts)}
              </span>
              <Disclosure open={showAll} onOpenChange={setShowAll} controls={sameListId}>
                {showAll ? "Hide them" : "Show them"}
              </Disclosure>
            </div>
          ) : null}
          {expanded ? (
            <div id={sameListId}>
              {filterable ? (
                <div className="py-2">
                  <Label htmlFor={filterId} className="sr-only">
                    Filter {SECTION_TITLES[section.id].toLowerCase()}
                  </Label>
                  <Input
                    id={filterId}
                    type="search"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder="Filter by name or library"
                    className="border-muted-foreground/25 h-8 max-w-sm text-sm"
                  />
                </div>
              ) : null}
              {nothingMatches}
              {libraryGroups ? (
                libraryGroups.map((group) => (
                  <LibraryGroupRows
                    key={group.key}
                    name={group.name}
                    items={group.items}
                    {...rows}
                  />
                ))
              ) : groups.restorable.length > 0 ? (
                <Rows items={groups.restorable} {...rows} />
              ) : null}
              {groups.blocked.map((group) => (
                <BlockedGroupRows key={group.key} group={group} {...rows} />
              ))}
              {groups.same.length > 0 ? (
                <SameRows
                  items={groups.same}
                  names={names}
                  label={
                    groups.same.length === 1
                      ? "1 matches this server"
                      : `${groups.same.length} match this server`
                  }
                />
              ) : null}
            </div>
          ) : null}
          {bottom}
        </div>
      </SettingsGroup>
    </div>
  );
}

export interface RestoreChooseProps {
  set: RestoreSet;
  selection: SelectionKeys;
  onSelectionChange: (selection: Set<string>) => void;
  onReview: () => void;
}

/** Step 1: what differs, and what to bring back, with the plan so far beside it. */
export function RestoreChoose({ set, selection, onSelectionChange, onReview }: RestoreChooseProps) {
  const index = useMemo(() => indexItems(set), [set]);
  const [notice, setNotice] = useState<string | null>(null);
  const libraries = useAdminLibraries();
  const names = useMemo(() => libraryNames(set, libraries.data ?? []), [set, libraries.data]);
  const plan = restorePlan(set, selection);

  // Rows get one toggle for the page's life, so a click re-renders only the
  // rows whose checkbox changed. It reads the latest selection from a ref.
  const selectionRef = useRef(selection);
  useLayoutEffect(() => {
    selectionRef.current = selection;
  }, [selection]);
  const onToggle = useCallback<ToggleKeys>(
    (keys, checked) => {
      const current = selectionRef.current;
      const result = checked
        ? chooseItems(index, current, keys)
        : unchooseItems(index, current, keys);
      setNotice(result.notice);
      onSelectionChange(result.selection);
    },
    [index, onSelectionChange],
  );

  return (
    <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <div className="min-w-0 space-y-5">
        <SectionIndex sections={set.sections} selection={selection} variant="chips" />
        {!set.key_matches ? (
          <SettingFieldStatus tone="warn">
            This backup was taken with a different SECRET_KEY. Encrypted settings and plugin
            settings in it can&apos;t be restored here.
          </SettingFieldStatus>
        ) : null}
        {set.missing_blobs.length > 0 ? (
          <div>
            <SettingFieldStatus tone="warn">
              {set.missing_blobs.length === 1
                ? "1 file was missing when the backup was taken. Items that use it say so."
                : `${set.missing_blobs.length} files were missing when the backup was taken. Items that use them say so.`}
            </SettingFieldStatus>
          </div>
        ) : null}
        {set.sections.map((section) => (
          <SectionBlock
            key={section.id}
            section={section}
            selection={selection}
            onToggle={onToggle}
            names={names}
            bottom={
              section.id === "settings" ? (
                <NeverRestored keys={set.never_restored_settings} />
              ) : undefined
            }
          />
        ))}
      </div>

      <aside
        aria-labelledby="restore-summary-heading"
        className="surface-panel-subtle h-fit space-y-4 rounded-xl p-5 lg:sticky lg:top-6"
      >
        <SectionIndex sections={set.sections} selection={selection} variant="list" />
        <h2 id="restore-summary-heading" className="text-[15px] font-semibold tracking-tight">
          Silo will
        </h2>
        {plan.length === 0 ? (
          <p className="text-muted-foreground text-sm">Choose what to restore.</p>
        ) : (
          <ul className="list-disc space-y-1.5 pl-5 text-sm">
            {plan.flatMap((group) =>
              group.sentences.map((sentence, position) => (
                <li key={`${group.section}-${position}`}>{sentence}.</li>
              )),
            )}
          </ul>
        )}
        {notice ? (
          <p role="status" className="text-[13px] text-amber-600 dark:text-amber-400">
            {notice}
          </p>
        ) : null}
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Before changing anything, Silo saves a backup of this server as it is now. That copy is a
          recovery point for a full restore.
        </p>
        <Button className="w-full" disabled={selection.size === 0} onClick={onReview}>
          Review restore
        </Button>
      </aside>
    </div>
  );
}
