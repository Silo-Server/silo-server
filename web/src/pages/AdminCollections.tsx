import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import type { AdminJob, Library, LibraryCollection, LibraryCollectionGroup } from "@/api/types";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useAdminCollectionsBoard,
  useCreateCollectionGroup,
  useUpdateCollectionGroup,
  useDeleteCollectionGroup,
} from "@/hooks/queries/admin/collectionGroups";
import {
  useAdminCollections,
  useDeleteAdminCollection,
  useSyncAdminCollection,
  useTemplateBundleApplyJobs,
} from "@/hooks/queries/admin/collections";
import { invalidateAdminCollectionQueries } from "@/hooks/queries/collectionSurfaceRefresh";
import { sectionKeys } from "@/hooks/queries/keys";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { GroupsBoard } from "@/components/collections/admin/GroupsBoard";
import { GroupEditDialog } from "@/components/collections/admin/GroupEditDialog";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  AlertCircle,
  CheckCircle2,
  Library as LibraryIcon,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Sparkles,
  Trash2,
} from "lucide-react";
import { CollectionTemplateGallery } from "@/components/CollectionTemplateGallery";
import { buildAdminCollectionEditorPath } from "./adminCollectionsShared";

export default function AdminCollections() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: libraries = [] } = useAdminLibraries();
  const requestedLibraryId = Number(searchParams.get("libraryId"));
  const initialLibraryId =
    Number.isFinite(requestedLibraryId) && requestedLibraryId > 0 ? requestedLibraryId : null;
  const selectedLibraryId = initialLibraryId;
  const [galleryOpen, setGalleryOpen] = useState(false);
  const [editingGroup, setEditingGroup] = useState<{
    mode: "create" | "edit";
    id?: string;
  } | null>(null);
  const [confirmDeleteCollection, setConfirmDeleteCollection] = useState<LibraryCollection | null>(
    null,
  );

  const allCollections = useAdminCollections();
  const libraryCounts = useMemo(
    () => countCollectionsByLibrary(libraries, allCollections.data ?? []),
    [libraries, allCollections.data],
  );

  const setSelectedLibraryId = (libraryId: number | null) => {
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      if (libraryId) {
        next.set("libraryId", String(libraryId));
      } else {
        next.delete("libraryId");
      }
      return next;
    });
  };

  const board = useAdminCollectionsBoard(selectedLibraryId ?? undefined);
  const createGroup = useCreateCollectionGroup(selectedLibraryId ?? 0);
  const updateGroup = useUpdateCollectionGroup(selectedLibraryId ?? 0);
  const deleteGroup = useDeleteCollectionGroup(selectedLibraryId ?? 0);
  const deleteCollection = useDeleteAdminCollection();
  const syncCollection = useSyncAdminCollection();
  const isAllLibraries = selectedLibraryId === null;
  const applyJobs = useTemplateBundleApplyJobs();
  useEventChannel("jobs");
  const latestApplyJob = applyJobs.data?.[0] ?? null;
  const activeApplyJob = latestApplyJob ? isActiveTemplateBundleApplyJob(latestApplyJob) : false;
  const lastInvalidatedJobID = useRef<string | null>(null);

  useEffect(() => {
    if (!latestApplyJob || activeApplyJob || lastInvalidatedJobID.current === latestApplyJob.id) {
      return;
    }
    lastInvalidatedJobID.current = latestApplyJob.id;
    void invalidateAdminCollectionQueries(queryClient);
    void queryClient.invalidateQueries({ queryKey: sectionKeys.all });
  }, [activeApplyJob, latestApplyJob, queryClient]);

  if (allCollections.isLoading && libraries.length === 0) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-10 w-full rounded-lg" />
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  const editingTarget: LibraryCollectionGroup | null =
    editingGroup?.mode === "edit" && editingGroup.id
      ? (board.data?.groups.find((g) => g.id === editingGroup.id) ?? null)
      : null;

  const boardCollectionCount =
    (board.data?.ungrouped.length ?? 0) +
    (board.data?.groups.reduce((sum, group) => sum + group.collections.length, 0) ?? 0);
  const hasRegularBoardGroups =
    board.data?.groups.some((group) => group.kind === "regular") ?? false;
  const showScopedEmpty =
    !isAllLibraries &&
    !board.isLoading &&
    board.data &&
    boardCollectionCount === 0 &&
    !hasRegularBoardGroups;

  return (
    <div className="space-y-6">
      <ConfirmDialog
        open={confirmDeleteCollection !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteCollection(null);
        }}
        title="Delete collection"
        description={`Delete collection "${confirmDeleteCollection?.title}"? This action cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          if (confirmDeleteCollection) {
            deleteCollection.mutate({
              id: confirmDeleteCollection.id,
              libraryId: selectedLibraryId ?? confirmDeleteCollection.library_id,
            });
          }
          setConfirmDeleteCollection(null);
        }}
      />

      <CollectionApplyJobBanner job={latestApplyJob} />

      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Collections</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Curate library shelves and sync them from MDBList or TMDB trending.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <AdminCollectionsLibrarySelect
            libraries={libraries}
            value={selectedLibraryId}
            counts={libraryCounts}
            totalCount={allCollections.data?.length ?? 0}
            onChange={setSelectedLibraryId}
          />
          {!isAllLibraries ? (
            <Button size="sm" variant="outline" onClick={() => setEditingGroup({ mode: "create" })}>
              <Plus className="mr-1 h-4 w-4" /> New Group
            </Button>
          ) : null}
          <Button size="sm" variant="outline" onClick={() => setGalleryOpen(true)}>
            <Sparkles className="mr-1 h-4 w-4" /> Browse Templates
          </Button>
          <Button
            size="sm"
            onClick={() => navigate(buildAdminCollectionEditorPath("new", selectedLibraryId))}
          >
            <Plus className="mr-1 h-4 w-4" /> Add Collection
          </Button>
        </div>
      </div>

      <CollectionTemplateGallery
        open={galleryOpen}
        onOpenChange={setGalleryOpen}
        libraries={libraries}
        initialLibraryId={selectedLibraryId}
      />

      {isAllLibraries ? (
        <AllLibraryCollectionsOverview
          libraries={libraries}
          collections={allCollections.data ?? []}
          isLoading={allCollections.isLoading}
          syncingCollectionID={syncCollection.variables?.id ?? null}
          onEdit={(collection, libraryId) =>
            navigate(buildAdminCollectionEditorPath(collection.id, libraryId))
          }
          onDelete={setConfirmDeleteCollection}
          onSync={(collection, libraryId) =>
            syncCollection.mutate({
              id: collection.id,
              libraryId,
            })
          }
          onCreate={() => navigate(buildAdminCollectionEditorPath("new", null))}
          onOpenTemplates={() => setGalleryOpen(true)}
        />
      ) : null}

      {!isAllLibraries && board.isLoading && (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-32 w-full rounded-lg" />
          ))}
        </div>
      )}

      {!isAllLibraries && board.data && !showScopedEmpty && (
        <GroupsBoard
          libraryID={selectedLibraryId}
          groups={board.data.groups}
          ungrouped={board.data.ungrouped}
          ungroupedSortOrder={board.data.ungroupedSortOrder}
          onEditGroup={(id) => setEditingGroup({ mode: "edit", id })}
          onEditCollection={(collection) =>
            navigate(buildAdminCollectionEditorPath(collection.id, selectedLibraryId))
          }
          onDeleteCollection={(collection) => setConfirmDeleteCollection(collection)}
          onSyncCollection={(collection) =>
            syncCollection.mutate({
              id: collection.id,
              libraryId: selectedLibraryId,
            })
          }
          syncingCollectionID={syncCollection.variables?.id ?? null}
        />
      )}

      {showScopedEmpty && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
          <div className="space-y-1">
            <p className="text-sm font-medium">No collections yet</p>
            <p className="text-muted-foreground max-w-sm text-xs">
              Create collections for this library, or sync them from MDBList or TMDB trending.
            </p>
          </div>
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Button variant="outline" size="sm" onClick={() => setGalleryOpen(true)}>
              <Sparkles className="mr-1 h-4 w-4" /> Start from a template
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => navigate(buildAdminCollectionEditorPath("new", selectedLibraryId))}
            >
              <Plus className="mr-1 h-4 w-4" /> Create from scratch
            </Button>
          </div>
        </div>
      )}

      {editingGroup && (
        <GroupEditDialog
          mode={editingGroup.mode}
          group={editingTarget}
          onCancel={() => setEditingGroup(null)}
          onSubmit={async (input) => {
            try {
              if (editingGroup.mode === "create") {
                await createGroup.mutateAsync(input);
              } else if (editingGroup.id) {
                await updateGroup.mutateAsync({ id: editingGroup.id, ...input });
              }
              setEditingGroup(null);
            } catch {
              // toast already shown by mutation onError
            }
          }}
          onDelete={
            editingGroup.mode === "edit" && editingGroup.id
              ? async () => {
                  if (!editingGroup.id) return;
                  try {
                    await deleteGroup.mutateAsync(editingGroup.id);
                    setEditingGroup(null);
                  } catch {
                    // toast already shown by mutation onError
                  }
                }
              : undefined
          }
        />
      )}
    </div>
  );
}

function CollectionApplyJobBanner({ job }: { job: AdminJob | null }) {
  if (!job || job.job_type !== "template_bundle_apply") {
    return null;
  }

  const active = isActiveTemplateBundleApplyJob(job);
  const recent = active || isRecentTemplateBundleApplyJob(job);
  if (!recent) {
    return null;
  }

  if (job.status === "failed") {
    return (
      <div className="border-destructive/30 bg-destructive/5 text-destructive rounded-lg border px-4 py-3">
        <div className="flex items-start gap-3">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0 space-y-1">
            <p className="text-sm font-medium">Collection defaults apply failed</p>
            <p className="text-xs">{job.error_message || job.message || "The job failed."}</p>
          </div>
        </div>
      </div>
    );
  }

  if (job.status === "completed") {
    return (
      <div className="border-border bg-muted/30 rounded-lg border px-4 py-3">
        <div className="flex items-start gap-3">
          <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-500" />
          <div className="min-w-0 space-y-1">
            <p className="text-sm font-medium">Collection defaults applied</p>
            <p className="text-muted-foreground text-xs">{templateBundleApplySummary(job)}</p>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="border-border bg-muted/30 rounded-lg border px-4 py-3">
      <div className="flex items-start gap-3">
        <Loader2 className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0 animate-spin" />
        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm font-medium">Applying collection defaults</p>
            <p className="text-muted-foreground text-xs">{job.message || "Working..."}</p>
          </div>
          <div className="progress-bar">
            <div className="progress-fill animate-pulse" style={{ width: "40%" }} />
          </div>
        </div>
      </div>
    </div>
  );
}

function isActiveTemplateBundleApplyJob(job: AdminJob) {
  return job.status === "queued" || job.status === "running";
}

function isRecentTemplateBundleApplyJob(job: AdminJob) {
  const timestamp = job.completed_at ?? job.requested_at;
  const parsed = Date.parse(timestamp);
  if (Number.isNaN(parsed)) {
    return false;
  }
  return Date.now() - parsed < 10 * 60_000;
}

function templateBundleApplySummary(job: AdminJob) {
  const payload = job.result_payload as Record<string, unknown> | undefined;
  const created = resultArrayLength(payload, "created");
  const skipped = resultArrayLength(payload, "skipped");
  const failed = resultArrayLength(payload, "failed");
  const syncQueued = resultArrayLength(payload, "sync_queued");
  const featured = resultArrayLength(payload, "featured");
  const parts = [
    `Created ${created}`,
    `skipped ${skipped}`,
    failed > 0 ? `failed ${failed}` : "",
    syncQueued > 0 ? `queued ${syncQueued} initial syncs` : "",
    featured > 0 ? `featured ${featured}` : "",
  ].filter(Boolean);
  return parts.join("; ");
}

function resultArrayLength(payload: Record<string, unknown> | undefined, key: string) {
  const value = payload?.[key];
  return Array.isArray(value) ? value.length : 0;
}

function AdminCollectionsLibrarySelect({
  libraries,
  value,
  counts,
  totalCount,
  onChange,
}: {
  libraries: Library[];
  value: number | null;
  counts: Map<number, number>;
  totalCount: number;
  onChange: (libraryId: number | null) => void;
}) {
  return (
    <Select
      value={value ? String(value) : "all"}
      onValueChange={(next) => onChange(next === "all" ? null : Number(next))}
    >
      <SelectTrigger className="w-full sm:w-[240px]">
        <SelectValue placeholder="Choose library" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">All libraries ({totalCount})</SelectItem>
        {libraries.map((library) => (
          <SelectItem key={library.id} value={String(library.id)}>
            {library.name} ({counts.get(library.id) ?? 0})
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function AllLibraryCollectionsOverview({
  libraries,
  collections,
  isLoading,
  syncingCollectionID,
  onEdit,
  onDelete,
  onSync,
  onCreate,
  onOpenTemplates,
}: {
  libraries: Library[];
  collections: LibraryCollection[];
  isLoading: boolean;
  syncingCollectionID: string | null;
  onEdit: (collection: LibraryCollection, libraryId: number) => void;
  onDelete: (collection: LibraryCollection) => void;
  onSync: (collection: LibraryCollection, libraryId: number) => void;
  onCreate: () => void;
  onOpenTemplates: () => void;
}) {
  const sections = useMemo(
    () => buildAllLibrarySections(libraries, collections),
    [libraries, collections],
  );

  if (isLoading) {
    return (
      <div className="space-y-3">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-24 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  if (collections.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
        <LibraryIcon className="text-muted-foreground h-9 w-9" />
        <div className="space-y-1">
          <p className="text-sm font-medium">No collections yet</p>
          <p className="text-muted-foreground max-w-sm text-xs">
            Create collections to curate library shelves, or sync them from MDBList or TMDB
            trending.
          </p>
        </div>
        <div className="flex flex-wrap items-center justify-center gap-2">
          <Button variant="outline" size="sm" onClick={onOpenTemplates}>
            <Sparkles className="mr-1 h-4 w-4" /> Start from a template
          </Button>
          <Button variant="ghost" size="sm" onClick={onCreate}>
            <Plus className="mr-1 h-4 w-4" /> Create from scratch
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {sections.map((section) => (
        <section key={section.library.id} className="bg-background rounded-lg border">
          <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
            <h2 className="text-base font-semibold">{section.library.name}</h2>
            <Badge variant="outline">
              {section.collections.length} collection{section.collections.length === 1 ? "" : "s"}
            </Badge>
          </div>
          <div className="divide-y">
            {section.collections.map((collection) => (
              <AllLibraryCollectionRow
                key={`${section.library.id}:${collection.id}`}
                collection={collection}
                libraries={libraries}
                libraryId={section.library.id}
                isSyncing={syncingCollectionID === collection.id}
                onEdit={() => onEdit(collection, section.library.id)}
                onDelete={() => onDelete(collection)}
                onSync={() => onSync(collection, section.library.id)}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function AllLibraryCollectionRow({
  collection,
  libraries,
  libraryId,
  isSyncing,
  onEdit,
  onDelete,
  onSync,
}: {
  collection: LibraryCollection;
  libraries: Library[];
  libraryId: number;
  isSyncing: boolean;
  onEdit: () => void;
  onDelete: () => void;
  onSync: () => void;
}) {
  const syncable = collection.collection_type !== "manual";
  const collectionLibraries = collectionLibraryIDs(collection)
    .map((id) => libraries.find((library) => library.id === id)?.name ?? `Library ${id}`)
    .join(", ");

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      {collection.poster_url ? (
        <img src={collection.poster_url} alt="" className="h-12 w-8 rounded object-cover" />
      ) : (
        <div className="bg-muted h-12 w-8 rounded" />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <p className="truncate text-sm font-medium">{collection.title}</p>
          {collection.featured ? <Badge variant="secondary">Featured</Badge> : null}
          {collection.visibility === "hidden" ? <Badge variant="outline">Hidden</Badge> : null}
        </div>
        <div className="text-muted-foreground mt-1 flex flex-wrap items-center gap-2 text-xs">
          <span>{collection.item_count} items</span>
          <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
            {collection.collection_type}
          </Badge>
          <span>{collectionLibraries}</span>
        </div>
      </div>
      <div className="flex items-center gap-1">
        {syncable ? (
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label={`Sync ${collection.title}`}
            disabled={isSyncing}
            onClick={onSync}
          >
            <RefreshCw className={isSyncing ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
          </Button>
        ) : null}
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          aria-label={`Edit ${collection.title}`}
          onClick={onEdit}
        >
          <Pencil className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="text-destructive hover:bg-destructive/10 hover:text-destructive h-8 w-8"
          aria-label={`Delete ${collection.title}`}
          onClick={onDelete}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
      <span className="sr-only">Shown under library {libraryId}</span>
    </div>
  );
}

function countCollectionsByLibrary(
  libraries: Library[],
  collections: LibraryCollection[],
): Map<number, number> {
  const counts = new Map(libraries.map((library) => [library.id, 0]));
  for (const collection of collections) {
    for (const libraryId of collectionLibraryIDs(collection)) {
      counts.set(libraryId, (counts.get(libraryId) ?? 0) + 1);
    }
  }
  return counts;
}

function buildAllLibrarySections(libraries: Library[], collections: LibraryCollection[]) {
  return libraries
    .map((library) => ({
      library,
      collections: collections
        .filter((collection) => collectionLibraryIDs(collection).includes(library.id))
        .sort((a, b) => a.sort_order - b.sort_order || a.title.localeCompare(b.title)),
    }))
    .filter((section) => section.collections.length > 0);
}

function collectionLibraryIDs(collection: LibraryCollection): number[] {
  return collection.library_ids.length > 0 ? collection.library_ids : [collection.library_id];
}
