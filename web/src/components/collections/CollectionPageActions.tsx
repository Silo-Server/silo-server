import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { House, Library as LibraryIcon, Lock, MoreHorizontal, Pencil } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
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
  addToMyHome,
  addToMyLibraryPage,
  libraryPageLabel,
  personalDeleteDescription,
  serverDeleteDescription,
  syncNow,
} from "@/lib/collections/copy";
import { addToMyHomePath, matchedLibraries, rowPages } from "@/lib/collections/rows";
import { PERSONAL_SCOPE, type CollectionScope, type EditorSnapshot } from "@/lib/collections/scope";
import type { RowLinkCollection } from "@/lib/homeRows/rowLinks";
import { cn } from "@/lib/utils";
import { buildLibraryCollectionCatalogHref } from "@/pages/catalogSearchParams";
import { useT } from "@/i18n";

/** The collection hero's glass pills, the homepage hero's secondary button. */
const HERO_PILL =
  "pill pill-glass outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4";
const HERO_ICON_PILL = cn(HERO_PILL, "size-11 p-0");

/** "by *Name* · Read-only" under the title of another profile's shared collection. */
export function CollectionByline({ access }: { access: CollectionPageAccess }) {
  const t = useT();
  if (access.kind !== "read-only" || !access.ownerName) return null;
  return (
    <p className="text-muted-foreground text-sm">
      {t("web.collections.pageActions.byline", {
        owner: access.ownerName,
        name: (chunks) => <span className="text-foreground font-medium">{chunks}</span>,
        readOnly: (chunks) => <span>{chunks}</span>,
      })}
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
  const t = useT();
  if (access.kind !== "read-only") return null;
  const name = (chunks: ReactNode) => (
    <strong className="text-foreground font-semibold">{chunks}</strong>
  );
  return (
    <div
      role="note"
      className="surface-panel text-muted-foreground flex items-start gap-3 rounded-2xl border-0 px-4 py-3 text-sm"
    >
      <Lock className="mt-0.5 size-4 shrink-0" aria-hidden />
      <p>
        {access.ownerName
          ? t("web.collections.pageActions.readOnlyOwner", {
              owner: access.ownerName,
              collection: collectionName,
              name,
            })
          : t("web.collections.pageActions.readOnlyAdmins", { collection: collectionName, name })}
      </p>
    </div>
  );
}

/**
 * Beside Shuffle in a collection page's hero: Edit + ⋯ for viewers who may change the
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
  const t = useT();
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
    <div className="flex items-center gap-3">
      <Link to={scope.paths.edit(id, { libraryId })} className={HERO_PILL}>
        <Pencil aria-hidden />
        {t("web.collections.pageActions.edit")}
      </Link>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className={HERO_ICON_PILL}
            aria-label={t("web.collections.actions.moreActions")}
          >
            <MoreHorizontal />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-48">
          {openIn.length > 0 ? (
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>{t("web.collections.actions.openIn")}</DropdownMenuSubTrigger>
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
              {sync.isPending ? t("web.collections.actions.syncing") : syncNow()}
            </DropdownMenuItem>
          ) : null}
          {openIn.length > 0 || canSync ? <DropdownMenuSeparator /> : null}
          <DropdownMenuItem
            variant="destructive"
            disabled={!snapshot || remove.isPending}
            onSelect={() => setConfirmOpen(true)}
          >
            {t("web.collections.actions.delete")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {snapshot ? (
        <ConfirmDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          title={t("web.collections.pageActions.deleteTitle", { name: snapshot.view.name })}
          description={deleteDescription}
          confirmLabel={t("web.collections.deleteDialog.delete")}
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
  const t = useT();
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
    <div className="flex items-center gap-3">
      <Link to={addToMyHomePath(collection, { kind: "home" })} className={HERO_PILL}>
        <House aria-hidden />
        {addToMyHome()}
      </Link>
      {pages.length > 0 ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              className={HERO_ICON_PILL}
              aria-label={t("web.collections.actions.moreActions")}
            >
              <MoreHorizontal />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-48">
            {pages.length === 1 ? (
              <DropdownMenuItem asChild>
                <Link to={pageLink(pages[0]!)}>
                  <LibraryIcon aria-hidden />
                  {t("web.collections.pageActions.addToMyLibraryPage", { library: pages[0]!.name })}
                </Link>
              </DropdownMenuItem>
            ) : (
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <LibraryIcon aria-hidden />
                  {addToMyLibraryPage()}
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
