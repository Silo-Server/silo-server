import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Film, FolderPlus, ListPlus, Loader2, Plus, Search, Users } from "lucide-react";
import { Link, useNavigate } from "react-router";
import { toast } from "sonner";
import type { Collection } from "@/api/types";
import { PosterArt } from "@/components/calm/PosterTile";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  useAddItemToCollection,
  useCollectionsContaining,
  useRemoveCollectionItem,
} from "@/hooks/queries/collections";
import { putCollectionItem } from "@/hooks/queries/collectionScope";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import {
  ADD_TO_COLLECTION_FOOTNOTE,
  inCollections,
  madeButNotAdded,
  manualTitleCount,
} from "@/lib/collections/copy";
import { PERSONAL_SCOPE, type SavableDraft } from "@/lib/collections/scope";

interface AddToCollectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mediaItemId: string;
  /** The title being added, named in the dialog and its messages. */
  itemTitle?: string;
}

/** How long "Added" or "Removed" stays beside a collection after its tick saves. */
const FLASH_MS = 2000;

type Flash = { id: string; text: "Added" | "Removed" };

/**
 * Add to collection: a checkbox for each of the profile's own manual
 * collections (another profile's shared collection is read-only for it, and
 * smart and synced collections fill themselves). A tick saves at once, and the
 * initial ticks come from the list's `contains_item` marks. A New collection
 * row is always there, so the dialog never dead-ends.
 *
 * Acting admins get the same list: titles go into a server collection from
 * that collection's editor.
 */
