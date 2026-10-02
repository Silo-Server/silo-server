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
import {
  getLibraryCollectionList,
  libraryCollectionsQueryOptions,
} from "@/hooks/queries/libraryCollections";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";

interface AddToCollectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mediaItemId: string;
  /** Optional label shown so the user knows what they're adding. */
  itemTitle?: string;
  /**
   * The item's whole series, offered alongside mediaItemId when that is a
   * season. Library collections hold catalog items only, so they can take
   * the series but not the season.
   */
  seriesOption?: { id: string; title: string };
}

interface CollectionPick {
  id: string;
  title: string;
  source: "user" | "library";
  group: string;
  disabled?: boolean;
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
  seriesOption,
}: AddToCollectionDialogProps) {
  const isAdmin = useIsActingAdmin();
  const { data: userCollections, isLoading: userLoading } = useCollections();
  const { data: libraries } = useUserLibraries();
  const addItem = useAddItemToCollection();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [target, setTarget] = useState<"item" | "series">("item");
  const addingSeries = Boolean(seriesOption) && target === "series";
  const addingSeason = Boolean(seriesOption) && !addingSeries;
  const targetId = addingSeries && seriesOption ? seriesOption.id : mediaItemId;
  const targetTitle = addingSeries && seriesOption ? seriesOption.title : itemTitle;

  // Admins fetch each library's collections so we can offer adding to admin
  // manual collections too. Non-admins skip these queries entirely.
  const libraryQueries = useQueries({
    queries: (isAdmin ? (libraries ?? []) : []).map((lib) => ({
      ...libraryCollectionsQueryOptions(lib.id),
      select: getLibraryCollectionList,
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
        const collections = Array.isArray(res?.data) ? res.data : [];
        for (const c of collections) {
          if (c.collection_type === "manual") {
            out.push({
              id: c.id,
              title: c.title,
              source: "library",
              group: lib.name,
              disabled: addingSeason,
            });
          }
        }
      }
    }
    return out;
  }, [userCollections, libraries, libraryQueries, isAdmin, addingSeason]);

  const groups = useMemo(() => {
    const m = new Map<string, CollectionPick[]>();
    for (const p of picks) {
      if (!m.has(p.group)) m.set(p.group, []);
      m.get(p.group)!.push(p);
    }
    return Array.from(m.entries());
  }, [picks]);

  const isLoading = userLoading || libraryLoading;

  function chooseTarget(next: "item" | "series") {
    setTarget(next);
    if (next === "item" && picks.find((p) => p.id === selectedId)?.source === "library") {
      setSelectedId(null);
    }
  }

  function handleConfirm() {
    if (!selectedId) return;
    const pick = picks.find((p) => p.id === selectedId);
    if (!pick || pick.disabled) return;
    addItem.mutate(
      { collectionId: pick.id, mediaItemId: targetId, source: pick.source },
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
            {targetTitle
              ? `Pick a manual collection to add "${targetTitle}" to.`
              : "Pick a manual collection."}
          </DialogDescription>
        </DialogHeader>

        {seriesOption && (
          <div role="group" aria-label="What to add" className="grid grid-cols-2 gap-2">
            <Button
              type="button"
              variant={addingSeries ? "outline" : "secondary"}
              aria-pressed={!addingSeries}
              className="h-auto flex-col items-start gap-0.5 py-2"
              onClick={() => chooseTarget("item")}
            >
              <span>This season</span>
              {itemTitle && (
                <span className="text-muted-foreground max-w-full truncate text-xs">
                  {itemTitle}
                </span>
              )}
            </Button>
            <Button
              type="button"
              variant={addingSeries ? "secondary" : "outline"}
              aria-pressed={addingSeries}
              className="h-auto flex-col items-start gap-0.5 py-2"
              onClick={() => chooseTarget("series")}
            >
              <span>Whole series</span>
              <span className="text-muted-foreground max-w-full truncate text-xs">
                {seriesOption.title}
              </span>
            </Button>
          </div>
        )}

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
                  {addingSeason && list.some((p) => p.source === "library") && (
                    <p className="text-muted-foreground px-3 py-1 text-xs">
                      Library collections hold whole series
                    </p>
                  )}
                  {list.map((p) => {
                    const isSelected = p.id === selectedId;
                    return (
                      <button
                        key={p.id}
                        type="button"
                        onClick={() => setSelectedId(p.id)}
                        disabled={p.disabled}
                        data-selected={isSelected ? "true" : undefined}
                        className={`hover:bg-muted/50 flex w-full items-center gap-3 px-3 py-2.5 text-left text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
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
