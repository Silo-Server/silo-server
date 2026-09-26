import { useEffect, useMemo, useRef, useState } from "react";
import type { Library, PageSectionConfig } from "@/api/types";
import {
  useAdminSections,
  useAdminSectionCapabilities,
  useBulkCreateSections,
  useCreateSection,
  useUpdateSection,
  useDeleteSection,
  useDeleteSections,
  useReorderSections,
  useRestoreDefaultSections,
} from "@/hooks/queries/sections";
import RecipeGalleryModal from "@/components/RecipeGallery/RecipeGalleryModal";
import RecipeConfigDrawer from "@/components/RecipeGallery/RecipeConfigDrawer";
import type { AddPayload } from "@/components/RecipeGallery/RecipeConfigDrawer";
import type { RecipeDefinition, GalleryPreset } from "@/lib/recipes";
import { fetchRecipeCatalog } from "@/lib/recipes";
import { useQuery } from "@tanstack/react-query";
import { useAdminCollections, useImportTraktCollection } from "@/hooks/queries/admin/collections";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Loader2, Plus, RotateCcw, Trash2 } from "lucide-react";

import { toast } from "sonner";
import {
  fetchAdminSectionSnapshot,
  fetchAdminSectionDeleteTargets,
  fetchAdminSectionOrderSnapshot,
} from "@/api/adminSections";
import { V2ProblemError } from "@/api/v2/request";
import SectionEditorDrawer from "@/components/sections/SectionEditorDrawer";
import {
  SectionDragOverlay,
  SortableSectionTableRow,
  type EditableSectionViewModel,
} from "@/components/sections/EditableSectionRows";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  KeyboardSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import type { DragStartEvent, DragEndEvent } from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy, arrayMove } from "@dnd-kit/sortable";
import { sortableKeyboardCoordinates } from "@dnd-kit/sortable";
import type { LibraryCollection } from "@/api/types";
import {
  createAdminSectionCreation,
  runAdminSectionCreation,
  type AdminSectionCreation,
} from "@/lib/adminSectionCreation";
import { updateCheckboxSelection } from "@/lib/checkboxSelection";

function formatCollectionOptionLabel(
  collection: { title: string; library_id: number },
  libraries: Library[],
): string {
  const library = libraries.find((entry) => entry.id === collection.library_id);
  return library ? `${collection.title} (${library.name})` : collection.title;
}

