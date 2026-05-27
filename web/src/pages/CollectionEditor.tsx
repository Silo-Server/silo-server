import { useNavigate, useParams } from "react-router";

import type { Collection, UserCollectionType } from "@/api/types";
import PageBack from "@/components/PageBack";
import { Card, CardHeader, CardDescription, CardTitle } from "@/components/ui/card";
import { useCollections } from "@/hooks/queries/collections";

import { ImportedCollectionEditor } from "./ImportedCollectionEditor";
import SmartCollectionWizard from "./SmartCollectionWizard";
import { UserCollectionForm } from "./userCollectionsShared";
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
  const { id } = useParams<{ id: string }>();
  const { data: collections = [], isLoading } = useCollections();
  const collection = id ? (collections.find((entry) => entry.id === id) ?? null) : null;

  if (isLoading && id) {
    return <div className="page-shell py-8">Loading collection editor...</div>;
  }

  if (id && !collection && !isLoading) {
    return (
      <div className="page-shell relative space-y-4 py-4 sm:py-6">
        <PageBack to="/collections" preferHistory={false} />
        <Card className="surface-panel mt-10 rounded-[1.7rem] border-0 shadow-none sm:mt-12">
          <CardHeader>
            <CardTitle>Collection not found</CardTitle>
            <CardDescription>The selected collection could not be loaded.</CardDescription>
          </CardHeader>
        </Card>
      </div>
    );
  }

  if (collection && isImportedCollection(collection)) {
    return (
      <div className="page-shell relative space-y-6 py-4 sm:py-6">
        <PageBack to="/collections" preferHistory={false} />
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
          onClose={() => navigate("/collections")}
        />
      </div>
    );
  }

  // Legacy manual collections keep the long-form editor; smart and new go to
  // the wizard so users can see the live card preview while tuning filters.
  if (collection && collection.collection_type === "manual") {
    return (
      <div className="page-shell relative space-y-6 py-4 sm:py-6">
        <PageBack to="/collections" preferHistory={false} />
        <div className="mt-10 sm:mt-12">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Edit {collection.name}</h1>
          <p className="page-subtitle mt-1 text-sm sm:text-base">
            Manual collections are curated by adding titles directly.
          </p>
        </div>
        <UserCollectionForm collection={collection} onClose={() => navigate("/collections")} />
        <section className="space-y-3">
          <h2 className="text-lg font-semibold">Items</h2>
          <p className="text-muted-foreground text-sm">
            Drag the handle to reorder. The saved order is what every viewer sees.
          </p>
          <ManualCollectionItemsEditor collectionId={collection.id} />
        </section>
      </div>
    );
  }

  return (
    <SmartCollectionWizard
      mode="user"
      collection={collection}
      onClose={() => navigate("/collections")}
    />
  );
}
