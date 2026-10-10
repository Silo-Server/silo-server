import { useState } from "react";
import { Navigate, useLocation, useParams, useSearchParams } from "react-router";

import { isNotFoundProblem } from "@/api/v2/request";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { CollectionEditorShell } from "@/components/collections/editor/CollectionEditorShell";
import { CollectionEditor } from "@/components/collections/editor/CollectionEditor";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useScopeEditor } from "@/hooks/queries/collectionScope";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { newCollectionPickerHref } from "@/lib/collections/dialogs";
import {
  PERSONAL_SCOPE,
  SERVER_SCOPE,
  type CollectionScope,
  type CreateKind,
  type ScopeKind,
} from "@/lib/collections/scope";
import { useListReturnPath } from "@/lib/collections/listReturn";
import type { SyncedTab } from "@/lib/collections/synced";

const CREATE_KINDS: readonly CreateKind[] = ["manual", "smart", "synced"];
const SYNCED_TABS: readonly SyncedTab[] = ["mdblist", "tmdb_chart", "tmdb_list"];

/** The collection's page with the lock callout, for someone who can't change it. */
function readOnlyHref(href: string) {
  return `${href}${href.includes("?") ? "&" : "?"}notice=read-only`;
}

/** Header and cards in their final places while the collection loads; no save bar. */
function EditorSkeleton() {
  const panel = (rows: number) => (
    <div className="surface-panel grid gap-4 rounded-[22px] p-5 sm:p-6">
      <Skeleton className="h-5 w-28" />
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton key={index} className="h-11 rounded-xl" />
      ))}
    </div>
  );
  return (
    <div role="status" aria-busy="true">
      <span className="sr-only">Loading collection editor…</span>
      <CollectionEditorShell
        header={
          <div aria-hidden className="grid gap-4">
            <Skeleton className="h-4 w-24" />
            <div className="flex items-end gap-5">
              <Skeleton className="aspect-[2/3] w-16 rounded-[10px]" />
              <div className="grid flex-1 gap-2">
                <Skeleton className="h-5 w-20" />
                <Skeleton className="h-8 w-2/3" />
                <Skeleton className="h-4 w-40" />
              </div>
            </div>
          </div>
        }
      >
        {panel(2)}
        {panel(5)}
        {panel(4)}
      </CollectionEditorShell>
    </div>
  );
}

/**
 * Every collection editor URL, both scopes: `/new?type=…[&source=…]` and
 * `/:id/edit`. Mounted once per family by a pathless route, so it stays the
 * same page from `/new` to `/:id/edit` after Create. Every type opens the
 * same editor, create and edit. `/new` with no type opens the collection
 * list with the New collection picker over it, so old links still work.
 * Someone who can't change the collection is sent to its page instead of a form.
 */
export default function CollectionEditorPage({ scope: scopeKind }: { scope: ScopeKind }) {
  const scope = (scopeKind === "server" ? SERVER_SCOPE : PERSONAL_SCOPE) as CollectionScope;
  const { id } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const libraryId = Number(searchParams.get("libraryId")) || null;
  const type = CREATE_KINDS.find((kind) => kind === searchParams.get("type"));
  const createKind = type ?? "manual";
  const source = SYNCED_TABS.find((tab) => tab === searchParams.get("source"));
  const location = useLocation();
  // The collection the create editor made, its kind, and the visit it was made
  // on: the editor carries on at its edit URL. Any other visit starts a fresh one.
  const [created, setCreated] = useState<{
    round: number;
    id?: string;
    visit?: string;
    kind?: CreateKind;
  }>({ round: 0 });
  if (created.id && (id ? id !== created.id : location.key !== created.visit)) {
    setCreated({ round: created.round + 1 });
  }
  const carriedOn = Boolean(id) && id === created.id;
  const editor = useScopeEditor(scope, carriedOn ? undefined : id);
  const { profile, isLoading: profileLoading } = useCurrentProfile();
  const listPath = useListReturnPath(scope.paths.list({ libraryId }));

  if ((!id && type) || carriedOn) {
    const kind = carriedOn ? created.kind : createKind;
    return (
      <CollectionEditor
        key={`new-${created.round}-${kind}`}
        scope={scope}
        kind={kind}
        libraryId={libraryId}
        syncedTab={source}
        onCreated={(newId) =>
          setCreated({ ...created, id: newId, visit: location.key, kind: createKind })
        }
      />
    );
  }
  if (!id) return <Navigate replace to={newCollectionPickerHref(scopeKind, libraryId)} />;

  const { snapshot } = editor;
  if (!snapshot) {
    if (editor.isLoading) return <EditorSkeleton />;
    if (editor.error && !isNotFoundProblem(editor.error)) {
      return (
        <PageUnavailable
          title="Couldn't load this collection"
          description="Something went wrong while loading it. Try again in a moment."
          onRetry={() => void editor.refetch()}
          retrying={editor.isFetching}
        />
      );
    }
    return <CollectionNotFound scope={scopeKind} listPath={listPath} />;
  }

  // Ownership fails closed while the profile is unknown, so wait for it.
  if (scopeKind === "personal" && !profile && profileLoading) return <EditorSkeleton />;
  if (scope.isReadOnly(snapshot.view, profile?.id)) {
    return <Navigate replace to={readOnlyHref(scope.paths.browse(snapshot.view))} />;
  }

  return <CollectionEditor key={snapshot.view.id} scope={scope} snapshot={snapshot} />;
}

function CollectionNotFound({ scope, listPath }: { scope: ScopeKind; listPath: string }) {
  useDocumentTitle("Not found");
  return (
    <PageUnavailable
      title={scope === "server" ? "Collection not found" : "This collection isn't available"}
      description={
        scope === "server"
          ? "It may have been deleted, or the link may be wrong."
          : "It may have been deleted, or you may not have access to it."
      }
    >
      <Button asChild variant="outline">
        <ViewTransitionLink to={listPath} up>
          All collections
        </ViewTransitionLink>
      </Button>
    </PageUnavailable>
  );
}
