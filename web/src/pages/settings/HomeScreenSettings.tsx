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
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import SectionEditorDrawer from "@/components/sections/SectionEditorDrawer";
import HomeLayoutTransfer from "@/components/sections/HomeLayoutTransfer";
import RecipeGalleryModal from "@/components/RecipeGallery/RecipeGalleryModal";
import RecipeConfigDrawer from "@/components/RecipeGallery/RecipeConfigDrawer";
import type { AddPayload } from "@/components/RecipeGallery/RecipeConfigDrawer";
import type { GalleryPreset, RecipeDefinition } from "@/lib/recipes";
import { fetchRecipeCatalog } from "@/lib/recipes";
import { canAddAdminOnlyRecipes, isTraktConfig } from "@/lib/sectionTypes";
import { randomUUID } from "@/lib/uuid";
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
import { v2, V2ProblemError } from "@/api/v2/request";
import { useOptionalAuth } from "@/hooks/useAuth";
import {
  useEffectiveSettings,
  useSetSettingValue,
  type SettingIdentity,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };
const HOME_PREFERENCE_KEYS = [SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS] as const;

interface RemovedSystemOverride {
  id: string;
}

interface SectionOverrideIds {
  /** The profile's saved overrides for the page. */
  savedOverrides?: SectionOverride[];
  /** An ID for an admin section the profile has no saved override for. */
  newId?: (sectionId: string) => string;
  /** The section the change being saved is to, if it is to one section. */
  changedSectionId?: string;
}

/**
 * The override set to save for one page. A change to an admin section keeps
 * the ID of the profile's saved override for that section, or gets one from
 * `newId`: the server's section source policy refuses a legacy Trakt admin
 * section's override without an ID. It also refuses a new override that
 * leaves such a section showing, so a shown one without a saved override is
 * left out and keeps its admin position, unless the change is to that
 * section; the refusal then reaches the user instead of the change silently
 * not saving. Positions are only as close to the list order as that held
 * position allows.
 */
export function buildSectionOverrides(
  sections: SettingsSectionEntry[],
  removedSystemSections: RemovedSystemOverride[] = [],
  { savedOverrides = [], newId = () => randomUUID(), changedSectionId }: SectionOverrideIds = {},
): SectionOverride[] {
  // The server resolves the last saved override for a section.
  const savedIds = new Map<string, string>();
  for (const override of savedOverrides) {
    if (override.section_id && override.id) savedIds.set(override.section_id, override.id);
  }
  const leftOut = (s: SettingsSectionEntry) =>
    !s.is_custom &&
    !s.hidden &&
    !savedIds.has(s.id) &&
    s.id !== changedSectionId &&
    isTraktConfig(s.config);
  // A section left out keeps its admin position, so the others are numbered
  // in list order around it and never on it: the server orders sections with
  // equal positions arbitrarily.
  const heldPositions = new Set(sections.filter(leftOut).map((s) => s.position));
  const overrides: SectionOverride[] = [];
  let position = 0;
  for (const s of sections) {
    if (leftOut(s)) {
      position = Math.max(position, s.position + 1);
      continue;
    }
    while (heldPositions.has(position)) position += 1;
    overrides.push({
      section_id: s.is_custom ? undefined : s.id,
      id: s.is_custom ? s.id : (savedIds.get(s.id) ?? newId(s.id)),
      position: position++,
      hidden: s.hidden,
      title: s.title,
      featured: s.featured,
      item_limit: s.item_limit,
      section_type: s.is_custom ? s.section_type : undefined,
      config: s.config,
    });
  }
  for (const section of removedSystemSections) {
    overrides.push({
      section_id: section.id,
      id: savedIds.get(section.id) ?? newId(section.id),
      removed: true,
    });
  }
  return overrides;
}

/**
 * Gives each admin section one new override ID and returns the same one on
 * later calls, so a quick second save on a page reuses the IDs of the first
 * before the saved overrides refetch.
 */
