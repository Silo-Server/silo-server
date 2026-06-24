import { useEffect, useState } from "react";
import type { Collection, CreateCollectionRequest, UpdateCollectionRequest } from "@/api/types";
import { useProfiles } from "@/hooks/queries/profiles";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import {
  useCreateCollection,
  useDeleteUserCollectionImage,
  useUpdateCollection,
} from "@/hooks/queries/collections";
import { buildUserCollectionCatalogHref as buildCatalogHrefForUserCollection } from "@/pages/catalogSearchParams";
import {
  collectionMediaFilterLabel,
  collectionWatchFilterLabel,
  queryDefinitionToDisplayFilters,
} from "@/lib/collectionDisplayFilters";
import { CollectionLibraryPicker } from "@/pages/adminCollectionsShared";
import CollectionBuilder, {
  createCollectionBuilderValue,
  type CollectionBuilderValue,
} from "@/components/collections/CollectionBuilder";
import { ImageUploadField } from "@/components/ImageUploadField";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export function buildUserCollectionEditorPath(id: "new" | string) {
  return id === "new" ? "/collections/new" : `/collections/${id}/edit`;
}

export function buildUserCollectionCatalogHref(id: string, title?: string) {
  return buildCatalogHrefForUserCollection(id, title);
}

// Imported types (mdblist/tmdb/trakt) collapse to "manual" in the builder
// since the sync service owns their items; the original type stays in the DB.
function builderCollectionType(raw: Collection["collection_type"] | undefined): "manual" | "smart" {
  return raw === "smart" ? "smart" : "manual";
}

export function toUserCollectionBuilderValue(
  collection: Collection | null,
): CollectionBuilderValue {
  return createCollectionBuilderValue({
    title: collection?.name ?? "",
    collection_type: builderCollectionType(collection?.collection_type ?? "smart"),
    query_definition: collection?.query_definition,
    sort_config: collection?.sort_config ?? {},
    access: {
      is_shared: collection?.is_shared ?? false,
      allowed_profile_ids: collection?.allowed_profile_ids ?? [],
    },
    include_in_server_collections: collection?.include_in_server_collections ?? false,
    display_query_definition: collection?.display_query_definition,
  });
}

export function toCreateCollectionBody(value: CollectionBuilderValue): CreateCollectionRequest {
  const body: CreateCollectionRequest = {
    name: value.title,
    collection_type: value.collection_type,
    is_shared: value.access.is_shared,
    allowed_profile_ids: value.access.allowed_profile_ids,
    query_definition: value.collection_type === "smart" ? value.query_definition : undefined,
    sort_config: value.collection_type === "smart" ? value.sort_config : undefined,
    include_in_server_collections: value.include_in_server_collections,
  };
  if (value.collection_type === "manual") {
    body.display_query_definition = value.display_query_definition;
  }
  return body;
}

export function toUpdateCollectionBody(value: CollectionBuilderValue): UpdateCollectionRequest {
  const body: UpdateCollectionRequest = {
    name: value.title,
    is_shared: value.access.is_shared,
    allowed_profile_ids: value.access.allowed_profile_ids,
    query_definition: value.collection_type === "smart" ? value.query_definition : undefined,
    sort_config: value.collection_type === "smart" ? value.sort_config : undefined,
    include_in_server_collections: value.include_in_server_collections,
  };
  if (value.collection_type === "manual") {
    body.display_query_definition = value.display_query_definition;
  }
  return body;
}

export function isCollectionReadOnly(
  collection: Collection | null,
  currentProfileId?: string | null,
): boolean {
  if (!collection || !currentProfileId) {
    return false;
  }
  return collection.creator_profile_id !== currentProfileId;
}

function UserCollectionSummary({
  draft,
  collection,
  libraries,
}: {
  draft: CollectionBuilderValue;
  collection: Collection | null;
  libraries: Array<{ id: number; name: string }>;
}) {
  const profileSummary =
    draft.access.allowed_profile_ids.length > 0
      ? `${draft.access.allowed_profile_ids.length} selected`
      : "All profiles";

  const { watch: displayWatch, media: displayMedia } = queryDefinitionToDisplayFilters(
    draft.display_query_definition,
  );

  const selectedLibraryIDs = draft.query_definition.library_ids;
  const librarySummary =
    selectedLibraryIDs.length === 0
      ? "All libraries"
      : libraries
          .filter((lib) => selectedLibraryIDs.includes(lib.id))
          .map((lib) => lib.name)
          .join(", ") || `${selectedLibraryIDs.length} selected`;

  return (
    <Card className="surface-panel gap-0 rounded-[1.5rem] border-0 shadow-none">
      <CardHeader>
        <CardTitle>Collection Summary</CardTitle>
        <CardDescription>Preview and sharing stay visible while you edit.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <SummaryRow label="Mode" value={draft.collection_type === "smart" ? "Smart" : "Manual"} />
        <SummaryRow label="Libraries" value={librarySummary} />
        {draft.collection_type === "manual" ? (
          <>
            <SummaryRow label="Watch state" value={collectionWatchFilterLabel(displayWatch)} />
            <SummaryRow label="Content" value={collectionMediaFilterLabel(displayMedia)} />
          </>
        ) : null}
        <SummaryRow label="Shared" value={draft.access.is_shared ? "Yes" : "No"} />
        <SummaryRow label="Profiles" value={profileSummary} />
        <SummaryRow
          label="In my library tab"
          value={draft.include_in_server_collections ? "Yes" : "No"}
        />
        {collection ? <SummaryRow label="Collection" value={collection.name} /> : null}
      </CardContent>
    </Card>
  );
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-medium">{value}</span>
    </div>
  );
}

