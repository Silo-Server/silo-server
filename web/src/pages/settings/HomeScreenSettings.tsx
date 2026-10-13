import type { ActionMenuItem } from "@/components/calm/ActionMenu";
import type { PageMoreMenuItem } from "@/components/calm/PageMoreMenu";
import { AddRowDialog } from "@/components/homeRows/addRow/AddRowDialog";
import { HideWatchedCard } from "@/components/homeRows/HideWatchedCard";
import { HomeRowsPage, type SharedRowMenuItems } from "@/components/homeRows/HomeRowsPage";
import { RemoveRowDialog } from "@/components/homeRows/RemoveRowDialog";
import { ResetProfileDialog } from "@/components/homeRows/ResetProfileDialog";
import { useNewRowHighlight } from "@/components/homeRows/useNewRowHighlight";
import { useRowFocus } from "@/components/homeRows/useRowFocus";
import { useRowDialog, useRowLinks } from "@/components/homeRows/useRowLinks";
import { HomeLayoutImportDialog } from "@/components/sections/HomeLayoutTransfer";
import { Button } from "@/components/ui/button";
import { useHomeLayoutExport } from "@/hooks/queries/homeRows/useHomeLayoutExport";
import { useProfileHomeRowsAdapter } from "@/hooks/queries/homeRows/useProfileHomeRowsAdapter";
import { useUserLibraries } from "@/hooks/queries/libraries";
import {
  useEffectiveSettings,
  useSetSettingValue,
  type SettingIdentity,
} from "@/hooks/queries/settingValues";
import { useOptionalAuth } from "@/hooks/useAuth";
import { collectionKind, profilePageName, type CollectionSummary } from "@/lib/homeRows/describe";
import { pageLabel } from "@/lib/homeRows/pages";
import type { HomeRow } from "@/lib/homeRows/types";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  CircleMinus,
  Download,
  Pencil,
  RotateCcw,
  Star,
  StarOff,
  Trash2,
  Undo2,
  Upload,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";

const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };
const HOME_PREFERENCE_KEYS = [SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS] as const;

/**
 * Settings > Home Screen: this profile's rows on Home and each library page,
 * on the same Home rows components as the admin page. Changes save as they
 * are made and only ever change this profile.
 */
/** Something the page needs didn't load, so it can't change until a retry. */
function LoadFailedNote({
  what,
  retryLabel,
  onRetry,
}: {
  what: string;
  retryLabel: string;
  onRetry: () => void;
}) {
  return (
    <div
      role="alert"
      className="border-warning/40 bg-warning/10 flex flex-wrap items-center gap-3 rounded-2xl border px-4 py-3 text-sm"
    >
      <p className="min-w-0 flex-1">
        {what} didn&apos;t load, so this page can&apos;t change right now.
      </p>
      <Button size="sm" variant="outline" onClick={onRetry}>
        {retryLabel}
      </Button>
    </div>
  );
}

