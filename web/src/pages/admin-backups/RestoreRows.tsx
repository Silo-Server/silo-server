import { Fragment, memo, useId, useState, type ReactNode } from "react";

import type {
  RestoreChange,
  RestoreItem,
  RestoreSectionId,
  RestoreSetting,
} from "@/api/v2/adminBackupRestore";
import { PathList } from "@/components/PathList";
import { RestartBadge } from "@/components/settings/RestartBadge";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import {
  NARROW_OVERFLOW_CLIP,
  OVERFLOW_CLIP,
  useOverflowsInline,
} from "@/hooks/useOverflowsInline";
import { useT, type MessageKey, type Translate } from "@/i18n";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { SettingFieldStatus } from "@/pages/admin-settings/SettingField";

import { RowDisclosure } from "./Disclosure";
import { itemState, type ItemState } from "./restoreGroups";
import { REASON_SENTENCES, displayLabel, nameDir, toggleKeys } from "./restoreModel";

/** Toggles item keys; stable for the page's life so memoized rows don't re-render. */
export type ToggleKeys = (keys: string[], checked: boolean) => void;

const STATE_TAGS: Record<ItemState, { label: MessageKey; className: string }> = {
  differs: {
    label: "web.admin.backups.restore.tag.differs",
    className: "border-[var(--settings-accent-line)] text-[var(--settings-accent)]",
  },
  new: { label: "web.admin.backups.restore.tag.new", className: "border-info/30 text-info" },
  blocked: {
    label: "web.admin.backups.restore.tag.blocked",
    className: "border-amber-500/30 text-amber-600 dark:text-amber-400",
  },
  same: {
    label: "web.admin.backups.restore.tag.same",
    className: "border-border text-muted-foreground",
  },
};

export function StateTag({ state }: { state: ItemState }) {
  const t = useT();
  const tag = STATE_TAGS[state];
  return (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center rounded-md border px-1.5 text-[11px] font-medium whitespace-nowrap",
        tag.className,
      )}
    >
      {t(tag.label)}
    </span>
  );
}

function settingValues(t: Translate, setting: RestoreSetting): { now: string; backup: string } {
  const set = t("web.admin.backups.restore.setting.set");
  const empty = t("web.admin.backups.restore.setting.empty");
  const current = setting.current_value || empty;
  const backup = setting.backup_value || empty;
  if (setting.status === "needs_another_key") {
    return {
      now: setting.masked ? set : current,
      backup: t("web.admin.backups.restore.setting.otherKey"),
    };
  }
  if (setting.status === "cleared") {
    return {
      now: setting.masked ? set : current,
      backup: setting.masked
        ? t("web.admin.backups.restore.setting.cleared")
        : t("web.admin.backups.restore.setting.turnedOff", { value: backup }),
    };
  }
  if (setting.status === "unverified_backup") {
    return {
      now: setting.masked ? set : current,
      backup: t("web.admin.backups.restore.setting.unverified"),
    };
  }
  if (setting.status === "kept_stricter") {
    return {
      now: t("web.admin.backups.restore.setting.keptStricter", { value: current }),
      backup,
    };
  }
  if (setting.status === "managed_by_environment") {
    return {
      now: t("web.admin.backups.restore.setting.managedByEnvironment", {
        value: setting.masked ? set : current,
      }),
      backup: setting.masked ? t("web.admin.backups.restore.setting.differentEncrypted") : backup,
    };
  }
  if (setting.masked) {
    return { now: set, backup: t("web.admin.backups.restore.setting.differentEncrypted") };
  }
  return { now: current, backup };
}

/**
 * A setting key or dotted name that may break after its dots and underscores,
 * so a narrow column wraps "playback.watched_threshold" or "silo.tmdb 1.2.3"
 * between words, not inside one. A dot before a digit, another dot or a space
 * stays, keeping a version number and an ellipsis whole, and so does a dot or
 * underscore before "*", so a prefix like "s3.*" keeps its "*".
 */
