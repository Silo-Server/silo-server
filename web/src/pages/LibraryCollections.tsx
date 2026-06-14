import type { LibraryTabCollection, LibraryTabGroup, LibraryTabUngrouped } from "@/api/types";
import { useLibraryCollections } from "@/hooks/queries/libraryCollections";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  COLLECTION_POSTER_GRID_CLASSES as GRID_CLASSES,
  CollectionPosterCard,
} from "@/components/collections/CollectionPosterCard";

interface LibraryCollectionsProps {
  libraryId: number;
}

export default function LibraryCollections({ libraryId }: LibraryCollectionsProps) {
  const { data, isLoading } = useLibraryCollections(libraryId);

  if (isLoading) {
    return (
      <div className="page-shell py-6 sm:py-8">
        <div className={GRID_CLASSES}>
          {Array.from({ length: 24 }, (_, i) => (
            <div key={i}>
              <Skeleton className="aspect-[2/3] rounded-lg" />
              <Skeleton className="mt-2 h-4 w-3/4" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  const groups = data?.groups ?? [];
  const ungroupedData = data?.ungrouped ?? null;
  const ungrouped = ungroupedData?.collections ?? [];

  if (groups.length === 0 && ungrouped.length === 0) {
    return (
      <div className="page-shell py-6 sm:py-8">
        <Card className="surface-panel overflow-hidden rounded-[2rem] border-0 shadow-none">
          <CardContent className="py-10 text-center">
            <p className="text-lg font-semibold">No collections yet</p>
            <p className="text-muted-foreground mt-2 text-sm">
              Create library collections from the admin area to feature curated shelves here.
            </p>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="page-shell space-y-6 py-6 sm:py-8">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Collections</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Browse hand-picked shelves and smart lists created for this library.
          </p>
        </div>
      </div>
      <div className="space-y-8">
        {buildRenderOrder(groups, ungroupedData).map((item) =>
          item.kind === "ungrouped" ? (
            <UngroupedGroupSection
              key="ungrouped"
              collections={item.collections}
              libraryId={libraryId}
            />
          ) : (
            <GroupSection key={item.group.id} group={item.group} libraryId={libraryId} />
          ),
        )}
      </div>
    </div>
  );
}

// Build a unified render order from groups + (optional) ungrouped, sorted by
// each item's effective sort position.
type RenderItem =
  | { kind: "group"; group: LibraryTabGroup }
  | { kind: "ungrouped"; collections: LibraryTabCollection[] };

function buildRenderOrder(
  groups: LibraryTabGroup[],
  ungroupedData: LibraryTabUngrouped | null,
): RenderItem[] {
  type Slot = { order: number; item: RenderItem };
  const slots: Slot[] = groups.map((g) => ({
    order: g.sort_order,
    item: { kind: "group" as const, group: g },
  }));
  if (ungroupedData && ungroupedData.collections.length > 0) {
    slots.push({
      order: ungroupedData.sort_order,
      item: { kind: "ungrouped" as const, collections: ungroupedData.collections },
    });
  }
  slots.sort((a, b) => a.order - b.order);
  return slots.map((s) => s.item);
}

function UngroupedGroupSection({
  collections,
  libraryId,
}: {
  collections: LibraryTabCollection[];
  libraryId: number;
}) {
  return (
    <section>
      <div className={GRID_CLASSES}>
        {collections.map((c) => (
          <CollectionPosterCard key={c.id} collection={c} kind="regular" libraryId={libraryId} />
        ))}
      </div>
    </section>
  );
}

function GroupSection({ group, libraryId }: { group: LibraryTabGroup; libraryId: number }) {
  return (
    <section>
      <h2 className="mb-3 text-lg font-semibold">{group.name}</h2>
      <div className={GRID_CLASSES}>
        {group.collections.map((c) => (
          <CollectionPosterCard key={c.id} collection={c} kind={group.kind} libraryId={libraryId} />
        ))}
      </div>
    </section>
  );
}
