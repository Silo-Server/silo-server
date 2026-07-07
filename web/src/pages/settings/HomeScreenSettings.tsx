/* eslint-disable react-refresh/only-export-components */
import { useState, useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import {
  useProfileSectionOverrides,
  useProfileSectionSettings,
  useSaveProfileOverrides,
  useResetProfileOverrides,
} from "@/hooks/queries/sections";
import { useUserLibraries } from "@/hooks/queries/libraries";
import type { SettingsSectionEntry, SectionOverride } from "@/api/types";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import SectionEditorDrawer from "@/components/sections/SectionEditorDrawer";
import RecipeGalleryModal from "@/components/RecipeGallery/RecipeGalleryModal";
import RecipeConfigDrawer from "@/components/RecipeGallery/RecipeConfigDrawer";
import type { AddPayload } from "@/components/RecipeGallery/RecipeConfigDrawer";
import type { GalleryPreset, RecipeDefinition } from "@/lib/recipes";
import { fetchRecipeCatalog } from "@/lib/recipes";
import { Plus } from "lucide-react";
import {
  SectionDragOverlay,
  SortableSectionCardRow,
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
import { toast } from "sonner";

interface RemovedSystemOverride {
  id: string;
}

export function buildSectionOverrides(
  sections: SettingsSectionEntry[],
  removedSystemSections: RemovedSystemOverride[] = [],
): SectionOverride[] {
  return [
    ...sections.map((s, index) => ({
      section_id: s.is_custom ? undefined : s.id,
      id: s.is_custom ? s.id : undefined,
      position: index,
      hidden: s.hidden,
      title: s.title,
      featured: s.featured,
      item_limit: s.item_limit,
      section_type: s.is_custom ? s.section_type : undefined,
      config: s.config,
    })),
    ...removedSystemSections.map((section) => ({
      section_id: section.id,
      removed: true,
    })),
  ];
}

export function applySectionDeletion(
  sections: SettingsSectionEntry[],
  removedSystemSections: RemovedSystemOverride[],
  id: string,
): { sections: SettingsSectionEntry[]; removedSystemSections: RemovedSystemOverride[] } {
  const target = sections.find((section) => section.id === id);
  if (!target) {
    return { sections, removedSystemSections };
  }

  const nextSections = sections.filter((section) => section.id !== id);
  if (target.is_custom) {
    return { sections: nextSections, removedSystemSections };
  }

  if (removedSystemSections.some((section) => section.id === id)) {
    return { sections: nextSections, removedSystemSections };
  }

  return {
    sections: nextSections,
    removedSystemSections: [...removedSystemSections, { id }],
  };
}

export function hydrateRemovedSystemSections(
  overrides: SectionOverride[] = [],
): RemovedSystemOverride[] {
  return Array.from(
    new Set(
      overrides
        .filter((override) => override.removed && Boolean(override.section_id))
        .map((override) => override.section_id as string),
    ),
  ).map((id) => ({ id }));
}

interface ReadyQueryState {
  isSuccess: boolean;
  isError: boolean;
}

export function canMutateSectionSettings(
  settingsQuery?: ReadyQueryState,
  rawOverridesQuery?: ReadyQueryState,
): boolean {
  return Boolean(
    settingsQuery?.isSuccess &&
    !settingsQuery.isError &&
    rawOverridesQuery?.isSuccess &&
    !rawOverridesQuery.isError,
  );
}

export function shouldRestoreSelectionState(
  currentSelectionValue: string,
  selectionValueAtSave: string,
): boolean {
  return currentSelectionValue === selectionValueAtSave;
}

export function shouldRestoreLatestSaveFailure(
  currentSelectionValue: string,
  selectionValueAtSave: string,
  latestAttemptId: number,
  failedAttemptId: number,
): boolean {
  return (
    shouldRestoreSelectionState(currentSelectionValue, selectionValueAtSave) &&
    latestAttemptId === failedAttemptId
  );
}

function toEditableSection(section: SettingsSectionEntry): EditableSectionViewModel {
  return {
    id: section.id,
    title: section.title,
    sectionType: section.section_type,
    itemLimit: section.item_limit,
    featured: section.featured,
    hidden: section.hidden,
    isCustom: section.is_custom,
    config: section.config,
  };
}

export function buildProfileGallerySection(
  payload: AddPayload,
  position: number,
): SettingsSectionEntry {
  return {
    id: crypto.randomUUID(),
    section_type: payload.section_type,
    title: payload.title,
    featured: payload.featured,
    item_limit: payload.item_limit,
    hidden: false,
    is_custom: true,
    customized: true,
    position,
    config: payload.config,
  };
}

export default function HomeScreenSettings() {
  const { data: libraries } = useUserLibraries();
  const { data: recipeCatalog } = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: 5 * 60 * 1000,
  });

  // Scope state
  const [scopeValue, setScopeValue] = useState("home");
  const scope = scopeValue === "home" ? "home" : "library";
  const libraryId = scopeValue.startsWith("library:")
    ? Number(scopeValue.split(":")[1])
    : undefined;

  // Section data
  const settingsQuery = useProfileSectionSettings(scope, libraryId);
  const rawOverridesQuery = useProfileSectionOverrides(scope, libraryId);
  const saveMutation = useSaveProfileOverrides();
  const resetMutation = useResetProfileOverrides();
  const canEditSections = canMutateSectionSettings(settingsQuery, rawOverridesQuery);
  const activeSelectionValue = scopeValue;
  const activeSelectionRef = useRef(activeSelectionValue);
  const latestSaveAttemptRef = useRef(0);

  // DnD state
  const [activeId, setActiveId] = useState<string | null>(null);
  const [orderedSections, setOrderedSections] = useState<SettingsSectionEntry[]>([]);
  const [removedSystemSections, setRemovedSystemSections] = useState<RemovedSystemOverride[]>([]);

  // Drawer state
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerSection, setDrawerSection] = useState<SettingsSectionEntry | null>(null);
  const [galleryOpen, setGalleryOpen] = useState(false);
  const [pickedRecipe, setPickedRecipe] = useState<{
    def: RecipeDefinition;
    preset: GalleryPreset;
  } | null>(null);

  // Reset confirm state
  const [confirmResetOpen, setConfirmResetOpen] = useState(false);
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);
  const [pendingDeleteSection, setPendingDeleteSection] = useState<SettingsSectionEntry | null>(
    null,
  );

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  // Sync from server
  useEffect(() => {
    activeSelectionRef.current = activeSelectionValue;
  }, [activeSelectionValue]);

  useEffect(() => {
    if (settingsQuery.data?.sections) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setOrderedSections(settingsQuery.data.sections);
    }
  }, [settingsQuery.data?.sections]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setRemovedSystemSections(hydrateRemovedSystemSections(rawOverridesQuery.data?.overrides));
  }, [rawOverridesQuery.data?.overrides]);

  // Save helper
  function saveOverrides(
    sections: SettingsSectionEntry[],
    removedOverrides: RemovedSystemOverride[] = removedSystemSections,
  ) {
    if (!canEditSections) {
      return;
    }

    const selectionValueAtSave = activeSelectionValue;
    const saveAttemptId = latestSaveAttemptRef.current + 1;
    latestSaveAttemptRef.current = saveAttemptId;
    const overrides = buildSectionOverrides(sections, removedOverrides);
    saveMutation.mutate(
      {
        scope,
        library_id: libraryId ? String(libraryId) : undefined,
        overrides,
      },
      {
        onError: () => {
          toast.error("Failed to save section changes");
          if (
            !shouldRestoreLatestSaveFailure(
              activeSelectionRef.current,
              selectionValueAtSave,
              latestSaveAttemptRef.current,
              saveAttemptId,
            )
          ) {
            return;
          }

          if (settingsQuery.data?.sections) setOrderedSections(settingsQuery.data.sections);
          setRemovedSystemSections(hydrateRemovedSystemSections(rawOverridesQuery.data?.overrides));
        },
      },
    );
  }

  // DnD handlers
  function handleDragStart(event: DragStartEvent) {
    if (!canEditSections) {
      return;
    }
    setActiveId(event.active.id as string);
  }

  function handleDragEnd(event: DragEndEvent) {
    if (!canEditSections) {
      setActiveId(null);
      return;
    }
    setActiveId(null);
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = orderedSections.findIndex((s) => s.id === active.id);
    const newIndex = orderedSections.findIndex((s) => s.id === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    const next = arrayMove(orderedSections, oldIndex, newIndex);
    setOrderedSections(next);
    saveOverrides(next);
  }

  function handleDragCancel() {
    setActiveId(null);
  }

  const activeSection = activeId ? (orderedSections.find((s) => s.id === activeId) ?? null) : null;

  // Toggle visibility
  function handleToggleHidden(id: string) {
    if (!canEditSections) {
      return;
    }
    const next = orderedSections.map((s) => (s.id === id ? { ...s, hidden: !s.hidden } : s));
    setOrderedSections(next);
    saveOverrides(next);
  }

  function handleRequestDelete(section: SettingsSectionEntry) {
    if (!canEditSections) {
      return;
    }
    setPendingDeleteSection(section);
    setConfirmDeleteOpen(true);
  }

  function handleConfirmDelete() {
    if (!pendingDeleteSection || !canEditSections) {
      return;
    }

    const nextState = applySectionDeletion(
      orderedSections,
      removedSystemSections,
      pendingDeleteSection.id,
    );
    setOrderedSections(nextState.sections);
    setRemovedSystemSections(nextState.removedSystemSections);
    if (activeId === pendingDeleteSection.id) {
      setActiveId(null);
    }
    setConfirmDeleteOpen(false);
    setPendingDeleteSection(null);
    saveOverrides(nextState.sections, nextState.removedSystemSections);
  }

  function handleDeleteDialogChange(open: boolean) {
    setConfirmDeleteOpen(open);
    if (!open) {
      setPendingDeleteSection(null);
    }
  }

  function handleOpenAdd() {
    if (!canEditSections) {
      return;
    }
    setDrawerSection(null);
    setDrawerOpen(true);
  }

  function handleOpenEdit(section: SettingsSectionEntry) {
    if (!canEditSections) {
      return;
    }
    setDrawerSection(section);
    setDrawerOpen(true);
  }

  function handleDrawerSave(updated: SettingsSectionEntry) {
    if (!canEditSections) {
      return;
    }
    let next: SettingsSectionEntry[];
    const existing = orderedSections.find((s) => s.id === updated.id);
    if (existing) {
      next = orderedSections.map((s) => (s.id === updated.id ? updated : s));
    } else {
      next = [...orderedSections, { ...updated, position: orderedSections.length }];
    }
    setOrderedSections(next);
    saveOverrides(next);
  }

  // Reset
  function handleReset() {
    if (!canEditSections) {
      return;
    }
    setConfirmResetOpen(true);
  }

  function handleScopeChange(value: string) {
    setOrderedSections([]);
    setRemovedSystemSections([]);
    setActiveId(null);
    setConfirmResetOpen(false);
    setConfirmDeleteOpen(false);
    setPendingDeleteSection(null);
    setDrawerOpen(false);
    setDrawerSection(null);
    setGalleryOpen(false);
    setPickedRecipe(null);
    setScopeValue(value);
  }

  function handleAddFromGallery(payload: AddPayload) {
    if (!canEditSections) {
      return;
    }
    const next = [...orderedSections, buildProfileGallerySection(payload, orderedSections.length)];
    setOrderedSections(next);
    setPickedRecipe(null);
    saveOverrides(next);
  }

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Home screen</h2>
        <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
          Choose a scope, then arrange the sections that appear on that screen.
        </p>
      </div>

      <ConfirmDialog
        open={confirmResetOpen}
        onOpenChange={(open) => {
          if (!open) setConfirmResetOpen(false);
        }}
        title="Reset section customizations"
        description="Reset all section customizations to defaults? This action cannot be undone."
        confirmLabel="Reset"
        variant="default"
        onConfirm={() => {
          setConfirmResetOpen(false);
          resetMutation.mutate(
            { scope, libraryId: libraryId ? String(libraryId) : undefined },
            { onSuccess: () => toast.success("Sections reset to default") },
          );
        }}
      />

      <ConfirmDialog
        open={confirmDeleteOpen}
        onOpenChange={handleDeleteDialogChange}
        title={pendingDeleteSection?.is_custom ? "Delete custom section?" : "Remove section?"}
        description={
          pendingDeleteSection?.is_custom
            ? "Delete this custom section?"
            : "Remove this section from your home screen?"
        }
        confirmLabel={pendingDeleteSection?.is_custom ? "Delete" : "Remove"}
        variant="destructive"
        onConfirm={handleConfirmDelete}
      />

      <SettingsGroup
        title="Scope"
        description="Pick the home screen or library-specific view you want to customize."
      >
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="space-y-0.5">
            <Label className="text-sm font-medium">Editing scope</Label>
            <p className="text-muted-foreground text-[13px] leading-relaxed">
              Changes apply only to the selected home screen.
            </p>
          </div>
          <Select value={scopeValue} onValueChange={handleScopeChange}>
            <SelectTrigger className="w-full sm:w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="home">Home</SelectItem>
              {libraries?.map((lib) => (
                <SelectItem key={lib.id} value={`library:${lib.id}`}>
                  {lib.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </SettingsGroup>

      <SettingsGroup
        title="Sections"
        description="Add, reorder, or hide sections. Drag to change order."
      >
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => setGalleryOpen(true)}
            disabled={!canEditSections}
          >
            <Plus className="mr-1 h-4 w-4" /> Add from Gallery
          </Button>
          <Button size="sm" onClick={handleOpenAdd} disabled={!canEditSections}>
            <Plus className="mr-1 h-4 w-4" /> Add Section
          </Button>
          <Button size="sm" variant="outline" onClick={handleReset} disabled={!canEditSections}>
            Reset to Default
          </Button>
          <Badge variant="secondary" className="ml-auto">
            {orderedSections.length} sections
          </Badge>
        </div>
        {!canEditSections ? (
          <p className="text-muted-foreground text-[13px]">
            {rawOverridesQuery.isError
              ? "Saved section state failed to load. Editing is disabled."
              : "Loading saved section state before section changes are enabled."}
          </p>
        ) : null}

        <DndContext
          sensors={canEditSections ? sensors : []}
          collisionDetection={closestCenter}
          onDragStart={handleDragStart}
          onDragEnd={handleDragEnd}
          onDragCancel={handleDragCancel}
        >
          <SortableContext
            items={orderedSections.map((s) => s.id)}
            strategy={verticalListSortingStrategy}
          >
            <div className="space-y-2">
              {orderedSections.map((section) => (
                <SortableSectionCardRow
                  key={section.id}
                  section={toEditableSection(section)}
                  catalog={recipeCatalog}
                  onToggleHidden={() => handleToggleHidden(section.id)}
                  onEdit={() => handleOpenEdit(section)}
                  onDelete={() => handleRequestDelete(section)}
                  disabled={!canEditSections}
                />
              ))}
              {orderedSections.length === 0 && (
                <div className="surface-panel-subtle text-muted-foreground rounded-[1.2rem] py-8 text-center text-sm">
                  No sections configured.
                </div>
              )}
            </div>
          </SortableContext>
          <DragOverlay>
            {activeSection ? (
              <SectionDragOverlay
                section={toEditableSection(activeSection)}
                catalog={recipeCatalog}
              />
            ) : null}
          </DragOverlay>
        </DndContext>
      </SettingsGroup>

      <SectionEditorDrawer
        mode="profile"
        open={drawerOpen}
        onOpenChange={setDrawerOpen}
        section={drawerSection}
        libraries={libraries ?? []}
        recipeCatalog={recipeCatalog}
        onSave={handleDrawerSave}
      />

      <RecipeGalleryModal
        open={galleryOpen}
        onClose={() => setGalleryOpen(false)}
        hideAdminOnly
        onPick={(def, preset) => {
          setGalleryOpen(false);
          setPickedRecipe({ def, preset });
        }}
      />

      {pickedRecipe ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
          <RecipeConfigDrawer
            def={pickedRecipe.def}
            preset={pickedRecipe.preset}
            showBulkApply={false}
            showEnabled={false}
            onCancel={() => setPickedRecipe(null)}
            onBackToGallery={() => {
              setPickedRecipe(null);
              setGalleryOpen(true);
            }}
            onAdd={handleAddFromGallery}
          />
        </div>
      ) : null}
    </div>
  );
}