export function BreakableKey({ value }: { value: string }) {
  const parts = value.replace(/(_(?!\*)|\.(?=[^\d.\s*]))/g, "$1\u0000").split("\u0000");
  return (
    <>
      {parts.map((part, position) => (
        <Fragment key={position}>
          {part}
          {position < parts.length - 1 ? <wbr /> : null}
        </Fragment>
      ))}
    </>
  );
}

const PATH_SEPARATOR = ", ";
/** Keeps a path's own spaces, so a copied path names the folder. */
const KEEP_SPACES = "[white-space-collapse:preserve]";

/**
 * Folder paths, each in its own `dir="ltr"` span, so two folders with Arabic
 * names keep their order beside each other. The spans isolate each path in the
 * layout, not with Unicode marks, and keep its spaces, so a copied path is the
 * folder's own path.
 */
function FolderPaths({ paths }: { paths: readonly string[] }) {
  return (
    <>
      {paths.map((path, position) => (
        <Fragment key={position}>
          {position > 0 ? PATH_SEPARATOR : null}
          <span dir="ltr" className={KEEP_SPACES}>
            {path}
          </span>
        </Fragment>
      ))}
    </>
  );
}

const MIDDLE_DOT = "·";

/**
 * A row's muted detail: the library's name, then what the item is here. Each part
 * is cut at its own end, the name in its own direction and the text in the page's,
 * so both keep their start: one cut line would lose the start of whichever part sits
 * at its end. Each part's tooltip is on the part, so it reads in that part's direction
 * too. When both don't fit on one line, the text moves to its own, so neither is cut
 * short unless it is wider than the line on its own. As in UserDetailHeader, the
 * separator sits before the text, pulled back over the gap, so when the text opens a
 * line the separator falls past the clipped start edge.
 */
function DetailLine({
  library,
  text,
  className,
}: {
  library: string | null;
  text: string | null;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "text-muted-foreground flex w-fit max-w-full min-w-0 flex-wrap gap-x-(--sep) overflow-x-clip text-xs [--sep:0.65em]",
        className,
      )}
    >
      {library ? (
        <span dir="auto" title={library} className="min-w-0 truncate">
          {library}
        </span>
      ) : null}
      {text ? (
        <span className={cn("flex min-w-0", library && "-ms-(--sep)")}>
          {library ? (
            <span aria-hidden="true" className="w-(--sep) shrink-0 text-center">
              {MIDDLE_DOT}
            </span>
          ) : null}
          <span title={text} className="min-w-0 truncate">
            {text}
          </span>
        </span>
      ) : null}
    </span>
  );
}

/** Below `sm` each setting stacks: its key, then Now and In backup, each labelled. */
const PHONE_CELL = "max-sm:block max-sm:p-0";