function LibraryPicker({
  disabled,
  libraries,
  value,
  onChange,
}: {
  disabled: boolean;
  libraries: Library[];
  value: number | null;
  onChange: (libraryId: number) => void;
}) {
  return (
    <Select
      disabled={disabled}
      value={value ? String(value) : undefined}
      onValueChange={(next) => onChange(Number(next))}
    >
      <SelectTrigger className="w-[220px]">
        <SelectValue placeholder="Choose library" />
      </SelectTrigger>
      <SelectContent>
        {libraries.map((library) => (
          <SelectItem key={library.id} value={String(library.id)}>
            {library.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function toEditableSection(section: PageSectionConfig): EditableSectionViewModel {
  return {
    id: section.id,
    title: section.title,
    sectionType: section.section_type,
    itemLimit: section.item_limit,
    featured: section.featured,
    enabled: section.enabled,
    isCustom: true,
    config: section.config,
  };
}

interface TraktPublicRecipeConfig {
  preset: "trending" | "popular";
  mediaType: "movie" | "tv";
}

function getTraktRecipeConfig(config: Record<string, unknown>): TraktPublicRecipeConfig | null {
  if (config.source_provider !== "trakt") return null;
  const preset = config.source_preset;
  const mediaType = config.media_type;
  if (
    (preset !== "trending" && preset !== "popular") ||
    (mediaType !== "movie" && mediaType !== "tv")
  ) {
    return null;
  }
  return { preset, mediaType };
}

function isMatchingTraktCollection(
  collection: LibraryCollection,
  preset: string,
  mediaType: string,
  libraryID: number,
) {
  return (
    collection.management_mode === "section" &&
    collection.management_key ===
      buildTraktSectionManagedCollectionKey(preset, mediaType, libraryID) &&
    collection.collection_type === "trakt" &&
    collection.library_id === libraryID &&
    collection.source_config?.preset === preset &&
    collection.source_config?.media_type === mediaType
  );
}

function buildTraktSectionManagedCollectionKey(
  preset: string,
  mediaType: string,
  libraryID: number,
) {
  return `trakt:${preset}:${mediaType}:library:${libraryID}`;
}

function findDefaultTraktLibrary(libraries: Library[], mediaType: string): number | null {
  const wantedType = mediaType === "tv" ? "series" : "movies";
  return libraries.find((library) => library.type === wantedType)?.id ?? libraries[0]?.id ?? null;
}

export default function AdminSections() {
  const [scope, setScope] = useState("home");
  const { data: librariesData } = useAdminLibraries();
  const librariesList = useMemo(() => librariesData ?? [], [librariesData]);
  const [selectedLibraryId, setSelectedLibraryId] = useState<number | null>(null);
  const activeLibraryId = scope === "library" ? selectedLibraryId : null;
  const {
    data,
    isLoading,
    isError,
    error: listError,
    refetch,
  } = useAdminSections(scope, activeLibraryId ?? undefined);
  const { data: capabilities } = useAdminSectionCapabilities();
  const [snapshotLoading, setSnapshotLoading] = useState(false);
  const snapshotRequest = useRef(0);
  const [editingETag, setEditingETag] = useState<string | null>(null);
  const [editConflict, setEditConflict] = useState(false);
  const [orderConflict, setOrderConflict] = useState(false);
  const [deleteETag, setDeleteETag] = useState<string | null>(null);
  const [deleteConflict, setDeleteConflict] = useState(false);
  const [deleteTargets, setDeleteTargets] = useState<
    Awaited<ReturnType<typeof fetchAdminSectionDeleteTargets>>
  >([]);
  const [restoreSnapshot, setRestoreSnapshot] = useState<Awaited<
    ReturnType<typeof fetchAdminSectionOrderSnapshot>
  > | null>(null);
  const [restoreConflict, setRestoreConflict] = useState(false);
  const dragSnapshot = useRef<{
    sections: PageSectionConfig[];
    etag: string;
    scope: string;
    library_id?: number;
  } | null>(null);
  const { data: collectionsData = [] } = useAdminCollections();
  const { data: recipeCatalog } = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: 5 * 60 * 1000,
  });
  const collectionLabels = new Map(
    collectionsData.map((collection) => [
      collection.id,
      formatCollectionOptionLabel(collection, librariesList),
    ]),
  );
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingSection, setEditingSection] = useState<PageSectionConfig | null>(null);
  const [orderedSections, setOrderedSections] = useState<PageSectionConfig[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [confirmDeleteSection, setConfirmDeleteSection] = useState<PageSectionConfig | null>(null);
  const [confirmDeleteSelected, setConfirmDeleteSelected] = useState(false);
  const [confirmDeleteAll, setConfirmDeleteAll] = useState(false);
  const [selectedSectionIds, setSelectedSectionIds] = useState<Set<string>>(new Set());
  const selectionAnchorRef = useRef<string | null>(null);
  const deleteMutation = useDeleteSection();
  const deleteSectionsMutation = useDeleteSections();
  const reorderMutation = useReorderSections();
  const restoreDefaultsMutation = useRestoreDefaultSections();
  const [confirmRestoreOpen, setConfirmRestoreOpen] = useState(false);
  const [resetProfiles, setResetProfiles] = useState(false);
  const [galleryOpen, setGalleryOpen] = useState(false);
  const [pickedRecipe, setPickedRecipe] = useState<{
    def: RecipeDefinition;
    preset: GalleryPreset;
  } | null>(null);
  const createFromGalleryMutation = useCreateSection();
  const importTraktMutation = useImportTraktCollection();
  const createMutation = useCreateSection();
  const bulkCreateMutation = useBulkCreateSections();
  const updateMutation = useUpdateSection();
  const [creation, setCreation] = useState<{ state: AdminSectionCreation; scope: string } | null>(
    null,
  );
  const [creationRunning, setCreationRunning] = useState(false);
  const creationRunningRef = useRef(false);

  const creationUnresolved = Boolean(
    creation?.state.targets.some((target) => target.status !== "complete"),
  );

  const sections = useMemo(() => data?.sections ?? [], [data?.sections]);
  const orderedSectionIds = useMemo(
    () => orderedSections.map((section) => section.id),
    [orderedSections],
  );
  const selectedSections = useMemo(
    () => orderedSections.filter((section) => selectedSectionIds.has(section.id)),
    [orderedSections, selectedSectionIds],
  );
  const isHomeScope = scope === "home";
  const canManageCurrentScope =
    !isError &&
    Boolean(capabilities?.available) &&
    (isHomeScope || (librariesList.length > 0 && selectedLibraryId !== null));
  const canDrag =
    activeId !== null ||
    (!orderConflict &&
      !reorderMutation.isPending &&
      !snapshotLoading &&
      canManageCurrentScope &&
      Boolean(data?.etag) &&
      orderedSections.length <= 10000 &&
      data?.ordered_ids.join("\0") === orderedSectionIds.join("\0"));

  useEffect(() => {
    if (librariesList.length === 0) {
      setSelectedLibraryId(null);
      return;
    }

    setSelectedLibraryId((current) => {
      if (current && librariesList.some((library) => library.id === current)) {
        return current;
      }
      return librariesList[0]?.id ?? null;
    });
  }, [librariesList]);

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  useEffect(() => {
    if (!dragSnapshot.current && !orderConflict && !reorderMutation.isPending)
      setOrderedSections(sections);
  }, [sections, orderConflict, reorderMutation.isPending]);

  useEffect(() => {
    const clearSelection = (event: KeyboardEvent) => {
      if (
        event.key === "Escape" &&
        confirmDeleteSection === null &&
        !confirmDeleteSelected &&
        !confirmDeleteAll
      ) {
        setSelectedSectionIds(new Set());
        selectionAnchorRef.current = null;
      }
    };
    window.addEventListener("keydown", clearSelection);
    return () => window.removeEventListener("keydown", clearSelection);
  }, [confirmDeleteAll, confirmDeleteSection, confirmDeleteSelected]);

  function clearSectionSelection() {
    setSelectedSectionIds(new Set());
    selectionAnchorRef.current = null;
  }

  function handleScopeChange(nextScope: string) {
    if (reorderMutation.isPending || nextScope === scope) return;
    setOrderedSections([]);
    snapshotRequest.current++;
    dragSnapshot.current = null;
    setActiveId(null);
    setOrderConflict(false);
    clearSectionSelection();
    setScope(nextScope);
  }

  function handleLibraryChange(libraryId: number) {
    if (reorderMutation.isPending || libraryId === selectedLibraryId) return;
    setOrderedSections([]);
    snapshotRequest.current++;
    dragSnapshot.current = null;
    setActiveId(null);
    setOrderConflict(false);
    clearSectionSelection();
    setSelectedLibraryId(libraryId);
  }

  function updateSectionSelection(sectionId: string, checked: boolean, extendRange: boolean) {
    const anchorId = extendRange && selectedSectionIds.size > 0 ? selectionAnchorRef.current : null;
    setSelectedSectionIds((previous) =>
      updateCheckboxSelection(
        previous,
        orderedSectionIds,
        anchorId,
        sectionId,
        checked,
        extendRange,
      ),
    );
    if (anchorId === null || !orderedSectionIds.includes(anchorId)) {
      selectionAnchorRef.current = sectionId;
    }
  }

  async function prepareSnapshot(action: () => Promise<void>) {
    setSnapshotLoading(true);
    try {
      await action();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Could not load current sections");
    } finally {
      setSnapshotLoading(false);
    }
  }

  function handleDelete(section: PageSectionConfig) {
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const snapshot = await fetchAdminSectionSnapshot(section.id);
      if (request !== snapshotRequest.current) return;
      setConfirmDeleteSection(snapshot.section);
      setDeleteETag(snapshot.etag);
      setDeleteConflict(false);
    });
  }

  function handleEdit(section: PageSectionConfig) {
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const snapshot = await fetchAdminSectionSnapshot(section.id);
      if (request !== snapshotRequest.current) return;
      setEditingSection(snapshot.section);
      setEditingETag(snapshot.etag);
      setEditConflict(false);
      setDialogOpen(true);
    });
  }

  function prepareBulkDelete(all: boolean) {
    const ids = (all ? orderedSections : selectedSections).map((section) => section.id);
    if (ids.length > 100) {
      toast.error("Select at most 100 sections per deletion.");
      return;
    }
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const targets = await fetchAdminSectionDeleteTargets(ids);
      if (request !== snapshotRequest.current) return;
      setDeleteTargets(targets);
      setConfirmDeleteAll(all);
      setConfirmDeleteSelected(!all);
    });
  }

  function openRestore() {
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const snapshot = await fetchAdminSectionOrderSnapshot(scope, activeLibraryId ?? undefined);
      if (request !== snapshotRequest.current) return;
      setRestoreSnapshot(snapshot);
      setRestoreConflict(false);
      setConfirmRestoreOpen(true);
    });
  }

  function handleDragStart(event: DragStartEvent) {
    if (!canDrag || !data?.etag) return;
    dragSnapshot.current = {
      sections: orderedSections,
      etag: data.etag,
      scope,
      library_id: activeLibraryId ?? undefined,
    };
    setActiveId(event.active.id as string);
  }

  function handleDragEnd(event: DragEndEvent) {
    const snapshot = dragSnapshot.current;
    dragSnapshot.current = null;
    setActiveId(null);
    const { active, over } = event;
    if (!snapshot || !over || active.id === over.id) {
      setOrderedSections(sections);
      return;
    }
    const oldIndex = snapshot.sections.findIndex((s) => s.id === active.id);
    const newIndex = snapshot.sections.findIndex((s) => s.id === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    const nextSections = arrayMove(snapshot.sections, oldIndex, newIndex);
    setOrderedSections(nextSections);
    reorderMutation.mutate(
      {
        scope: snapshot.scope,
        library_id: snapshot.library_id,
        etag: snapshot.etag,
        ordered_ids: nextSections.map((s) => s.id),
      },
      {
        onError: (error) => {
          setOrderConflict(true);
          toast.error(error instanceof Error ? error.message : "Could not reorder sections");
        },
      },
    );
  }

  function handleDragCancel() {
    dragSnapshot.current = null;
    setActiveId(null);
    setOrderedSections(sections);
  }

  const activeSection = activeId ? (orderedSections.find((s) => s.id === activeId) ?? null) : null;
  const selectedLibraryName = librariesList.find((library) => library.id === activeLibraryId)?.name;
  const sectionScopeLabel = isHomeScope ? "home" : `${selectedLibraryName ?? "library"} library`;
  const sectionDeletionNotice =
    "Silo will also try to remove section-managed collections that are no longer referenced. This action cannot be undone.";
  const deleteProgressLabel = `Deleting ${deleteSectionsMutation.progress?.completed ?? 0} of ${deleteSectionsMutation.progress?.total ?? deleteTargets.length} sections`;

  function handleDeleteCapturedSections() {
    deleteSectionsMutation.mutate(deleteTargets, {
      onSuccess: (result) => {
        setSelectedSectionIds(new Set(result.failedIds));
        setConfirmDeleteAll(false);
        setConfirmDeleteSelected(false);
      },
    });
  }

  function normalizeLibraryIDs(ids: number[] | undefined): number[] {
    if (!ids || ids.length === 0) return [];
    return Array.from(new Set(ids.filter((id) => Number.isInteger(id) && id > 0)));
  }

  async function ensureTraktSectionCollection(
    payload: AddPayload,
    traktRecipe: TraktPublicRecipeConfig,
    libraryID: number,
  ): Promise<LibraryCollection> {
    const existing = collectionsData.find((collection) =>
      isMatchingTraktCollection(collection, traktRecipe.preset, traktRecipe.mediaType, libraryID),
    );
    if (existing) return existing;

    const managementKey = buildTraktSectionManagedCollectionKey(
      traktRecipe.preset,
      traktRecipe.mediaType,
      libraryID,
    );
    const imported = await importTraktMutation.mutateAsync({
      body: {
        library_id: libraryID,
        title: payload.title,
        description: "",
        preset: traktRecipe.preset,
        media_type: traktRecipe.mediaType,
        limit: payload.item_limit,
        featured: payload.featured,
        management_mode: "section",
        management_source: "recipe_gallery",
        management_key: managementKey,
      },
    });
    return imported.collection;
  }

  async function runTraktCreation(state: AdminSectionCreation, targetScope: string) {
    if (creationRunningRef.current) return;
    creationRunningRef.current = true;
    setCreationRunning(true);
    setCreation({ state, scope: targetScope });
    try {
      const result = await runAdminSectionCreation(state, {
        resolveCollection: async (payload, libraryID) => {
          const recipe = getTraktRecipeConfig(payload.config);
          if (!recipe) throw new Error("This recipe no longer identifies a Trakt list");
          return (await ensureTraktSectionCollection(payload, recipe, libraryID)).id;
        },
        createSection: async (payload, libraryID, collectionID) => {
          const created = await createMutation.mutateAsync({
            scope: targetScope,
            ...(targetScope === "library" ? { library_id: libraryID } : {}),
            section_type: payload.section_type,
            title: payload.title,
            item_limit: payload.item_limit,
            featured: payload.featured,
            enabled: payload.enabled,
            config: { ...payload.config, library_collection_id: collectionID },
          });
          return created.id;
        },
        onProgress: (next) => setCreation({ state: next, scope: targetScope }),
      });
      const completed = result.targets.filter((target) => target.status === "complete").length;
      if (completed === result.targets.length)
        toast.success(`Created ${completed} section${completed === 1 ? "" : "s"}`);
      else
        toast.warning(
          `Created ${completed} of ${result.targets.length} sections. Review the remaining targets below.`,
        );
      setCreation({ state: result, scope: targetScope });
    } finally {
      creationRunningRef.current = false;
      setCreationRunning(false);
    }
  }

  async function createBulkSectionsFromGallery(
    payload: AddPayload,
    libraryIDs: number[],
  ): Promise<void> {
    if (libraryIDs.length === 0) {
      throw new Error("Choose at least one library before applying this section");
    }

    const config = payload.config;
    const traktRecipe = getTraktRecipeConfig(config);
    const selectedCollectionID =
      typeof config.library_collection_id === "string" ? config.library_collection_id.trim() : "";
    if (traktRecipe && selectedCollectionID === "") {
      await runTraktCreation(createAdminSectionCreation(payload, libraryIDs), "library");
      return;
    }

    const result = await bulkCreateMutation.mutateAsync({
      scope: "library",
      library_ids: libraryIDs,
      section_type: payload.section_type,
      title: payload.title,
      item_limit: payload.item_limit,
      featured: payload.featured,
      enabled: payload.enabled,
      config,
    });
    toast.success(`Created ${result.created} section${result.created === 1 ? "" : "s"}`);
  }

  async function createSectionFromGallery(payload: AddPayload) {
    const bulkLibraryIDs = normalizeLibraryIDs(payload.library_ids);
    if (payload.apply_to_all_libraries || bulkLibraryIDs.length > 0) {
      await createBulkSectionsFromGallery(payload, bulkLibraryIDs);
      return;
    }

    const config = payload.config;
    const traktRecipe = getTraktRecipeConfig(config);
    const selectedCollectionID =
      typeof config.library_collection_id === "string" ? config.library_collection_id.trim() : "";
    if (traktRecipe && selectedCollectionID === "") {
      const targetLibraryID =
        activeLibraryId ?? findDefaultTraktLibrary(librariesList, traktRecipe.mediaType);
      if (!targetLibraryID) {
        throw new Error("Choose a library before adding this Trakt section");
      }
      await runTraktCreation(createAdminSectionCreation(payload, [targetLibraryID]), scope);
      return;
    }

    const data: Partial<PageSectionConfig> = {
      scope,
      ...(scope === "library" && activeLibraryId != null ? { library_id: activeLibraryId } : {}),
      section_type: payload.section_type,
      title: payload.title,
      item_limit: payload.item_limit,
      featured: payload.featured,
      enabled: payload.enabled,
      config,
    };
    await createFromGalleryMutation.mutateAsync(data);
  }

  if (isLoading) return <div className="p-4">Loading sections...</div>;

  return (
    <div
      className="space-y-6"
      aria-busy={deleteSectionsMutation.isPending}
      inert={deleteSectionsMutation.isPending ? true : undefined}
    >
      <Dialog
        open={confirmDeleteSection !== null}
        onOpenChange={(open) => {
          if (!open && !deleteMutation.isPending) {
            snapshotRequest.current++;
            setConfirmDeleteSection(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete section</DialogTitle>
          </DialogHeader>
          <p>
            Delete section "{confirmDeleteSection?.title}"? {sectionDeletionNotice}
          </p>
          {deleteConflict && (
            <p role="alert">This section changed. Reload it before confirming deletion.</p>
          )}
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={deleteMutation.isPending}
              onClick={() => {
                snapshotRequest.current++;
                setConfirmDeleteSection(null);
              }}
            >
              Cancel
            </Button>
            {deleteConflict ? (
              <Button
                disabled={snapshotLoading}
                onClick={() => {
                  if (confirmDeleteSection) handleDelete(confirmDeleteSection);
                }}
              >
                Reload section
              </Button>
            ) : (
              <Button
                variant="destructive"
                disabled={deleteMutation.isPending || !deleteETag}
                onClick={() => {
                  if (!confirmDeleteSection || !deleteETag) return;
                  deleteMutation.mutate(
                    { id: confirmDeleteSection.id, etag: deleteETag },
                    {
                      onSuccess: () => setConfirmDeleteSection(null),
                      onError: (error) => {
                        setDeleteConflict(error instanceof V2ProblemError && error.status === 412);
                        toast.error(
                          error instanceof Error ? error.message : "Could not delete section",
                        );
                      },
                    },
                  );
                }}
              >
                Delete
              </Button>
            )}
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={confirmDeleteSelected || confirmDeleteAll}
        onOpenChange={(open) => {
          if (!open && !deleteSectionsMutation.isPending) {
            setConfirmDeleteSelected(false);
            setConfirmDeleteAll(false);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {confirmDeleteAll ? "Delete all sections" : "Delete selected sections"}
            </DialogTitle>
          </DialogHeader>
          <p>
            Delete {deleteTargets.length} captured sections? {sectionDeletionNotice}
          </p>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={deleteSectionsMutation.isPending}
              onClick={() => {
                setConfirmDeleteSelected(false);
                setConfirmDeleteAll(false);
              }}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={deleteSectionsMutation.isPending}
              onClick={handleDeleteCapturedSections}
            >
              Delete {deleteTargets.length} sections
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={confirmRestoreOpen}
        onOpenChange={(open) => {
          if (restoreDefaultsMutation.isPending) return;
          setConfirmRestoreOpen(open);
          if (!open) {
            snapshotRequest.current++;
            setResetProfiles(false);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Restore Default Sections</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <p className="text-muted-foreground text-sm">
              This will replace all {restoreSnapshot?.scope === "home" ? "home" : "library"}{" "}
              sections with the defaults. Any custom sections will be removed.
            </p>
            <div className="flex items-center gap-2">
              <Switch
                id="resetProfiles"
                size="sm"
                disabled={!capabilities?.reset_profiles || restoreDefaultsMutation.isPending}
                checked={resetProfiles}
                onCheckedChange={(checked) => setResetProfiles(checked === true)}
              />
              <Label htmlFor="resetProfiles" className="text-sm font-normal">
                Also reset all user customizations for this scope
              </Label>
            </div>
            {restoreConflict && (
              <p role="alert">
                Sections changed. Reload the current scope before restoring defaults.
              </p>
            )}
            {!capabilities?.reset_profiles && (
              <p className="text-muted-foreground text-sm">
                Resetting user customizations is unavailable on this server.
              </p>
            )}
            <div className="flex justify-end gap-2">
              {restoreConflict && (
                <Button disabled={snapshotLoading} onClick={openRestore}>
                  Reload sections
                </Button>
              )}
              <Button
                variant="outline"
                disabled={restoreDefaultsMutation.isPending}
                onClick={() => {
                  snapshotRequest.current++;
                  setConfirmRestoreOpen(false);
                  setResetProfiles(false);
                }}
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                disabled={restoreDefaultsMutation.isPending || restoreConflict || !restoreSnapshot}
                onClick={() => {
                  restoreDefaultsMutation.mutate(
                    {
                      scope: restoreSnapshot!.scope,
                      ...(restoreSnapshot!.library_id != null
                        ? { library_id: Number(restoreSnapshot!.library_id) }
                        : {}),
                      etag: restoreSnapshot!.etag,
                      reset_profiles: Boolean(capabilities?.reset_profiles && resetProfiles),
                    },
                    {
                      onSuccess: () => {
                        toast.success("Sections restored to defaults");
                        setConfirmRestoreOpen(false);
                        setResetProfiles(false);
                      },
                      onError: (error) => {
                        setRestoreConflict(error instanceof V2ProblemError && error.status === 412);
                        toast.error("Failed to restore defaults");
                      },
                    },
                  );
                }}
              >
                Restore Defaults
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Sections</h1>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={
              !canManageCurrentScope || snapshotLoading || restoreDefaultsMutation.isPending
            }
            onClick={openRestore}
          >
            <RotateCcw className="mr-1 h-4 w-4" /> Restore Defaults
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={
              !canManageCurrentScope || snapshotLoading || creationRunning || creationUnresolved
            }
            onClick={() => setGalleryOpen(true)}
          >
            <Plus className="mr-1 h-4 w-4" /> Add from Gallery
          </Button>
          <Button
            size="sm"
            disabled={
              !canManageCurrentScope || snapshotLoading || creationRunning || creationUnresolved
            }
            onClick={() => {
              setEditingSection(null);
              setEditingETag(null);
              setEditConflict(false);
              setDialogOpen(true);
            }}
          >
            <Plus className="mr-1 h-4 w-4" /> Add Section
          </Button>
          {selectedSections.length > 0 ? (
            <>
              <Badge variant="secondary">{selectedSections.length} selected</Badge>
              <Button size="sm" variant="ghost" onClick={clearSectionSelection}>
                Clear
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={
                  snapshotLoading ||
                  selectedSections.length > 100 ||
                  deleteSectionsMutation.isPending ||
                  !canManageCurrentScope
                }
                onClick={() => prepareBulkDelete(false)}
              >
                <Trash2 data-icon="inline-start" /> Delete Selected
              </Button>
            </>
          ) : null}
          {orderedSections.length > 0 ? (
            <Button
              size="sm"
              variant="destructive"
              disabled={
                snapshotLoading ||
                orderedSections.length > 100 ||
                deleteSectionsMutation.isPending ||
                restoreDefaultsMutation.isPending ||
                !canManageCurrentScope
              }
              onClick={() => prepareBulkDelete(true)}
            >
              {deleteSectionsMutation.isPending ? (
                <Loader2 className="mr-1 h-4 w-4 animate-spin" />
              ) : (
                <Trash2 className="mr-1 h-4 w-4" />
              )}
              {deleteSectionsMutation.isPending ? `${deleteProgressLabel}…` : "Delete All"}
            </Button>
          ) : null}
        </div>
      </div>

      {isError && (
        <div role="alert">
          <p>{listError instanceof Error ? listError.message : "Could not load sections."}</p>
          <Button onClick={() => void refetch()}>Reload sections</Button>
        </div>
      )}
      {creation && (
        <div className="surface-panel space-y-2 rounded-xl p-4" role="status">
          <p>
            {creation.state.targets.filter((target) => target.status === "complete").length} of{" "}
            {creation.state.targets.length} sections created for "{creation.state.payload.title}".
          </p>
          {creation.state.targets.map((target) => (
            <p key={target.libraryID}>
              {librariesList.find((library) => library.id === target.libraryID)?.name ??
                `Library ${target.libraryID}`}
              : {target.status === "complete" ? "Created" : target.status.replace(/_/g, " ")}
              {target.collectionID ? ` · Collection ${target.collectionID}` : ""}
              {target.error ? ` · ${target.error}` : ""}
            </p>
          ))}
          {creation.state.targets.some(
            (target) => target.status === "import_unknown" || target.status === "section_unknown",
          ) && (
            <p role="alert">
              A creation response was not confirmed. Review Collections and Sections before creating
              that target again; it will not be retried automatically.
            </p>
          )}
          {creation.state.targets.some(
            (target) => target.status === "section_failed" || target.status === "pending",
          ) && (
            <Button
              disabled={creationRunning}
              onClick={() => void runTraktCreation(creation.state, creation.scope)}
            >
              Retry remaining sections
            </Button>
          )}
          {!creationRunning && creationUnresolved && (
            <Button variant="outline" onClick={() => setCreation(null)}>
              Finish review and clear tracking
            </Button>
          )}
          {!creationRunning &&
            creation.state.targets.every((target) => target.status === "complete") && (
              <Button variant="ghost" onClick={() => setCreation(null)}>
                Dismiss
              </Button>
            )}
        </div>
      )}
      {(orderedSections.length > 100 || selectedSections.length > 100) && (
        <p role="status">Select up to 100 sections per deletion. </p>
      )}
      {snapshotLoading && <p role="status">Loading current section details…</p>}
      <Tabs value={scope} onValueChange={handleScopeChange}>
        <TabsList>
          <TabsTrigger value="home" disabled={reorderMutation.isPending}>
            Home
          </TabsTrigger>
          <TabsTrigger value="library" disabled={reorderMutation.isPending}>
            Library
          </TabsTrigger>
        </TabsList>
      </Tabs>

      {!isHomeScope && (
        <div className="flex items-center gap-3">
          <Label>Library</Label>
          {librariesList.length > 0 ? (
            <LibraryPicker
              disabled={reorderMutation.isPending}
              libraries={librariesList}
              value={selectedLibraryId}
              onChange={handleLibraryChange}
            />
          ) : (
            <p className="text-muted-foreground text-sm">
              Create a library before configuring Library sections.
            </p>
          )}
        </div>
      )}

      {orderConflict && (
        <p role="alert">
          Sections changed.{" "}
          <Button
            variant="outline"
            onClick={() =>
              void refetch().then((result) => {
                if (!result.isError) setOrderConflict(false);
              })
            }
          >
            Reload order
          </Button>
        </p>
      )}
      {canDrag && (
        <p className="text-muted-foreground text-sm">
          Drag and drop sections to change their order.
        </p>
      )}

      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        onDragStart={handleDragStart}
        onDragEnd={handleDragEnd}
        onDragCancel={handleDragCancel}
      >
        <div className="surface-panel overflow-x-auto rounded-2xl border-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10">
                  <span className="sr-only">Select</span>
                </TableHead>
                <TableHead className="w-8"></TableHead>
                <TableHead>Title</TableHead>
                <TableHead>Type</TableHead>
                <TableHead className="w-20">Items</TableHead>
                <TableHead className="w-20">Featured</TableHead>
                <TableHead className="w-20">Enabled</TableHead>
                <TableHead className="w-24">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <SortableContext
              items={orderedSections.map((s) => s.id)}
              strategy={verticalListSortingStrategy}
            >
              <TableBody>
                {orderedSections.map((section) => (
                  <SortableSectionTableRow
                    key={section.id}
                    section={toEditableSection(section)}
                    canReorder={canDrag}
                    libraries={librariesList}
                    collectionLabels={collectionLabels}
                    catalog={recipeCatalog}
                    selected={selectedSectionIds.has(section.id)}
                    selectionLabel={`Select ${section.title} ${sectionScopeLabel} section`}
                    onSelectionChange={(checked, extendRange) =>
                      updateSectionSelection(section.id, checked, extendRange)
                    }
                    onEdit={() => handleEdit(section)}
                    onDelete={() => handleDelete(section)}
                  />
                ))}
                {!isError && orderedSections.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={8} className="text-muted-foreground py-8 text-center">
                      No sections configured for {scope} scope.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </SortableContext>
          </Table>
        </div>
        <DragOverlay>
          {activeSection ? (
            <SectionDragOverlay
              section={toEditableSection(activeSection)}
              catalog={recipeCatalog}
            />
          ) : null}
        </DragOverlay>
      </DndContext>

      <SectionEditorDrawer
        mode="admin"
        open={dialogOpen}
        onOpenChange={(open) => {
          setDialogOpen(open);
          if (!open) {
            snapshotRequest.current++;
            setEditingSection(null);
          }
        }}
        section={editingSection}
        conflict={editConflict}
        onReload={() => {
          if (editingSection) handleEdit(editingSection);
        }}
        scope={editingSection?.scope ?? scope}
        currentLibraryId={editingSection ? editingSection.library_id : activeLibraryId}
        libraries={librariesList}
        recipeCatalog={recipeCatalog}
        isSubmitting={
          createMutation.isPending || updateMutation.isPending || bulkCreateMutation.isPending
        }
        onSave={(section) => {
          if (section.id) {
            updateMutation.mutate(
              { ...section, id: section.id, etag: editingETag! },
              {
                onSuccess: () => {
                  setDialogOpen(false);
                  setEditingSection(null);
                },
                onError: (error) => {
                  setEditConflict(error instanceof V2ProblemError && error.status === 412);
                  toast.error(error instanceof Error ? error.message : "Failed to update section");
                },
              },
            );
          } else {
            createMutation.mutate(section, {
              onSuccess: () => {
                setDialogOpen(false);
                setEditingSection(null);
              },
              onError: (error) => {
                toast.error(error instanceof Error ? error.message : "Failed to create section");
              },
            });
          }
        }}
      />

      <RecipeGalleryModal
        open={galleryOpen}
        onClose={() => setGalleryOpen(false)}
        onPick={(def, preset) => {
          setGalleryOpen(false);
          setPickedRecipe({ def, preset });
        }}
      />

      {pickedRecipe && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
          <RecipeConfigDrawer
            libraryCollectionsOnly
            def={pickedRecipe.def}
            preset={pickedRecipe.preset}
            onCancel={() => setPickedRecipe(null)}
            onBackToGallery={() => {
              setPickedRecipe(null);
              setGalleryOpen(true);
            }}
            onAdd={async (payload) => {
              try {
                await createSectionFromGallery(payload);
                setPickedRecipe(null);
              } catch (error) {
                toast.error(error instanceof Error ? error.message : "Failed to create section");
                throw error;
              }
            }}
          />
        </div>
      )}
    </div>
  );
}
