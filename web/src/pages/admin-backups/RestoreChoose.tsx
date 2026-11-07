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
  RestoreDataTypeId,
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
import { useAppLanguage, useT } from "@/i18n";
import { pendingCount } from "@/lib/profileDataTypes";
import { prefersReducedMotion } from "@/hooks/useImmediateSidebarCollapse";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { OmittedAccounts } from "./BackupWarningDetails";
import { Disclosure } from "./Disclosure";
import { ItemRow, SameRow, type ToggleKeys } from "./RestoreRows";
import { AccountRow, DataTypesPanel } from "./RestoreUsers";
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
  chosenItems,
  chosenProfiles,
  indexItems,
  itemKey,
  neverRestoredSettings,
  profileKey,
  restorePlan,
  toggleKeys,
  unchooseItems,
  type SelectionKeys,
} from "./restoreModel";

/** Between the never-restored setting keys. */
const KEY_SEPARATOR = ", ";

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
  useT();
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
  const t = useT();
  if (variant === "chips") {
    return (
      <nav
        aria-label={t("web.admin.backups.restore.choose.sectionsLabel")}
        className="-mx-1 overflow-x-auto px-1 pb-1 lg:hidden"
      >
        <ul className="flex w-max gap-2">
          {sections.map((section) => (
            <li key={section.id}>
              <button
                type="button"
                onClick={() => scrollToSection(section.id)}
                className="border-border hover:bg-accent focus-visible:ring-ring/50 rounded-full border px-3 py-1 text-xs whitespace-nowrap outline-none focus-visible:ring-[3px]"
              >
                {t("web.admin.backups.restore.choose.chip", {
                  section: t(SECTION_TITLES[section.id]),
                  progress: sectionProgress(section, selection),
                  muted: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
                })}
              </button>
            </li>
          ))}
        </ul>
      </nav>
    );
  }
  return (
    <nav
      aria-label={t("web.admin.backups.restore.choose.sectionsLabel")}
      className="hidden lg:block"
    >
      <ul className="space-y-0.5">
        {sections.map((section) => (
          <li key={section.id}>
            <button
              type="button"
              onClick={() => scrollToSection(section.id)}
              className="hover:bg-accent focus-visible:ring-ring/50 flex w-full items-baseline justify-between gap-3 rounded-md px-2 py-1 text-start text-[13px] outline-none focus-visible:ring-[3px]"
            >
              <span className="truncate">{t(SECTION_TITLES[section.id])}</span>
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

/** Joined ids of an account's chosen profiles, the cheap prop AccountRow compares. */
function chosenProfileIds(item: RestoreItem, selection: SelectionKeys): string {
  return (item.account?.profiles ?? [])
    .filter((profile) => selection.has(profileKey(item.id, profile.id)))
    .map((profile) => profile.id)
    .join("\u0000");
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
  useT();
  return (
    <ul>
      {items.map((item) =>
        item.account ? (
          <AccountRow
            key={item.id}
            item={item}
            checked={selection.has(itemKey(sectionId, item.id))}
            chosenProfiles={chosenProfileIds(item, selection)}
            onToggle={onToggle}
          />
        ) : (
          <ItemRow
            key={item.id}
            sectionId={sectionId}
            item={item}
            checked={selection.has(itemKey(sectionId, item.id))}
            onToggle={onToggle}
            secondary={rowSecondary(item, names, withLibrary)}
          />
        ),
      )}
    </ul>
  );
}

/** A library's restorable items under a heading that folds them away. */
function LibraryGroupRows({
  name,
  items,
  ...rows
}: Omit<RowsProps, "withLibrary"> & { name: string }) {
  useT();
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
        <div id={listId} className="ps-5">
          <Rows items={items} {...rows} />
        </div>
      ) : null}
    </div>
  );
}

/** One reason items can't be restored: a line, with the items behind a disclosure. */
function BlockedGroupRows({ group, ...rows }: Omit<RowsProps, "items"> & { group: BlockedGroup }) {
  const t = useT();
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
          {open
            ? t("web.admin.backups.restore.choose.hideThem")
            : t("web.admin.backups.restore.choose.showThem")}
        </Disclosure>
      </div>
      {open ? (
        <div id={listId} className="ps-3 pt-1">
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
  useT();
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
  const t = useT();
  const listId = useId();
  const [open, setOpen] = useState(false);
  if (keys.length === 0) return null;
  return (
    <div className="border-border/60 border-t py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-muted-foreground flex items-center gap-1.5 text-[13px]">
          <Lock aria-hidden="true" className="size-3.5 shrink-0" />
          {t("web.admin.backups.restore.choose.neverRestored")}
        </span>
        <Disclosure open={open} onOpenChange={setOpen} controls={listId}>
          {open
            ? t("web.admin.backups.restore.choose.hideThem")
            : t("web.admin.backups.restore.choose.showThem")}
        </Disclosure>
      </div>
      {open ? (
        <p id={listId} className="text-muted-foreground ps-5 pt-1 text-xs leading-relaxed">
          {neverRestoredSettings(keys).map((key, position) => (
            <span key={key}>
              {position > 0 ? KEY_SEPARATOR : null}
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
  top,
  bottom,
}: {
  section: RestoreSection;
  selection: SelectionKeys;
  onToggle: ToggleKeys;
  names: LibraryNames;
  /** Shown above the rows: the data types panel. */
  top?: ReactNode;
  /** Shown under the groups: omitted accounts, the never-restored note. */
  bottom?: ReactNode;
}) {
  const t = useT();
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
  // Group headings and library names are resolved here: rebuild them on a language change.
  const language = useAppLanguage();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- headings are resolved inside the helper
  const groups = useMemo(() => groupSectionItems(visible), [visible, language]);
  const libraryGroups = useMemo(
    () => (groupsByLibrary(section.id) ? groupByLibrary(groups.restorable, names) : null),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- group names are resolved inside the helper
    [section.id, groups.restorable, names, language],
  );

  const restorable = counts.differs + counts.new;
  const expanded = restorable > 0 || showAll;
  const filterable = section.items.length > FILTER_THRESHOLD && expanded;
  const rows = { sectionId: section.id, selection, onToggle, names };

  // "All" chooses what's shown: every restorable item, or those the filter left.
  const allKeys = groups.restorable.flatMap((item) => toggleKeys(section.id, item));
  const chosenShown = groups.restorable.filter((item) =>
    selection.has(itemKey(section.id, item.id)),
  ).length;
  const allState =
    chosenShown === 0 ? false : chosenShown === groups.restorable.length ? true : "indeterminate";
  const allId = `${sectionAnchor(section.id)}-all`;

  const nothingMatches =
    visible.length === 0 && query.trim() !== "" ? (
      <p className="text-muted-foreground py-3 text-[13px]">
        {t("web.admin.backups.restore.choose.noMatches")}
      </p>
    ) : null;

  return (
    <div id={sectionAnchor(section.id)} className="scroll-mt-6">
      <SettingsGroup
        title={t(SECTION_TITLES[section.id])}
        description={
          section.count === 0 ? (
            t("web.admin.backups.restore.choose.emptySection")
          ) : (
            <DifferenceBar counts={counts} />
          )
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
                {query.trim()
                  ? t("web.admin.backups.restore.choose.allShown")
                  : t("web.admin.backups.restore.choose.all")}
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
                {showAll
                  ? t("web.admin.backups.restore.choose.hideThem")
                  : t("web.admin.backups.restore.choose.showThem")}
              </Disclosure>
            </div>
          ) : null}
          {expanded ? (
            <div id={sameListId}>
              {top}
              {filterable ? (
                <div className="py-2">
                  <Label htmlFor={filterId} className="sr-only">
                    {t("web.admin.backups.restore.choose.filterLabel", { section: section.id })}
                  </Label>
                  <Input
                    id={filterId}
                    type="search"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder={
                      section.id === "users"
                        ? t("web.admin.backups.restore.choose.filterUsers")
                        : t("web.admin.backups.restore.choose.filterItems")
                    }
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
                  label={t("web.admin.backups.restore.choose.sameLabel", {
                    count: groups.same.length,
                    count_text: String(groups.same.length),
                  })}
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
  dataTypes: ReadonlySet<RestoreDataTypeId>;
  onDataTypesChange: (dataTypes: Set<RestoreDataTypeId>) => void;
  onReview: () => void;
}

/** Step 1: what differs, and what to bring back, with the plan so far beside it. */
export function RestoreChoose({
  set,
  selection,
  onSelectionChange,
  dataTypes,
  onDataTypesChange,
  onReview,
}: RestoreChooseProps) {
  const t = useT();
  const index = useMemo(() => indexItems(set), [set]);
  const [notice, setNotice] = useState<string | null>(null);
  const libraries = useAdminLibraries();
  const names = useMemo(() => libraryNames(set, libraries.data ?? []), [set, libraries.data]);
  const plan = restorePlan(set, selection, dataTypes);

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

  const chosenAccounts = chosenItems(set, selection).filter((entry) => entry.section === "users");
  const dataCounts = new Map<RestoreDataTypeId, number>();
  for (const entry of chosenAccounts) {
    for (const profile of chosenProfiles(entry.item, selection)) {
      for (const data of profile.data_types) {
        dataCounts.set(data.id, (dataCounts.get(data.id) ?? 0) + pendingCount(data));
      }
    }
  }

  return (
    <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <div className="min-w-0 space-y-5">
        <SectionIndex sections={set.sections} selection={selection} variant="chips" />
        {!set.key_matches ? (
          <SettingFieldStatus tone="warn">
            {t("web.admin.backups.restore.choose.otherKey")}
          </SettingFieldStatus>
        ) : null}
        {set.omitted.length > 0 ? (
          <div>
            <SettingFieldStatus tone="warn">
              {t("web.admin.backups.restore.choose.omitted", {
                count: set.omitted.length,
                count_text: String(set.omitted.length),
              })}
            </SettingFieldStatus>
          </div>
        ) : null}
        {set.missing_blobs.length > 0 ? (
          <div>
            <SettingFieldStatus tone="warn">
              {t("web.admin.backups.restore.choose.missingFiles", {
                count: set.missing_blobs.length,
                count_text: String(set.missing_blobs.length),
              })}
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
            top={
              section.id === "users" && chosenAccounts.length > 0 ? (
                <DataTypesPanel
                  dataTypes={dataTypes}
                  onChange={onDataTypesChange}
                  counts={dataCounts}
                />
              ) : undefined
            }
            bottom={
              section.id === "users" && set.omitted.length > 0 ? (
                <div className="border-border/60 border-t py-2">
                  <OmittedAccounts omitted={set.omitted} context="restore" />
                </div>
              ) : section.id === "settings" ? (
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
          {t("web.admin.backups.restore.choose.summaryHeading")}
        </h2>
        {plan.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {t("web.admin.backups.restore.choose.nothingChosen")}
          </p>
        ) : (
          <ul className="list-disc space-y-1.5 ps-5 text-sm">
            {plan.flatMap((group) =>
              group.sentences.map((sentence, position) => (
                <li key={`${group.section}-${position}`}>
                  {t("web.admin.backups.restore.plan.sentence", { sentence })}
                </li>
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
          {t("web.admin.backups.restore.choose.recoveryPoint")}
        </p>
        <Button className="w-full" disabled={selection.size === 0} onClick={onReview}>
          {t("web.admin.backups.restore.choose.review")}
        </Button>
      </aside>
    </div>
  );
}