function SettingsDiff({ item }: { item: RestoreItem }) {
  const t = useT();
  const rows = (item.settings ?? []).filter((setting) => setting.status !== "on_this_server");
  return (
    <div className="bg-background/60 overflow-hidden rounded-lg border">
      <Table className="max-sm:block">
        <TableHeader className="max-sm:hidden">
          <TableRow>
            <TableHead>{t("web.admin.backups.restore.setting.column")}</TableHead>
            <TableHead>{t("web.admin.backups.restore.setting.now")}</TableHead>
            <TableHead>{t("web.admin.backups.restore.setting.inBackup")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody className="max-sm:block">
          {rows.map((setting) => {
            const values = settingValues(t, setting);
            return (
              <TableRow key={setting.key} className="max-sm:block max-sm:space-y-1 max-sm:p-3">
                <TableCell className={cn("align-top whitespace-normal", PHONE_CELL)}>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="font-mono text-xs break-words">
                      <BreakableKey value={setting.key} />
                    </span>
                    {setting.restart_required &&
                    (setting.status === "differs" || setting.status === "cleared") ? (
                      <RestartBadge />
                    ) : null}
                  </div>
                  {setting.class === "external_service" ? (
                    <p className="text-muted-foreground mt-0.5 text-xs">
                      {t("web.admin.backups.restore.setting.external")}
                    </p>
                  ) : null}
                </TableCell>
                <TableCell
                  className={cn(
                    "text-muted-foreground align-top text-xs break-words whitespace-normal",
                    PHONE_CELL,
                  )}
                >
                  {t("web.admin.backups.restore.setting.nowCell", {
                    value: values.now,
                    label: (chunks) => <span className="text-foreground sm:hidden">{chunks}</span>,
                  })}
                </TableCell>
                <TableCell
                  className={cn("align-top text-xs break-words whitespace-normal", PHONE_CELL)}
                >
                  {t("web.admin.backups.restore.setting.inBackupCell", {
                    value: values.backup,
                    label: (chunks) => (
                      <span className="text-muted-foreground sm:hidden">{chunks}</span>
                    ),
                  })}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

/** The label of each change code but collection_items, which carries a count. */
const CHANGE_LABELS: Record<Exclude<RestoreChange, "collection_items">, MessageKey> = {
  type: "web.admin.backups.restore.rows.change.type",
  title: "web.admin.backups.restore.rows.change.title",
  title_translation: "web.admin.backups.restore.rows.change.titleTranslation",
  position: "web.admin.backups.restore.rows.change.position",
  featured: "web.admin.backups.restore.rows.change.featured",
  item_limit: "web.admin.backups.restore.rows.change.itemLimit",
  enabled: "web.admin.backups.restore.rows.change.enabled",
  web_only: "web.admin.backups.restore.rows.change.webOnly",
  collection_replaced: "web.admin.backups.restore.rows.change.collectionReplaced",
  configuration: "web.admin.backups.restore.rows.change.configuration",
  description: "web.admin.backups.restore.rows.change.description",
  visibility: "web.admin.backups.restore.rows.change.visibility",
  sort_order: "web.admin.backups.restore.rows.change.sortOrder",
  item_sort: "web.admin.backups.restore.rows.change.itemSort",
  source: "web.admin.backups.restore.rows.change.source",
  sync_schedule: "web.admin.backups.restore.rows.change.syncSchedule",
  poster: "web.admin.backups.restore.rows.change.poster",
  backdrop: "web.admin.backups.restore.rows.change.backdrop",
  libraries: "web.admin.backups.restore.rows.change.libraries",
};

/** One entry of a row's change list in the interface language. */
function changeLabel(t: Translate, item: RestoreItem, change: RestoreChange): string {
  if (change === "collection_items") {
    const count = item.home_section?.missing_collection_items ?? 0;
    return t("web.admin.backups.restore.rows.change.collectionItems", { count, count_text: count });
  }
  // Response enums are additive: a code this build has no label for, or an
  // older server's English word, shows as sent rather than blank.
  return Object.hasOwn(CHANGE_LABELS, change) ? t(CHANGE_LABELS[change]) : change;
}

/** Whether a row has anything for its chevron to open; cheap, unlike building it. */
function hasItemDetails(item: RestoreItem): boolean {
  return Boolean(
    item.message ||
    item.reason ||
    item.missing_blobs.length > 0 ||
    item.restart_required ||
    (item.home_section?.changes ?? item.collection?.changes ?? []).length > 0 ||
    (item.settings ?? []).some((setting) => setting.status !== "on_this_server") ||
    (item.library?.paths.length ?? 0) > 1 ||
    (item.library?.missing_paths?.length ?? 0) > 0,
  );
}

/** What a row's chevron opens; null when there is nothing more to say. */
function itemDetails(t: Translate, item: RestoreItem): ReactNode {
  const why = item.message || (item.reason ? t(REASON_SENTENCES[item.reason]) : undefined);
  const changes: readonly RestoreChange[] =
    item.home_section?.changes ?? item.collection?.changes ?? [];
  const settings = (item.settings ?? []).some((setting) => setting.status !== "on_this_server");
  const paths = item.library && item.library.paths.length > 1 ? item.library.paths : [];
  const missingPaths = item.library?.missing_paths ?? [];
  // A status gets a block of its own: SettingFieldStatus is inline, so two in a
  // row would share a line.
  const parts = [
    why ? (
      <div key="why">
        <SettingFieldStatus tone={item.selectable ? "muted" : "warn"}>{why}</SettingFieldStatus>
      </div>
    ) : null,
    paths.length > 0 ? (
      <PathList
        key="paths"
        paths={paths}
        className={cn("text-muted-foreground text-xs", KEEP_SPACES)}
      />
    ) : null,
    missingPaths.length > 0 ? (
      // The list reads left to right inside the sentence, as on the row above.
      <p key="missing-paths" className="text-xs wrap-anywhere text-amber-600 dark:text-amber-400">
        {t("web.admin.backups.restore.rows.cantOpen", {
          paths: (
            <span dir="ltr">
              <FolderPaths paths={missingPaths} />
            </span>
          ),
        })}
      </p>
    ) : null,
    item.missing_blobs.length > 0 ? (
      <div key="files">
        <SettingFieldStatus tone="warn">
          {t("web.admin.backups.restore.rows.filesMissing", {
            count: item.missing_blobs.length,
            count_text: item.missing_blobs.length,
          })}
        </SettingFieldStatus>
      </div>
    ) : null,
    item.restart_required ? (
      <p
        key="restart"
        className="text-muted-foreground flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs text-balance"
      >
        <RestartBadge /> {t("web.admin.backups.restore.rows.restart")}
      </p>
    ) : null,
    changes.length > 0 ? (
      <div key="changes" className="text-xs">
        <p className="text-muted-foreground">{t("web.admin.backups.restore.rows.changes")}</p>
        <ul className="text-muted-foreground list-disc ps-5">
          {changes.map((change) => (
            <li key={change}>{changeLabel(t, item, change)}</li>
          ))}
        </ul>
      </div>
    ) : null,
    settings ? <SettingsDiff key="settings" item={item} /> : null,
  ].filter(Boolean);
  return parts.length > 0 ? parts : null;
}

/**
 * One compact item row: checkbox, label, muted detail, state tag, and a
 * chevron that opens the rest. `checked` and `onToggle` are the only props
 * that change with the selection, so memo keeps the other rows still.
 */
export const ItemRow = memo(function ItemRow({
  sectionId,
  item,
  checked,
  onToggle,
  secondary,
  library = null,
}: {
  sectionId: RestoreSectionId;
  item: RestoreItem;
  checked: boolean;
  onToggle: ToggleKeys;
  secondary: string | null;
  /** The library to name before the detail, in lists not under a library heading. */
  library?: string | null;
}) {
  const t = useT();
  const controlId = useId();
  const detailsId = useId();
  const [open, setOpen] = useState(false);
  const details = open ? itemDetails(t, item) : null;
  const hasDetails = hasItemDetails(item);
  const label = displayLabel(item);
  const [nameRef, nameOverflows] = useOverflowsInline<HTMLSpanElement>(label);
  const paths = item.kind === "library" ? item.library?.paths : undefined;
  const hasDetail = Boolean(secondary || library);
  // The row is its own container, since its width depends on the admin sidebar and
  // restore summary as much as on the viewport. A narrow row wraps the whole name
  // above the muted detail, so names that differ only at the end stay apart.
  return (
    <li className="border-border/60 @container border-b last:border-b-0">
      <div className="flex min-h-10 items-center gap-3 py-1.5">
        {/* Named from the plain label: Chromium reads a <wbr> in it as a space. */}
        <Checkbox
          id={controlId}
          aria-label={label}
          checked={checked}
          disabled={!item.selectable}
          onCheckedChange={(value) => onToggle(toggleKeys(sectionId, item), value === true)}
        />
        <div className="flex min-w-0 flex-1 flex-col gap-x-2 gap-y-0.5 @2xl:flex-row @2xl:items-baseline">
          {/* Beside a detail, a one-line row shares its width 3:2 between the name and
              the detail, each no wider than its text, so either takes the room the other
              doesn't need. Only when both are longer than their shares does the name
              stop at 60% and end in "…"; Chromium breaks at <wbr> even under nowrap, so
              that row hides them. A name with nothing beside it wraps instead, so it is
              never cut short. A name clips sideways only while part of it can't wrap to
              fit, such as one long run of joined emoji; a one-line row with a detail
              truncates instead. The label spans a narrow row's column, so a click
              anywhere on it toggles the row, and min-w-0 lets it shrink to its share of
              a one-line row. The name inside reads in its own direction and is sized to
              its text: a short one stays on the page's start side, and a cut or clipped
              one keeps its start in either script. Its lines line up with the page's
              (align-with-page), so a wrapped one stays beside the checkbox. It is the box
              that's cut, measured and clipped, since it overflows toward its own end. */}
          <Label
            htmlFor={controlId}
            title={label}
            className={cn(
              "block max-w-full shrink-0 text-sm font-medium wrap-anywhere",
              hasDetail && "@2xl:max-w-max @2xl:min-w-0 @2xl:flex-3",
              !item.selectable && "text-muted-foreground",
            )}
          >
            <span
              ref={nameRef}
              dir={nameDir(item)}
              className={cn(
                "align-with-page block w-fit max-w-full",
                hasDetail && "@2xl:truncate @2xl:[&_wbr]:hidden",
                nameOverflows && (hasDetail ? NARROW_OVERFLOW_CLIP : OVERFLOW_CLIP),
              )}
            >
              <BreakableKey value={label} />
            </span>
          </Label>
          {paths && secondary ? (
            // A library's line is its folder paths, which read left to right in any
            // language, so they keep their start when cut short. In a narrow row, w-fit
            // keeps a short line under the name, on the page's start side; max-w-full
            // stops a long one at the column's width so it is still cut short.
            <span
              title={secondary}
              dir="ltr"
              className="text-muted-foreground w-fit max-w-full min-w-0 truncate text-xs @2xl:max-w-max @2xl:flex-2"
            >
              <FolderPaths paths={paths} />
            </span>
          ) : hasDetail ? (
            <DetailLine library={library} text={secondary} className="@2xl:max-w-max @2xl:flex-2" />
          ) : null}
        </div>
        {item.kind === "template_collection" ? (
          <span className="text-muted-foreground hidden shrink-0 text-[11px] sm:inline">
            {t("web.admin.backups.restore.rows.starterPack")}
          </span>
        ) : null}
        <StateTag state={itemState(item)} />
        {hasDetails ? (
          <RowDisclosure
            open={open}
            onOpenChange={setOpen}
            controls={detailsId}
            label={
              open
                ? t("web.admin.backups.restore.rows.hideDetails", { label })
                : t("web.admin.backups.restore.rows.showDetails", { label })
            }
          />
        ) : (
          <span aria-hidden="true" className="size-7 shrink-0" />
        )}
      </div>
      {open ? (
        <div id={detailsId} className="space-y-2 ps-7 pb-3">
          {details}
        </div>
      ) : null}
    </li>
  );
});

/**
 * A row of the "match this server" list: no choice to make, so just the name.
 * The name wraps rather than being cut short, so names that differ only at the
 * end stay apart; the detail moves under it when the two don't fit on one line.
 * A name with a part that can't wrap is clipped at the row's edge only while it
 * overflows, as in ItemRow, so the scrolling list never scrolls sideways.
 */
export const SameRow = memo(function SameRow({
  item,
  secondary,
  library,
}: {
  item: RestoreItem;
  secondary: string | null;
  library: string | null;
}) {
  useT();
  const label = displayLabel(item);
  const [nameRef, nameOverflows] = useOverflowsInline<HTMLSpanElement>(label);
  return (
    <li className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 py-1 text-[13px]">
      {/* The name reads in its own direction, so a part that can't wrap is clipped
          at the name's end rather than its start; its lines line up with the page's. */}
      <span
        ref={nameRef}
        title={label}
        dir={nameDir(item)}
        className={cn(
          "align-with-page block min-w-0 text-pretty wrap-anywhere",
          nameOverflows && OVERFLOW_CLIP,
        )}
      >
        <BreakableKey value={label} />
      </span>
      {secondary || library ? <DetailLine library={library} text={secondary} /> : null}
    </li>
  );
});