export default function HomeScreenSettings() {
  const adapter = useProfileHomeRowsAdapter();
  const { data: libraries } = useUserLibraries();
  const focus = useRowFocus(adapter.rows, adapter.pending);
  const label = pageLabel(adapter.page, adapter.pages);
  const pageName = profilePageName(label);
  const layoutExport = useHomeLayoutExport();
  const [importOpen, setImportOpen] = useState(false);
  const [rowDialog, setRowDialog] = useRowDialog();
  const [highlightId, setHighlightId] = useNewRowHighlight();
  // A row added from a link, waiting for its save to land before going back.
  const landing = useRef<{ ids: string[]; onAdded: (newIds: string[]) => void } | null>(null);
  const [removing, setRemoving] = useState<HomeRow | null>(null);
  const [resetOpen, setResetOpen] = useState(false);
  // Set when a confirmed delete takes away the control that opened its dialog.
  const removedOpener = useRef(false);

  const homePreferences = useEffectiveSettings({ keys: HOME_PREFERENCE_KEYS });
  const saveHomePreference = useSetSettingValue();
  const hideWatchedItems =
    homePreferences.data?.[SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS]?.value === true;

  const profileId = useOptionalAuth()?.profile?.id;
  const collectionOptions = adapter.collections?.options;
  const collectionSummaries = useMemo(
    () =>
      new Map<string, CollectionSummary>(
        (collectionOptions ?? []).map((option) => [
          option.id,
          {
            title: option.title,
            kind: collectionKind(option.collection_type),
            // The list also holds collections other profiles shared.
            yours: Boolean(profileId) && option.creator_profile_id === profileId,
          },
        ]),
      ),
    [collectionOptions, profileId],
  );

  function openRow(row: HomeRow) {
    if (!adapter.canEdit) return;
    void adapter
      .openEdit(row.id)
      .then((session) => setRowDialog({ session }))
      .catch((error: unknown) =>
        toast.error(error instanceof Error ? error.message : "Could not open this row"),
      );
  }

  useRowLinks({
    adapter,
    catalog: adapter.catalog,
    catalogFailed: adapter.catalogFailed,
    // Its saved changes decide whether the page can change.
    settled: adapter.status === "ready" && !adapter.overridesLoading,
    onAdd: (seed) => setRowDialog({ session: null, seed }),
    onEdit: openRow,
  });

  // Saves are queued, so Add row closes before this one lands. It goes back once
  // the row is on the page, which may take a later read when the one after the
  // save fails. A failed save has already said so, and the page stays for
  // another try.
  const { pending, rows, lastWriteAt } = adapter;
  useEffect(() => {
    const added = landing.current;
    if (!added || pending) return;
    const [id] = added.ids;
    if (rows.some((row) => row.id === id)) {
      landing.current = null;
      added.onAdded(added.ids);
    } else if (!id || lastWriteAt(id) === undefined) {
      landing.current = null;
    }
  }, [pending, rows, lastWriteAt]);

  function confirmRemove() {
    if (!removing) return;
    removedOpener.current = true;
    focus.afterRemoval(removing.id);
    adapter.remove([removing.id]);
    setRemoving(null);
  }

  function skipReturnFocus() {
    const skip = removedOpener.current;
    removedOpener.current = false;
    return skip;
  }

  function rowMenuItems(row: HomeRow, shared: SharedRowMenuItems): ActionMenuItem[] {
    const busy = !adapter.canEdit;
    return [
      {
        key: "edit",
        label: "Edit row…",
        icon: Pencil,
        disabled: busy,
        onSelect: () => openRow(row),
      },
      ...(row.renamedFrom
        ? [
            {
              key: "original-name",
              label: "Use the original name",
              icon: Undo2,
              disabled: busy,
              onSelect: () => adapter.restoreOriginalName(row.id),
            },
          ]
        : []),
      {
        key: "hero",
        label: row.hero ? "Stop using as hero banner" : "Use as hero banner",
        icon: row.hero ? StarOff : Star,
        disabled: busy,
        onSelect: () => void adapter.setHero(row.id, !row.hero),
      },
      shared.moveToTop,
      shared.moveToBottom,
      {
        key: "remove",
        label: row.own ? "Delete row…" : `Remove from my ${pageName}…`,
        icon: row.own ? Trash2 : CircleMinus,
        destructive: true,
        group: true,
        disabled: busy,
        onSelect: () => setRemoving(row),
      },
    ];
  }

  const resetTarget = adapter.page.kind === "home" ? "Home" : `the ${label} page`;
  const moreItems: PageMoreMenuItem[] = [
    {
      key: "export",
      label: layoutExport.running ? "Exporting layout…" : "Export layout",
      help: "Save your rows for every page to a file.",
      icon: Download,
      disabled: !layoutExport.ready || layoutExport.running,
      onSelect: () => void layoutExport.run(),
    },
    {
      key: "import",
      label: "Import layout…",
      help: "Load a layout saved here or on another server.",
      icon: Upload,
      disabled: !layoutExport.libraries,
      opensDialog: true,
      onSelect: () => setImportOpen(true),
    },
    {
      key: "reset",
      label: `Reset ${resetTarget} to the server's rows…`,
      help: "Brings back hidden rows and original names, and removes rows you added.",
      icon: RotateCcw,
      group: true,
      disabled: !adapter.canEdit,
      opensDialog: true,
      onSelect: () => setResetOpen(true),
    },
  ];

  function handleHideWatchedItemsChange(enabled: boolean) {
    saveHomePreference.mutate(
      { key: SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS, value: enabled, identity: PROFILE_SCOPE },
      { onError: () => toast.error("Failed to save Home preference") },
    );
  }

  let notice = null;
  if (adapter.overridesFailed) {
    notice = (
      <LoadFailedNote
        what="Your saved changes"
        retryLabel="Reload rows"
        onRetry={() => void adapter.reload()}
      />
    );
  } else if (adapter.overridesLoading) {
    notice = (
      <p role="status" className="text-muted-foreground text-sm">
        Loading your saved changes…
      </p>
    );
  }

  const editingRow = rowDialog?.session?.row;
  return (
    <HomeRowsPage
      adapter={adapter}
      title="Home screen"
      subtitle="Choose the rows you see and their order. Only this profile changes, and it saves as you go."
      focus={focus}
      collection={(id) => collectionSummaries.get(id)}
      onOpenRow={adapter.canEdit ? openRow : undefined}
      highlightRowId={highlightId}
      rowMenuItems={rowMenuItems}
      moreItems={moreItems}
      addRow={{ onClick: () => setRowDialog({ session: null }), disabled: !adapter.canEdit }}
      notices={notice}
    >
      <HideWatchedCard
        checked={hideWatchedItems}
        disabled={homePreferences.isLoading || saveHomePreference.isPending}
        onCheckedChange={handleHideWatchedItemsChange}
      />

      <RemoveRowDialog
        row={removing}
        pageName={pageName}
        onConfirm={confirmRemove}
        onOpenChange={(open) => !open && setRemoving(null)}
        skipReturnFocus={skipReturnFocus}
      />
      <ResetProfileDialog
        open={resetOpen}
        pageName={pageName}
        ownTitles={adapter.rows.filter((row) => row.own).map((row) => row.title)}
        onConfirm={() => {
          setResetOpen(false);
          adapter.reset();
        }}
        onOpenChange={setResetOpen}
      />
      {importOpen && layoutExport.libraries ? (
        <HomeLayoutImportDialog
          libraries={layoutExport.libraries}
          onClose={() => setImportOpen(false)}
        />
      ) : null}
      {rowDialog ? (
        <AddRowDialog
          key={rowDialog.key}
          adapter={adapter}
          catalog={adapter.catalog}
          catalogFailed={adapter.catalogFailed}
          libraries={libraries ?? []}
          session={rowDialog.session}
          initialSeed={rowDialog.seed}
          onClose={() => setRowDialog(null)}
          onSaved={(newIds) => {
            const onAdded = rowDialog.seed?.onAdded;
            if (onAdded) landing.current = { ids: newIds, onAdded };
            else setHighlightId(newIds[0] ?? null);
          }}
          deleteLabel={editingRow && !editingRow.own ? `Remove from my ${pageName}…` : undefined}
          onDelete={(session) => {
            setRowDialog(null);
            setRemoving(session.row);
          }}
        />
      ) : null}
    </HomeRowsPage>
  );
}
