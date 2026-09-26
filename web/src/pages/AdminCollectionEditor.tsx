import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft } from "lucide-react";

import { isNotFoundProblem } from "@/api/v2/request";
import { Button } from "@/components/ui/button";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { CollectionTemplateGallery } from "@/components/CollectionTemplateGallery";
import { ManualCollectionItemsEditor } from "@/components/collections/ManualCollectionItemsEditor";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminCollections, useAdminCollectionSnapshot } from "@/hooks/queries/admin/collections";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";

import {
  buildAdminCollectionsReturnPath,
  CollectionEditForm,
  CollectionForm,
  MDBListImportForm,
  SourceTypeSelector,
  TMDBPresetForm,
  type CollectionSourceType,
} from "./adminCollectionsShared";
import SmartCollectionWizard from "./SmartCollectionWizard";

function inferCollectionSourceType(collectionType?: string): CollectionSourceType {
  if (collectionType === "mdblist") return "mdblist";
  if (collectionType === "tmdb") return "tmdb";
  if (collectionType === "trakt") return "trakt";
  return "manual";
}

function isImportedAdminCollectionType(collectionType?: string): boolean {
  return collectionType === "mdblist" || collectionType === "tmdb" || collectionType === "trakt";
}

export default function AdminCollectionEditor() {
  const navigate = useNavigate();
  const { id } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const initialLibraryId = Number(searchParams.get("libraryId")) || null;
  const returnPath = buildAdminCollectionsReturnPath(initialLibraryId);
  const isCreate = !id;
  const { data: libraries = [] } = useAdminLibraries();
  const { data: collections = [], isLoading } = useAdminCollections();
  const snapshot = useAdminCollectionSnapshot(id);
  const [frozen, setFrozen] = useState<typeof snapshot.data>(undefined);
  if (snapshot.data && !isLoading && frozen?.collection.id !== id) {
    const listed = collections.find((entry) => entry.id === id);
    setFrozen({
      ...snapshot.data,
      collection: {
        ...snapshot.data.collection,
        poster_url: listed?.poster_url ?? "",
        backdrop_url: listed?.backdrop_url ?? "",
      },
    });
  }
  // The frozen copy survives background refetches so an edit is never
  // clobbered, but a 404 means the collection is gone and outranks it.
  const collection =
    frozen && frozen.collection.id === id && !isNotFoundProblem(snapshot.error)
      ? frozen.collection
      : null;
  const [sourceType, setSourceType] = useState<CollectionSourceType | null>(null);
  const [galleryOpen, setGalleryOpen] = useState(false);

  const activeSourceType = collection
    ? inferCollectionSourceType(collection.collection_type)
    : sourceType;

  const sourceTypeTitles: Record<CollectionSourceType, string> = {
    manual: "New Manual Collection",
    mdblist: "Import MDBList Collection",
    tmdb: "Import TMDB Collection",
    trakt: "Import Trakt Collection",
  };
  const title = collection
    ? `Edit ${collection.title}`
    : (activeSourceType && sourceTypeTitles[activeSourceType]) || "Add Collection";

  const description = collection
    ? "Collections now open in a dedicated workspace so rules, artwork, and preview can stay visible."
    : activeSourceType === null
      ? "Choose how this collection should be created."
      : "Build the collection in a full-page editor instead of a cramped dialog.";

  useDocumentTitle(!isCreate && isNotFoundProblem(snapshot.error) ? "Not found" : title);

  if ((!isCreate && (snapshot.isLoading || isLoading)) || (isLoading && libraries.length === 0)) {
    return <div className="page-shell py-8">Loading collection editor...</div>;
  }

  if (!isCreate && !collection && !isLoading) {
    if (snapshot.error && !isNotFoundProblem(snapshot.error)) {
      return (
        <PageUnavailable
          title="Couldn't load this collection"
          description="Something went wrong while loading it. Try again in a moment."
          onRetry={() => void snapshot.refetch()}
          retrying={snapshot.isFetching}
        />
      );
    }
    return (
      <PageUnavailable
        title="Collection not found"
        description="It may have been deleted, or the link may be wrong."
      >
        <Button asChild variant="outline">
          <ViewTransitionLink to={returnPath} up>
            All collections
          </ViewTransitionLink>
        </Button>
      </PageUnavailable>
    );
  }

  // The wizard owns its own page chrome (back button, title, step indicator).
  // Short-circuit the legacy editor shell so we don't render nested headers.
  const useWizard = collection && collection.collection_type === "smart";
  if (useWizard) {
    return (
      <SmartCollectionWizard
        mode="admin"
        etag={frozen?.etag}
        collection={collection}
        libraries={libraries}
        initialLibraryId={initialLibraryId}
        onClose={() => navigate(returnPath)}
      />
    );
  }

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <Button asChild variant="ghost" className="w-fit px-0">
            <Link to={returnPath}>
              <ArrowLeft className="mr-2 h-4 w-4" />
              Back to Collections
            </Link>
          </Button>
          <div>
            <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">{title}</h1>
            <p className="page-subtitle mt-1 text-sm sm:text-base">{description}</p>
          </div>
        </div>

        {!collection && activeSourceType !== null ? (
          <Button variant="outline" onClick={() => setSourceType(null)}>
            Change Source Type
          </Button>
        ) : null}
      </div>

      {!collection && activeSourceType === null ? (
        <Card className="surface-panel rounded-2xl border-0 shadow-none">
          <CardHeader>
            <CardTitle>Choose a Collection Type</CardTitle>
            <CardDescription>
              Smart/manual collections open the full query builder. Imports keep their
              source-specific setup.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <SourceTypeSelector
              showTemplates
              onSelect={(type) => {
                if (type === "templates") {
                  setGalleryOpen(true);
                } else {
                  setSourceType(type);
                }
              }}
            />
          </CardContent>
        </Card>
      ) : null}

      <CollectionTemplateGallery
        open={galleryOpen}
        onOpenChange={setGalleryOpen}
        libraries={libraries}
        initialLibraryId={initialLibraryId}
        onCreated={() => {
          if (isCreate) navigate(returnPath);
        }}
      />

      {collection ? (
        // Smart admin collections route to the wizard above; here we only see
        // imported (mdblist/tmdb/trakt) or legacy manual collections.
        isImportedAdminCollectionType(collection.collection_type) ? (
          <CollectionEditForm
            libraries={libraries}
            collection={collection}
            etag={frozen?.etag}
            initialLibraryId={initialLibraryId}
            onClose={() => navigate(returnPath)}
          />
        ) : (
          <CollectionForm
            libraries={libraries}
            collection={collection}
            etag={frozen?.etag}
            initialLibraryId={initialLibraryId}
            onClose={() => navigate(returnPath)}
          />
        )
      ) : null}

      {collection?.collection_type === "manual" && (
        <section className="space-y-3">
          <h2 className="text-lg font-semibold">Items</h2>
          <ManualCollectionItemsEditor collectionId={collection.id} source="library" />
        </section>
      )}

      {!collection && activeSourceType === "manual" ? (
        <CollectionForm
          libraries={libraries}
          collection={null}
          initialLibraryId={initialLibraryId}
          onClose={() => navigate(returnPath)}
        />
      ) : null}

      {!collection && activeSourceType === "mdblist" ? (
        <MDBListImportForm
          libraries={libraries}
          initialLibraryId={initialLibraryId}
          onClose={() => navigate(returnPath)}
        />
      ) : null}

      {!collection && activeSourceType === "tmdb" ? (
        <TMDBPresetForm
          libraries={libraries}
          initialLibraryId={initialLibraryId}
          onClose={() => navigate(returnPath)}
        />
      ) : null}
    </div>
  );
}