export default function AddToCollectionDialog({
  open,
  onOpenChange,
  mediaItemId,
  itemTitle,
}: AddToCollectionDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="gap-4 sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle>Add to a collection</DialogTitle>
          <DialogDescription className="sr-only">
            Tick a collection to add {itemTitle ?? "this title"} to it. Ticking saves right away.
          </DialogDescription>
        </DialogHeader>
        {/* Mounted only while open and keyed by title, so each opening or title starts from the server's ticks. */}
        <AddToCollectionPanel
          key={mediaItemId}
          mediaItemId={mediaItemId}
          itemTitle={itemTitle ?? "this title"}
          onClose={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

function AddToCollectionPanel({
  mediaItemId,
  itemTitle,
  onClose,
}: {
  mediaItemId: string;
  itemTitle: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { profile } = useCurrentProfile();
  const profileId = profile?.id;
  const list = useCollectionsContaining(mediaItemId);
  const [query, setQuery] = useState("");
  // Ticks changed in this dialog, ahead of the list's next read.
  const [ticks, setTicks] = useState<ReadonlyMap<string, boolean>>(new Map());
  const [flash, setFlash] = useState<Flash | null>(null);

  useEffect(() => {
    if (!flash) return;
    const timer = setTimeout(() => setFlash(null), FLASH_MS);
    return () => clearTimeout(timer);
  }, [flash]);

  // Nothing is listed until the acting profile is known: ownership fails closed.
  const loading = list.isLoading || !profileId;
  const own = useMemo(
    () =>
      (list.data ?? []).filter(
        (collection) =>
          collection.collection_type === "manual" &&
          !PERSONAL_SCOPE.isReadOnly(PERSONAL_SCOPE.toView(collection), profileId),
      ),
    [list.data, profileId],
  );
  const isTicked = (collection: Collection) =>
    ticks.get(collection.id) ?? collection.contains ?? false;
  const needle = query.trim().toLowerCase();
  const shown = own.filter((collection) => collection.name.toLowerCase().includes(needle));
  const tickedCount = own.filter(isTicked).length;

  function setTick(id: string, ticked: boolean) {
    setTicks((current) => new Map(current).set(id, ticked));
  }

  function changed(id: string, ticked: boolean) {
    setTick(id, ticked);
    setFlash({ id, text: ticked ? "Added" : "Removed" });
  }

  /** PUTs the title into a collection made here; on failure the collection is kept. */
  async function addToNew(id: string, name: string) {
    try {
      await putCollectionItem(PERSONAL_SCOPE, id, mediaItemId, 0);
      changed(id, true);
    } catch {
      toast.warning(madeButNotAdded(name, itemTitle), {
        action: { label: "Try again", onClick: () => void addToNew(id, name) },
        cancel: {
          label: "Open it",
          onClick: () => void navigate(PERSONAL_SCOPE.paths.edit(id, { focus: "titles" })),
        },
      });
    } finally {
      void PERSONAL_SCOPE.invalidate(queryClient, id);
    }
  }

  /** Makes a personal manual collection named `name`, then adds the title. False when nothing was made. */
  async function createAndAdd(name: string): Promise<boolean> {
    const draft: SavableDraft = {
      ...PERSONAL_SCOPE.toDraft(null, { kind: "manual" }),
      kind: "manual",
      name,
    };
    let id: string;
    try {
      ({ id } = await PERSONAL_SCOPE.create(draft));
    } catch (error) {
      toast.error(PERSONAL_SCOPE.errorMessage(error, "Couldn't make the collection"));
      return false;
    }
    setQuery("");
    await addToNew(id, name);
    return true;
  }

  if (loading) {
    return (
      <>
        <ItemChip title={itemTitle} />
        <p className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
          <Loader2 aria-hidden className="size-4 animate-spin" />
          Loading collections…
        </p>
      </>
    );
  }

  if (list.isError && !list.data) {
    return (
      <>
        <ItemChip title={itemTitle} />
        <div role="alert" className="grid justify-items-center gap-3 py-8 text-center">
          <p className="font-semibold">Couldn't load your collections</p>
          <Button variant="outline" onClick={() => void list.refetch()}>
            Try again
          </Button>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </DialogFooter>
      </>
    );
  }

  if (own.length === 0) {
    return (
      <>
        <ItemChip title={itemTitle} />
        <div className="border-border grid justify-items-center gap-3 rounded-2xl border border-dashed px-5 py-6 text-center">
          <span className="bg-info/10 text-info ring-info/30 grid size-11 place-items-center rounded-xl ring-1 ring-inset">
            <ListPlus aria-hidden className="size-5" />
          </span>
          <div className="grid gap-1">
            <p className="font-semibold">Start your first collection</p>
            <p className="text-muted-foreground text-sm">
              Name it and {itemTitle} goes straight in. You can add more titles and change the order
              later.
            </p>
          </div>
          <NewCollectionForm onCreate={createAndAdd} className="w-full" />
        </div>
        <p className="text-muted-foreground text-sm">
          Want one that fills itself?{" "}
          <Link
            to={PERSONAL_SCOPE.paths.create({ type: "smart" })}
            onClick={onClose}
            className="text-foreground underline underline-offset-2"
          >
            New smart collection
          </Link>
        </p>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </DialogFooter>
      </>
    );
  }

  return (
    <>
      <ItemChip title={itemTitle} />
      <div className="relative">
        <Search
          aria-hidden
          className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2"
        />
        <Input
          type="search"
          aria-label="Find one of your collections"
          placeholder="Find one of your collections"
          className="h-10 pl-9"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </div>
      <div className="border-border overflow-hidden rounded-2xl border">
        {shown.length > 0 ? (
          <ul className="max-h-[280px] overflow-y-auto p-1.5">
            {shown.map((collection) => (
              <CollectionChoice
                key={collection.id}
                collection={collection}
                mediaItemId={mediaItemId}
                ticked={isTicked(collection)}
                flash={flash?.id === collection.id ? flash.text : null}
                onTick={setTick}
                onSaved={changed}
              />
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground px-4 py-6 text-sm">
            No collections match "{query.trim()}".
          </p>
        )}
        <div className="border-border grid grid-cols-[36px_minmax(0,1fr)] items-center gap-3 border-t px-4 py-3">
          <span
            aria-hidden
            className="border-border text-muted-foreground grid aspect-[2/3] w-9 place-items-center rounded-md border border-dashed"
          >
            <Plus className="size-4" />
          </span>
          <NewCollectionForm onCreate={createAndAdd} labelled />
        </div>
      </div>
      <p className="text-muted-foreground text-[13px]">{ADD_TO_COLLECTION_FOOTNOTE}</p>
      <DialogFooter className="items-center sm:justify-between">
        <p className="text-muted-foreground text-sm">{inCollections(tickedCount)}</p>
        <Button variant="outline" onClick={onClose}>
          Done
        </Button>
      </DialogFooter>
    </>
  );
}

/** The title being added, so the dialog says what goes in. */
function ItemChip({ title }: { title: string }) {
  return (
    <div className="bg-muted/40 flex items-center gap-3 rounded-xl p-3">
      <span
        aria-hidden
        className="bg-muted text-muted-foreground grid aspect-[2/3] w-8 place-items-center rounded-md"
      >
        <Film className="size-4" />
      </span>
      <span className="min-w-0 truncate text-sm font-semibold">{title}</span>
    </div>
  );
}

function CollectionChoice({
  collection,
  mediaItemId,
  ticked,
  flash,
  onTick,
  onSaved,
}: {
  collection: Collection;
  mediaItemId: string;
  ticked: boolean;
  flash: Flash["text"] | null;
  /** Shows a tick ahead of the save, or puts it back when the save fails. */
  onTick: (id: string, ticked: boolean) => void;
  onSaved: (id: string, ticked: boolean) => void;
}) {
  const boxId = useId();
  const metaId = useId();
  const add = useAddItemToCollection();
  const remove = useRemoveCollectionItem(collection.id);
  const saving = add.isPending || remove.isPending;

  async function toggle(next: boolean) {
    // The box stays focusable while it saves, so a press during the save is ignored here.
    if (saving) return;
    onTick(collection.id, next);
    // Settled on the promise, not `mutate`'s callbacks: a search can unmount
    // this row before the save lands, and those callbacks then never run, so
    // a failed save would keep its tick. The hooks report the failure.
    try {
      if (next) await add.mutateAsync({ collectionId: collection.id, mediaItemId });
      else await remove.mutateAsync(mediaItemId);
      onSaved(collection.id, next);
    } catch {
      onTick(collection.id, !next);
    }
  }

  return (
    <li>
      <label
        htmlFor={boxId}
        className="hover:bg-accent/60 has-[[data-state=checked]]:bg-accent/40 grid cursor-pointer grid-cols-[18px_36px_minmax(0,1fr)_auto] items-center gap-3 rounded-xl px-3 py-2"
      >
        <Checkbox
          id={boxId}
          checked={ticked}
          aria-disabled={saving}
          aria-label={collection.name}
          aria-describedby={metaId}
          onCheckedChange={(checked) => void toggle(checked === true)}
          className="size-[18px] rounded-[5px] aria-disabled:opacity-50"
        />
        {collection.poster_url || collection.poster_thumbhash ? (
          <PosterArt
            posterUrl={collection.poster_url || undefined}
            thumbhash={collection.poster_thumbhash}
            className="ring-border/60 aspect-[2/3] w-9 rounded-md ring-1 ring-inset"
          />
        ) : (
          <span
            aria-hidden
            className="bg-muted text-muted-foreground grid aspect-[2/3] w-9 place-items-center rounded-md"
          >
            <FolderPlus className="size-4" />
          </span>
        )}
        <span className="min-w-0">
          <span className="block truncate text-sm font-semibold">{collection.name}</span>
          <span id={metaId} className="text-muted-foreground flex items-center gap-2 text-[12.5px]">
            <span className="truncate">{manualTitleCount(collection.item_count ?? 0)}</span>
            {collection.is_shared ? (
              <span className="bg-info/10 text-info ring-info/30 inline-flex h-5 shrink-0 items-center gap-1 rounded-full px-2 text-[11px] font-semibold ring-1 ring-inset">
                <Users aria-hidden className="size-3" />
                Shared
              </span>
            ) : null}
          </span>
        </span>
        <span role="status" className="text-success min-w-12 text-right text-xs font-semibold">
          {flash}
          {!flash && saving ? (
            <Loader2 aria-hidden className="ml-auto size-3.5 animate-spin" />
          ) : null}
        </span>
      </label>
    </li>
  );
}

/** A name box and "Create and add": makes a manual collection with the title in it. */
function NewCollectionForm({
  onCreate,
  labelled = false,
  className,
}: {
  onCreate: (name: string) => Promise<boolean>;
  /** Show the "New manual collection" label above the box; otherwise it is read out only. */
  labelled?: boolean;
  className?: string;
}) {
  const inputId = useId();
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const trimmed = name.trim();

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!trimmed || creating) return;
    setCreating(true);
    try {
      if (await onCreate(trimmed)) setName("");
    } finally {
      setCreating(false);
    }
  }

  return (
    <form onSubmit={(event) => void submit(event)} className={className}>
      <label
        htmlFor={inputId}
        className={labelled ? "mb-1.5 block text-[13px] font-medium" : "sr-only"}
      >
        New manual collection
      </label>
      <div className="flex gap-2">
        <Input
          id={inputId}
          value={name}
          placeholder="e.g. Ghibli Night"
          autoComplete="off"
          className="h-10 min-w-0 flex-1"
          onChange={(event) => setName(event.target.value)}
        />
        <Button type="submit" className="h-10 gap-2" disabled={!trimmed || creating}>
          {creating ? <Loader2 aria-hidden className="size-4 animate-spin" /> : null}
          Create and add
        </Button>
      </div>
    </form>
  );
}