export function UserCollectionForm({
  collection,
  onClose,
}: {
  collection: Collection | null;
  onClose: () => void;
}) {
  const [draft, setDraft] = useState(() => toUserCollectionBuilderValue(collection));
  const [posterFile, setPosterFile] = useState<File | null>(null);
  const [posterSourceUrl, setPosterSourceUrl] = useState("");
  const createMutation = useCreateCollection();
  const updateMutation = useUpdateCollection();
  const deletePosterMutation = useDeleteUserCollectionImage();
  const { data: profiles = [] } = useProfiles();
  const { data: libraries = [] } = useUserLibraries();
  const { profile } = useCurrentProfile();
  const isPending = createMutation.isPending || updateMutation.isPending;
  const readOnly = isCollectionReadOnly(collection, profile?.id);

  useEffect(() => {
    setDraft(toUserCollectionBuilderValue(collection));
    setPosterFile(null);
    setPosterSourceUrl("");
  }, [collection]);

  function handleSubmit() {
    const trimmedSource = posterSourceUrl.trim();
    if (collection) {
      const body: UpdateCollectionRequest = {
        ...toUpdateCollectionBody(draft),
        poster_source_url: trimmedSource || undefined,
      };
      updateMutation.mutate(
        { id: collection.id, body, poster: posterFile },
        { onSuccess: onClose },
      );
    } else {
      const body: CreateCollectionRequest = {
        ...toCreateCollectionBody(draft),
        poster_source_url: trimmedSource || undefined,
      };
      createMutation.mutate({ body, poster: posterFile }, { onSuccess: onClose });
    }
  }

  function setLibraryIDs(next: number[]) {
    setDraft({
      ...draft,
      query_definition: { ...draft.query_definition, library_ids: next },
    });
  }

  const builderLibraries = libraries.map((lib) => ({ id: lib.id, name: lib.name, type: lib.type }));

  return (
    <CollectionBuilder
      mode="user"
      value={draft}
      onChange={setDraft}
      onSubmit={handleSubmit}
      submitLabel="Save Collection"
      profiles={profiles.map((entry) => ({ id: entry.id, name: entry.name }))}
      libraries={builderLibraries}
      allowAccessControls
      allowLibrarySelection
      isPending={isPending}
      readOnly={readOnly}
      creatorProfileId={collection?.creator_profile_id ?? null}
      previewLayout="sidebar"
      sidebarContent={
        <UserCollectionSummary draft={draft} collection={collection} libraries={builderLibraries} />
      }
    >
      {draft.collection_type === "smart" ? (
        <section className="space-y-4">
          <div>
            <h2 className="text-lg font-semibold">Libraries</h2>
            <p className="text-muted-foreground mt-1 text-sm">
              Limit this collection to specific libraries. Leave empty to span every library you can
              see.
            </p>
          </div>
          <CollectionLibraryPicker
            libraries={builderLibraries}
            value={draft.query_definition.library_ids}
            onChange={setLibraryIDs}
          />
        </section>
      ) : null}

      {!readOnly ? (
        <section className="space-y-4">
          <div>
            <h2 className="text-lg font-semibold">Poster</h2>
            <p className="text-muted-foreground mt-1 text-sm">
              Upload a custom poster or paste an image URL. Leave empty to fall back to the default
              card.
            </p>
          </div>
          <div className="grid gap-4 md:grid-cols-2">
            <ImageUploadField
              label="Poster"
              currentUrl={collection?.poster_url}
              file={posterFile}
              onFileChange={setPosterFile}
              sourceUrl={posterSourceUrl}
              onSourceUrlChange={setPosterSourceUrl}
              onDelete={
                collection?.poster_url
                  ? () => deletePosterMutation.mutate({ id: collection.id, type: "poster" })
                  : undefined
              }
            />
          </div>
        </section>
      ) : null}
    </CollectionBuilder>
  );
}
