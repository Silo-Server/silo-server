import { useState, type ReactNode } from "react";
import type { Collection } from "@/api/types";
import AllowCollectionDialog from "@/components/profiles/AllowCollectionDialog";
import { useCanManageTitleAllowances } from "@/hooks/queries/titleAllowances";

/**
 * The "Allow for…" action for the profile's own collections: `openFor` is
 * undefined when the profile cannot manage allowances (so the menu leaves the
 * action out), and `dialog` renders the dialog for the last collection
 * chosen. A closing dialog keeps its collection while it animates out.
 */
export function useAllowCollectionDialog(enabled: boolean): {
  openFor?: (collection: Collection) => void;
  dialog: ReactNode;
} {
  const canAllow = useCanManageTitleAllowances() && enabled;
  const [state, setState] = useState<{ open: boolean; collection?: Collection }>({ open: false });
  const dialog = state.collection ? (
    <AllowCollectionDialog
      open={state.open}
      onOpenChange={(open) => setState((current) => ({ ...current, open }))}
      collectionId={state.collection.id}
      collectionName={state.collection.name}
      live={state.collection.collection_type === "manual"}
    />
  ) : null;
  return {
    openFor: canAllow ? (collection) => setState({ open: true, collection }) : undefined,
    dialog,
  };
}
