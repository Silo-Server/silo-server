import { Fragment, memo, useId, useState, type ReactNode } from "react";

import type { RestoreItem, RestoreSectionId, RestoreSetting } from "@/api/v2/adminBackupRestore";
import { RestartBadge } from "@/components/settings/RestartBadge";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
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
import { REASON_SENTENCES, displayLabel, toggleKeys } from "./restoreModel";

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
  if (setting.masked) {
    return { now: set, backup: t("web.admin.backups.restore.setting.differentEncrypted") };
  }
  return { now: current, backup };
}

/**
 * A setting key that may break after its dots and underscores, so a narrow
 * column wraps "playback.watched_threshold" between words, not inside one.
 */
function BreakableKey({ value }: { value: string }) {
  const parts = value.replace(/([._])/g, "$1\u0000").split("\u0000");
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
                    {setting.restart_required ? <RestartBadge /> : null}
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
  const changes = item.home_section?.changes ?? item.collection?.changes ?? [];
  const settings = (item.settings ?? []).some((setting) => setting.status !== "on_this_server");
  const paths = item.library && item.library.paths.length > 1 ? item.library.paths : [];
  const missingPaths = item.library?.missing_paths ?? [];
  const parts = [
    why ? (
      <SettingFieldStatus key="why" tone={item.selectable ? "muted" : "warn"}>
        {why}
      </SettingFieldStatus>
    ) : null,
    paths.length > 0 ? (
      <ul key="paths" className="text-muted-foreground font-mono text-xs break-all">
        {paths.map((path) => (
          <li key={path}>{path}</li>
        ))}
      </ul>
    ) : null,
    missingPaths.length > 0 ? (
      <p key="missing-paths" className="text-xs text-amber-600 dark:text-amber-400">
        {t("web.admin.backups.restore.rows.cantOpen", { paths: missingPaths.join(", ") })}
      </p>
    ) : null,
    item.missing_blobs.length > 0 ? (
      <SettingFieldStatus key="files" tone="warn">
        {t("web.admin.backups.restore.rows.filesMissing", {
          count: item.missing_blobs.length,
          count_text: item.missing_blobs.length,
        })}
      </SettingFieldStatus>
    ) : null,
    item.restart_required ? (
      <p key="restart" className="text-muted-foreground flex items-center gap-2 text-xs">
        <RestartBadge /> {t("web.admin.backups.restore.rows.restart")}
      </p>
    ) : null,
    changes.length > 0 ? (
      <div key="changes" className="text-xs">
        <p className="text-muted-foreground">{t("web.admin.backups.restore.rows.changes")}</p>
        <ul className="text-muted-foreground list-disc ps-5">
          {changes.map((change) => (
            <li key={change}>{change}</li>
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
}: {
  sectionId: RestoreSectionId;
  item: RestoreItem;
  checked: boolean;
  onToggle: ToggleKeys;
  secondary: string | null;
}) {
  const t = useT();
  const controlId = useId();
  const detailsId = useId();
  const [open, setOpen] = useState(false);
  const details = open ? itemDetails(t, item) : null;
  const hasDetails = hasItemDetails(item);
  const label = displayLabel(item);
  return (
    <li className="border-border/60 border-b last:border-b-0">
      <div className="flex min-h-10 items-center gap-3 py-1.5">
        <Checkbox
          id={controlId}
          checked={checked}
          disabled={!item.selectable}
          onCheckedChange={(value) => onToggle(toggleKeys(sectionId, item), value === true)}
        />
        <div className="flex min-w-0 flex-1 items-baseline gap-2">
          <Label
            htmlFor={controlId}
            title={label}
            className={cn(
              "max-w-[60%] shrink-0 truncate text-sm font-medium",
              !item.selectable && "text-muted-foreground",
            )}
          >
            {label}
          </Label>
          {secondary ? (
            <span title={secondary} className="text-muted-foreground min-w-0 truncate text-xs">
              {secondary}
            </span>
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

/** A row of the "match this server" list: no choice to make, so just the name. */
export const SameRow = memo(function SameRow({
  item,
  secondary,
}: {
  item: RestoreItem;
  secondary: string | null;
}) {
  useT();
  const label = displayLabel(item);
  return (
    <li className="flex min-w-0 items-baseline gap-2 py-1 text-[13px]">
      <span title={label} className="max-w-[60%] shrink-0 truncate">
        {label}
      </span>
      {secondary ? (
        <span title={secondary} className="text-muted-foreground min-w-0 truncate text-xs">
          {secondary}
        </span>
      ) : null}
    </li>
  );
});
