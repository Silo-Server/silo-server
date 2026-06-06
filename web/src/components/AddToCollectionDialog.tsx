import { useMemo, useState } from "react";
import { FolderPlus, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useCollections, useAddItemToCollection } from "@/hooks/queries/collections";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useQueries } from "@tanstack/react-query";
import { api } from "@/api/client";
import { libraryCollectionKeys } from "@/hooks/queries/keys";
import { useOptionalAuth } from "@/hooks/useAuth";
import type { LibraryCollection } from "@/api/types";

interface AddToCollectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mediaItemId: string;
  /** Optional label shown so the user knows what they're adding. */
  itemTitle?: string;
}

interface CollectionPick {
  id: string;
  title: string;
  source: "user" | "library";
  group: string;
}

/**
 * Add-to-collection picker. Lists the caller's manual user collections
 * plus — for admins — every manual library collection grouped by library.
 * Synced/smart collections are excluded since they overwrite manual edits
 * on the next sync. Selecting a row commits the add via the existing
 * PUT collections/{id}/items/{itemId} endpoint (or the /admin variant).
 */
export default function AddToCollectionDialog({
  open,
  onOpenChange,
  mediaItemId,
  itemTitle,
}: AddToCollectionDialogProps) {
  const auth = useOptionalAuth();
  const isAdmin = auth?.user?.role === "admin";
  const { data: userCollections, isLoading: userLoading } = useCollections();
  const { data: libraries } = useUserLibraries();
  const addItem = useAddItemToCollection();
  const [selectedId, setSelectedId] = useState<string | null>(null);

  // Admins fetch each library's collections so we can offer adding to admin
  // manual collections too. Non-admins skip these queries entirely.
  const libraryQueries = useQueries({
    queries: (isAdmin ? (libraries ?? []) : []).map((lib) => ({
      queryKey: libraryCollectionKeys.list(lib.id),
      queryFn: () =>
        api<{ collections: LibraryCollection[] }>(`/library/${lib.id}/collections`).then(
          (data) => data.collections ?? [],
        ),
      enabled: Number.isFinite(lib.id) && lib.id > 0,
    })),
  });
  const libraryLoading = isAdmin && libraryQueries.some((q) => q.isLoading);

  const picks = useMemo<CollectionPick[]>(() => {
    const out: CollectionPick[] = [];
    for (const c of userCollections ?? []) {
      if (c.collection_type === "manual") {
        out.push({ id: c.id, title: c.name, source: "user", group: "My Collections" });
      }
    }
    if (isAdmin && libraries) {
      for (let i = 0; i < libraries.length; i++) {
        const lib = libraries[i]!;
        const res = libraryQueries[i];
        if (res?.data) {
          for (const c of res.data) {
            if (c.collection_type === "manual") {
              out.push({ id: c.id, title: c.title, source: "library", group: lib.name });
            }
          }
        }
      }
    }
    return out;
  }, [userCollections, libraries, libraryQueries, isAdmin]);

  const groups = useMemo(() => {
    const m = new Map<string, CollectionPick[]>();
    for (const p of picks) {
      if (!m.has(p.group)) m.set(p.group, []);
      m.get(p.group)!.push(p);
    }
    return Array.from(m.entries());
  }, [picks]);

  const isLoading = userLoading || libraryLoading;

  function handleConfirm() {
    if (!selectedId) return;
    const pick = picks.find((p) => p.id === selectedId);
    if (!pick) return;
    addItem.mutate(
      { collectionId: pick.id, mediaItemId, source: pick.source },
      {
        onSuccess: () => {
          setSelectedId(null);
          onOpenChange(false);
        },
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add to Collection</DialogTitle>
          <DialogDescription>
            {itemTitle
              ? `Pick a manual collection to add "${itemTitle}" to.`
              : "Pick a manual collection."}
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-80 overflow-y-auto rounded-md border">
          {isLoading ? (
            <div className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading collections…
            </div>
          ) : groups.length === 0 ? (
            <div className="text-muted-foreground px-4 py-10 text-center text-sm">
              You don&apos;t have any manual collections yet. Create one in{" "}
              <span className="text-foreground">Collections</span> first.
            </div>
          ) : (
            <ul className="divide-border divide-y">
              {groups.map(([group, list]) => (
                <li key={group}>
                  <div className="bg-muted/50 text-muted-foreground px-3 py-1.5 text-[11px] font-semibold tracking-[0.12em] uppercase">
                    {group}
                  </div>
                  {list.map((p) => {
                    const isSelected = p.id === selectedId;
                    return (
                      <button
                        key={p.id}
                        type="button"
                        onClick={() => setSelectedId(p.id)}
                        data-selected={isSelected ? "true" : undefined}
                        className={`hover:bg-muted/50 flex w-full items-center gap-3 px-3 py-2.5 text-left text-sm transition-colors ${
                          isSelected ? "bg-muted/60" : ""
                        }`}
                      >
                        <FolderPlus className="text-muted-foreground h-4 w-4 shrink-0" />
                        <span className="flex-1 truncate">{p.title}</span>
                        {p.source === "library" && (
                          <span className="text-muted-foreground text-[10px] tracking-[0.1em] uppercase">
                            Library
                          </span>
                        )}
                      </button>
                    );
                  })}
                </li>
              ))}
            </ul>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={addItem.isPending}>
            Cancel
          </Button>
          <Button
            onClick={handleConfirm}
            disabled={!selectedId || addItem.isPending}
            className="gap-2"
          >
            {addItem.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Add
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
