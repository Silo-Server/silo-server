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
import type { SettingsSectionEntry } from "@/api/types";
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
import { canAddAdminOnlyRecipes } from "@/lib/sectionTypes";
import { randomUUID } from "@/lib/uuid";
import {
  applySectionDeletion,
  buildSectionOverrides,
  canMutateSectionSettings,
  createOverrideIdSource,
  hydrateRemovedSystemSections,
  shouldRestoreLatestSaveFailure,
  type RemovedSystemOverride,
} from "@/lib/sectionOverrides";
import { Plus } from "lucide-react";
import { SectionOrderList } from "@/components/sections/SectionOrderList";
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

  function handleMove(next: SettingsSectionEntry[], movedId: string) {
    if (!canEditSections) {
      return;
    }
    setOrderedSections(next);
    saveOverrides(next, removedSystemSections, movedId);
  }

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

        <SectionOrderList
          key={scopeValue}
          sections={orderedSections}
          catalog={recipeCatalog}
          disabled={!canEditSections}
          onMove={handleMove}
          onToggleHidden={(section) => handleToggleHidden(section.id)}
          onEdit={handleOpenEdit}
          onDelete={handleRequestDelete}
        />
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
