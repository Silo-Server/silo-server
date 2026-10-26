import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { House, Library as LibraryIcon, Lock, MoreHorizontal, Pencil } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useAdminCollectionCapabilities } from "@/hooks/queries/admin/collections";
import { useScopeDelete, useScopeSync } from "@/hooks/queries/collectionScope";
import { useCollectionCapabilities } from "@/hooks/queries/collections";
import { useAvailableUserLibraries, useUserLibraries } from "@/hooks/queries/libraries";
import type { CollectionPageAccess } from "@/hooks/useCollectionPageAccess";
import {
  ADD_TO_MY_HOME,
  ADD_TO_MY_LIBRARY_PAGE,
  libraryPageLabel,
  personalDeleteDescription,
  serverDeleteDescription,
} from "@/lib/collections/copy";
import { addToMyHomePath, matchedLibraries, rowPages } from "@/lib/collections/rows";
import { PERSONAL_SCOPE, type CollectionScope, type EditorSnapshot } from "@/lib/collections/scope";
import type { RowLinkCollection } from "@/lib/homeRows/rowLinks";
import { buildLibraryCollectionCatalogHref } from "@/pages/catalogSearchParams";

/** "by *Name* · Read-only" under the title of another profile's shared collection. */
export function CollectionByline({ access }: { access: CollectionPageAccess }) {
  if (access.kind !== "read-only" || !access.ownerName) return null;
  return (
    <p className="text-muted-foreground text-sm">
      by <span className="text-foreground font-medium">{access.ownerName}</span> ·{" "}
      <span>Read-only</span>
    </p>
  );
}

/** Shown when an editor link sent someone who can't change the collection to its page instead. */
export function ReadOnlyCollectionCallout({
  access,
  collectionName,
}: {
  access: CollectionPageAccess;
  collectionName: string;
}) {
  if (access.kind !== "read-only") return null;
  return (
    <div
      role="note"
      className="surface-panel text-muted-foreground flex items-start gap-3 rounded-2xl border-0 px-4 py-3 text-sm"
    >
      <Lock className="mt-0.5 size-4 shrink-0" aria-hidden />
      <p>
        Only {access.ownerName ?? "admins"} can change{" "}
        <strong className="text-foreground font-semibold">{collectionName}</strong>, so you're on
        its page instead. You can still watch it and add it to your Home.
      </p>
    </div>
  );
}

/**
 * Top-right on a collection page: Edit + ⋯ for viewers who may change the
 * collection, Add to my Home + ⋯ for everyone else.
 */
export function CollectionPageActions({
  access,
  libraryId,
}: {
  access: CollectionPageAccess;
  libraryId?: number;
}) {
  if (access.kind === "read-only") {
    return <AddToMyHomeActions collection={access.collection} libraryIds={access.libraryIds} />;
  }
  if (access.kind !== "manage") return null;
  return (
    <ManageActions
      scope={access.scope}
      id={access.id}
      snapshot={access.snapshot}
      libraryId={libraryId}
    />
  );
}