export function createOverrideIdSource(): (sectionId: string) => string {
  const ids = new Map<string, string>();
  return (sectionId) => {
    const id = ids.get(sectionId) ?? randomUUID();
    ids.set(sectionId, id);
    return id;
  };
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
    id: randomUUID(),
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

/**
 * A permission denial carries its cause in the detail: the custom-sections
 * refusal and the demo-mode gate both answer 403 permission_denied.
 */
export function sectionSaveErrorMessage(error: unknown): string {
  const detail =
    error instanceof V2ProblemError && error.problemType === "permission_denied"
      ? error.problem.detail?.trim()
      : undefined;
  return detail ? `Failed to save section changes: ${detail}` : "Failed to save section changes";
}

export default function HomeScreenSettings() {
  const { data: libraries } = useUserLibraries();
  const { data: recipeCatalog } = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: 5 * 60 * 1000,
  });
  const role = useOptionalAuth()?.user?.role;
  const { data: sectionFlags } = useQuery({
    queryKey: ["profile-section-flags"],
    queryFn: () => v2("GET /api/v2/profile/sections/flags"),
    staleTime: 5 * 60 * 1000,
  });
  const allowAdminOnlyRecipes = canAddAdminOnlyRecipes(
    role,
    sectionFlags?.allow_profile_custom_sections,
  );

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
  const homePreferences = useEffectiveSettings({ keys: HOME_PREFERENCE_KEYS });
  const saveHomePreference = useSetSettingValue();
  const hideWatchedItems =
    homePreferences.data?.[SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS]?.value === true;
  const activeSelectionValue = scopeValue;
  const activeSelectionRef = useRef(activeSelectionValue);
  const latestSaveAttemptRef = useRef(0);
  // New override IDs for admin sections on this page.
  const newOverrideIdRef = useRef(createOverrideIdSource());

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
    changedSectionId?: string,
  ) {
    if (!canEditSections) {
      return;
    }

    const selectionValueAtSave = activeSelectionValue;
    const saveAttemptId = latestSaveAttemptRef.current + 1;
    latestSaveAttemptRef.current = saveAttemptId;
    const overrides = buildSectionOverrides(sections, removedOverrides, {
      savedOverrides: rawOverridesQuery.data?.overrides,
      newId: newOverrideIdRef.current,
      changedSectionId,
    });
    saveMutation.mutate(
      {
        scope,
        library_id: libraryId ? String(libraryId) : undefined,
        overrides,
      },
      {
        onError: (error) => {
          toast.error(sectionSaveErrorMessage(error));
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
    saveOverrides(next, removedSystemSections, String(active.id));
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
    saveOverrides(next, removedSystemSections, id);
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
    saveOverrides(next, removedSystemSections, updated.id);
  }

  // Reset
  function handleReset() {
    if (!canEditSections) {
      return;
    }
    setConfirmResetOpen(true);
  }

  function handleScopeChange(value: string) {
    newOverrideIdRef.current = createOverrideIdSource();
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

  function handleHideWatchedItemsChange(enabled: boolean) {
    saveHomePreference.mutate(
      {
        key: SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS,
        value: enabled,
        identity: PROFILE_SCOPE,
      },
      { onError: () => toast.error("Failed to save Home preference") },
    );
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
            {
              onSuccess: () => toast.success("Sections reset to default"),
              onError: () => toast.error("Failed to reset section customizations"),
            },
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
        title="Home preferences"
        description="Choose how this profile's Home screen handles completed media."
      >
        <div className="flex items-center justify-between gap-4">
          <div className="space-y-0.5">
            <Label htmlFor="hide-watched-home" className="text-sm font-medium">
              Hide watched items
            </Label>
            <p className="text-muted-foreground text-[13px] leading-relaxed">
              Remove watched items from ordinary Home sections. Featured and watch-history sections
              keep them.
            </p>
          </div>
          <Switch
            id="hide-watched-home"
            checked={hideWatchedItems}
            disabled={homePreferences.isLoading || saveHomePreference.isPending}
            onCheckedChange={handleHideWatchedItemsChange}
          />
        </div>
      </SettingsGroup>

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

      <SettingsGroup
        title="Export and import"
        description="Save this profile's Home and library layouts to a file, or load one exported from another profile or server."
      >
        <HomeLayoutTransfer />
      </SettingsGroup>

      <SectionEditorDrawer
        mode="profile"
        open={drawerOpen}
        onOpenChange={setDrawerOpen}
        section={drawerSection}
        libraries={libraries ?? []}
        recipeCatalog={recipeCatalog}
        libraryScoped={scope === "library"}
        allowAdminOnlyRecipes={allowAdminOnlyRecipes}
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
            libraryScoped={scope === "library"}
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
