import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  alsoDeletedElsewhere,
  deleteCollectionsDescription,
  deleteCollectionsTitle,
  keptForRows,
} from "@/lib/collections/copy";
import type { CollectionKind } from "@/lib/collections/types";

/**
 * Confirms deleting several server collections at once (Delete all in this
 * view, or Delete… in select mode). It counts only what will be deleted,
 * names collections that also leave other libraries, and names the ones it
 * keeps because Home or library page rows use them.
 */
export function DeleteCollectionsDialog({
  open,
  onOpenChange,
  count,
  kind,
  where,
  elsewhere,
  kept,
  blocked,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** How many will be deleted. */
  count: number;
  /** Their type when they share one; null for a mix. */
  kind: CollectionKind | null;
  /** A library name or "this view"; null when the delete covers the selection only. */
  where: string | null;
  /** Collections being deleted that are also in other libraries, with those libraries. */
  elsewhere: ReadonlyArray<{ title: string; libraryNames: readonly string[] }>;
  /** Titles of the collections left alone because rows use them. */
  kept: readonly string[];
  /** Why the delete can't run right now; the confirm stays open and off until it can. */
  blocked: string | null;
  onConfirm: () => void;
}) {
  const items = [
    ...(elsewhere.length > 0 ? [alsoDeletedElsewhere(elsewhere, kind)] : []),
    ...(kept.length > 0 ? [keptForRows(kept)] : []),
  ];
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={deleteCollectionsTitle(count, kind, where)}
      description={deleteCollectionsDescription(count)}
      bullets={items.length > 0 ? { label: "Also", items } : undefined}
      irreversible
      error={blocked}
      confirmDisabled={blocked !== null}
      confirmLabel={`Delete ${count}`}
      variant="destructive"
      onConfirm={onConfirm}
    />
  );
}