function ManageActions({
  scope,
  id,
  snapshot,
  libraryId,
}: {
  scope: CollectionScope;
  id: string;
  snapshot?: EditorSnapshot;
  libraryId?: number;
}) {
  const navigate = useNavigate();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const { data: libraries } = useAvailableUserLibraries();
  const sync = useScopeSync(scope);
  const view = snapshot?.view;
  const isServer = scope.kind === "server";
  // Sync answers 501 when the scope's storage can't import, so it needs the capability.
  const { data: personalCapabilities } = useCollectionCapabilities(!isServer);
  const { data: serverCapabilities } = useAdminCollectionCapabilities(isServer);
  const canImport = (isServer ? serverCapabilities : personalCapabilities)?.imports === true;
  // After a delete, back to where the collection was browsed from.
  const browseLibraryId = libraryId ?? view?.libraryIds[0];
  const afterDeletePath =
    isServer && browseLibraryId
      ? `/library/${browseLibraryId}?tab=collections`
      : PERSONAL_SCOPE.paths.list();
  const remove = useScopeDelete(scope, {
    onDeleted: () => navigate(afterDeletePath, { replace: true }),
  });

  const namedLibraries = isServer
    ? (view?.libraryIds ?? []).flatMap((libraryID) => {
        const library = libraries?.find((entry) => entry.id === libraryID);
        return library ? [library] : [];
      })
    : [];
  const openIn = namedLibraries.length > 1 ? namedLibraries : [];
  const canSync = canImport && view?.kind === "synced";
  const deleteDescription = isServer
    ? serverDeleteDescription(namedLibraries.map((library) => library.name))
    : personalDeleteDescription(view?.personal?.shared ?? false);

  return (
    <div className="flex items-center gap-2">
      <Button asChild variant="outline" size="sm">
        <Link to={scope.paths.edit(id, { libraryId })}>
          <Pencil aria-hidden />
          Edit
        </Link>
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button type="button" variant="outline" size="icon-sm" aria-label="More actions">
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-48">
          {openIn.length > 0 ? (
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>Open in</DropdownMenuSubTrigger>
              <DropdownMenuSubContent>
                {openIn.map((library) => (
                  <DropdownMenuItem key={library.id} asChild>
                    <Link to={buildLibraryCollectionCatalogHref(id, view?.name, library.id)}>
                      {library.name}
                    </Link>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          ) : null}
          {canSync ? (
            <DropdownMenuItem disabled={sync.isPending} onSelect={() => sync.mutate(id)}>
              {sync.isPending ? "Syncing…" : "Sync now"}
            </DropdownMenuItem>
          ) : null}
          {openIn.length > 0 || canSync ? <DropdownMenuSeparator /> : null}
          <DropdownMenuItem
            variant="destructive"
            disabled={!snapshot || remove.isPending}
            onSelect={() => setConfirmOpen(true)}
          >
            Delete…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {snapshot ? (
        <ConfirmDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          title={`Delete "${snapshot.view.name}"?`}
          description={deleteDescription}
          confirmLabel="Delete"
          variant="destructive"
          isPending={remove.isPending}
          onConfirm={() => remove.mutate({ id, etag: snapshot.etag })}
        />
      ) : null}
    </div>
  );
}

/**
 * Add to my Home, and in ⋯ the viewer's own library pages: a shared personal
 * collection offers the libraries it matches, a server collection the library
 * it was opened in first, then the others. Each opens Settings > Home Screen
 * with Add row on the collection; nothing changes until that row is added.
 */
function AddToMyHomeActions({
  collection,
  libraryIds,
}: {
  collection: RowLinkCollection;
  libraryIds: readonly number[];
}) {
  const userLibraries = useUserLibraries();
  // Until the display preferences load, the list still holds hidden libraries.
  const libraries = (userLibraries.isLoading ? undefined : userLibraries.data) ?? [];
  let pages: ReadonlyArray<{ id: number; name: string }>;
  if (collection.source === "user") pages = matchedLibraries(libraries, libraryIds);
  else {
    const { bound, others } = rowPages(matchedLibraries(libraries, libraryIds), libraries);
    pages = [...bound, ...others];
  }
  const pageLink = (library: { id: number; name: string }) =>
    addToMyHomePath(collection, { kind: "library", libraryId: library.id });

  return (
    <div className="flex items-center gap-2">
      <Button asChild variant="outline" size="sm">
        <Link to={addToMyHomePath(collection, { kind: "home" })}>
          <House aria-hidden />
          {ADD_TO_MY_HOME}
        </Link>
      </Button>
      {pages.length > 0 ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button type="button" variant="outline" size="icon-sm" aria-label="More actions">
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-48">
            {pages.length === 1 ? (
              <DropdownMenuItem asChild>
                <Link to={pageLink(pages[0]!)}>
                  <LibraryIcon aria-hidden />
                  {`Add to my ${libraryPageLabel(pages[0]!.name)}`}
                </Link>
              </DropdownMenuItem>
            ) : (
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <LibraryIcon aria-hidden />
                  {ADD_TO_MY_LIBRARY_PAGE}
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent>
                  {pages.map((library) => (
                    <DropdownMenuItem key={library.id} asChild>
                      <Link to={pageLink(library)}>{libraryPageLabel(library.name)}</Link>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      ) : null}
    </div>
  );
}
