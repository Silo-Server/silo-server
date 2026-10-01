import { useState } from "react";
import type { CollectionEditSnapshot } from "@/api/personalCollections";
import { useNavigate, useParams } from "react-router";

import type { Collection, UserCollectionType } from "@/api/types";
import { isNotFoundProblem } from "@/api/v2/request";
import PageBack from "@/components/PageBack";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import {
  useCollections,
  useCollectionCapabilities,
  useCollectionEditSnapshot,
} from "@/hooks/queries/collections";

import { ImportedCollectionEditor } from "./ImportedCollectionEditor";
import SmartCollectionWizard from "./SmartCollectionWizard";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { UserCollectionForm, isCollectionReadOnly } from "./userCollectionsShared";
import { ManualCollectionItemsEditor } from "@/components/collections/ManualCollectionItemsEditor";

type ImportedType = Extract<UserCollectionType, "mdblist" | "tmdb" | "trakt">;
const IMPORTED_TYPES = new Set<ImportedType>(["mdblist", "tmdb", "trakt"]);

function isImportedCollection(
  collection: Collection,
): collection is Collection & { collection_type: ImportedType } {
  return IMPORTED_TYPES.has(collection.collection_type as ImportedType);
}

export default function CollectionEditor() {
  const navigate = useNavigate();
  const { profile } = useCurrentProfile();
  const { data: capabilities } = useCollectionCapabilities();
  const { id } = useParams<{ id: string }>();
  const { data: collections = [] } = useCollections();
  const { data: fetched, isLoading, isFetching, error, refetch } = useCollectionEditSnapshot(id);
  const [snapshot, setSnapshot] = useState<CollectionEditSnapshot>();
  if (fetched && fetched.collection.id === id && snapshot?.collection.id !== id) {
    const artwork = collections.find((entry) => entry.id === id)?.poster_url;
    setSnapshot({
      ...fetched,
      collection: { ...fetched.collection, poster_url: artwork ?? fetched.collection.poster_url },
    });
  }
  const collection = id && snapshot?.collection.id === id ? snapshot.collection : null;

  if (isLoading && id) {
    return <div className="page-shell py-8">Loading collection editor...</div>;
  }

  if (id && !collection && !isLoading) {
    if (error && !isNotFoundProblem(error)) {
      return (
        <PageUnavailable
          title="Couldn't load this collection"
          description="Something went wrong while loading it. Try again in a moment."
          onRetry={() => void refetch()}
          retrying={isFetching}
        />
      );
    }
    return (
      <PageUnavailable
        title="This collection isn't available"
        description="It may have been deleted, or you may not have access to it."
      >
        <Button asChild variant="outline">
          <ViewTransitionLink to="/collections" up>
            All collections
          </ViewTransitionLink>
        </Button>
      </PageUnavailable>
    );
  }

  if (collection && isImportedCollection(collection)) {
    return (
      <div className="page-shell relative space-y-6 py-4 sm:py-6">
        <PageBack to="/collections" up />
        <div className="mt-10 sm:mt-12">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">{collection.name}</h1>
          <p className="page-subtitle mt-1 text-sm sm:text-base">
            Edit what's local — name, libraries, sharing. Source-managed details (URL, schedule,
            item ordering) are locked.
          </p>
        </div>
        <ImportedCollectionEditor
          key={collection.id}
          collection={collection}
          etag={snapshot!.etag}
          onClose={() => navigate("/collections")}
        />
      </div>
    );
  }

  // New collections use the mode selector; saved smart collections keep their preview wizard.
  if (!collection || collection.collection_type === "manual") {
    return (
      <div className="page-shell relative space-y-6 py-4 sm:py-6">
        <PageBack to="/collections" up />
        <div className="mt-10 sm:mt-12">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">
            {collection ? `Edit ${collection.name}` : "New Collection"}
          </h1>
          <p className="page-subtitle mt-1 text-sm sm:text-base">
            {collection
              ? "Manual collections are curated by adding titles directly."
              : "Choose a manual collection to pick titles yourself, or a smart collection to match filters."}
          </p>
        </div>
        <UserCollectionForm
          collection={collection}
          etag={snapshot?.etag}
          onClose={() => navigate("/collections")}
        />
        {collection && (
          <section className="space-y-3">
            <h2 className="text-lg font-semibold">Items</h2>
            {capabilities?.item_reorder && (
              <p className="text-muted-foreground text-sm">
                Drag the handle to reorder. The saved order is what every viewer sees.
              </p>
            )}
            <ManualCollectionItemsEditor
              collectionId={collection.id}
              readOnly={isCollectionReadOnly(collection, profile?.id)}
            />
          </section>
        )}
      </div>
    );
  }

  return (
    <SmartCollectionWizard
      mode="user"
      collection={collection}
      etag={snapshot?.etag}
      onClose={() => navigate("/collections")}
    />
  );
}
