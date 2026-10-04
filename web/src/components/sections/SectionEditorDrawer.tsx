import { useEffect, useMemo, useState } from "react";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import CollectionRulesEditor from "@/components/collections/CollectionRulesEditor";
import { CollectionSearchableSelect } from "@/components/CollectionSearchableSelect";
import RecipeParamFields from "@/components/RecipeGallery/RecipeParamFields";
import {
  FILTER_SECTION_TYPES,
  fallbackSectionTypes,
  filterRecipeCatalog,
  sectionTypeLabel,
} from "@/lib/sectionTypes";
import { buildAdminSectionPayload, buildProfileSectionSaveEntry } from "@/lib/homeRows/payloads";
import {
  matchRecipePreset,
  type Category,
  type RecipeCatalogResponse,
  type RecipeDefinition,
} from "@/lib/recipes";
import {
  queryDefinitionFromSectionConfig,
  type PageSectionConfig,
  type QueryDefinition,
  type SettingsSectionEntry,
} from "@/api/types";
import { useAllUserCollections } from "@/hooks/queries/useAllUserCollections";

const CATEGORY_LABELS: Record<Category, string> = {
  library_staples: "Library",
  personalized: "For You",
  discovery: "Discovery",
  editorial: "Editorial",
  seasonal: "Seasonal",
  mood: "Mood",
  social: "Social",
  hand_picked: "Hand Picked",
  custom: "Custom",
};

function getCollectionId(config?: Record<string, unknown>): string {
  const userValue = config?.user_collection_id;
  if (typeof userValue === "string" && userValue) return userValue;
  const libraryValue = config?.library_collection_id;
  return typeof libraryValue === "string" ? libraryValue : "";
}

function isLegacyFilterType(type: string): boolean {
  return FILTER_SECTION_TYPES.has(type);
}

function lookupRecipe(
  catalog: RecipeCatalogResponse | undefined,
  type: string,
): RecipeDefinition | undefined {
  if (!catalog) return undefined;
  for (const defs of Object.values(catalog.categories)) {
    const found = defs?.find((def) => def.type === type);
    if (found) return found;
  }
  return undefined;
}

/** The preset labelling a type the pickable list no longer offers, e.g. an admin-only section a profile already owns. */
function matchRecipePresetFor(
  catalog: RecipeCatalogResponse | undefined,
  type: string,
  params: Record<string, unknown>,
) {
  const definition = lookupRecipe(catalog, type);
  return definition ? matchRecipePreset(definition, params) : undefined;
}

function parseRecipeParams(config: unknown): Record<string, unknown> {
  if (config && typeof config === "object" && !Array.isArray(config)) {
    return { ...(config as Record<string, unknown>) };
  }
  return {};
}

type ProfileDrawerProps = {
  mode: "profile";
  open: boolean;
  onOpenChange: (open: boolean) => void;
  section: SettingsSectionEntry | null;
  libraries: Array<{ id: number; name: string }>;
  recipeCatalog?: RecipeCatalogResponse;
  /** The profile is editing a library page's sections; see RecipeParamFieldsProps. */
  libraryScoped?: boolean;
  /** False when the server refuses admin-only recipes for this profile; defaults to true. */
  allowAdminOnlyRecipes?: boolean;
  onSave: (section: SettingsSectionEntry) => void | Promise<void>;
};

type AdminDrawerProps = {
  mode: "admin";
  open: boolean;
  onOpenChange: (open: boolean) => void;
  section: PageSectionConfig | null;
  scope: string;
  currentLibraryId: number | null;
  libraries: Array<{ id: number; name: string }>;
  recipeCatalog?: RecipeCatalogResponse;
  isSubmitting?: boolean;
  conflict?: boolean;
  onReload?: () => void;
  onSave: (section: Partial<PageSectionConfig> & { id?: string }) => void;
};

type SectionEditorDrawerProps = ProfileDrawerProps | AdminDrawerProps;

export default function SectionEditorDrawer(props: SectionEditorDrawerProps) {
  const isProfile = props.mode === "profile";
  const isEdit = props.section !== null;
  const lockSectionType = isProfile && props.section !== null && !props.section.is_custom;
  const [profileSubmitting, setProfileSubmitting] = useState(false);
  const isSubmitting = props.mode === "admin" ? props.isSubmitting : profileSubmitting;
  const [sectionType, setSectionType] = useState("recently_added");
  const [title, setTitle] = useState("");
  const [itemLimit, setItemLimit] = useState(20);
  const [featured, setFeatured] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [queryDefinition, setQueryDefinition] = useState<QueryDefinition>(
    queryDefinitionFromSectionConfig(),
  );
  const [selectedCollectionId, setSelectedCollectionId] = useState("");
  const [recipeParams, setRecipeParams] = useState<Record<string, unknown>>({});
  const { collections: allCollections, isLoading: collectionsLoading } = useAllUserCollections();
  const collections = useMemo(
    () =>
      isProfile
        ? allCollections
        : allCollections.filter((collection) => collection.source === "library"),
    [allCollections, isProfile],
  );

  const allowAdminOnlyRecipes = props.mode === "admin" || props.allowAdminOnlyRecipes !== false;
  const pickableCatalog = useMemo(
    () => filterRecipeCatalog(props.recipeCatalog, allowAdminOnlyRecipes),
    [props.recipeCatalog, allowAdminOnlyRecipes],
  );
  const pickableFallbackTypes = fallbackSectionTypes(allowAdminOnlyRecipes);
  const catalogCategories = useMemo(
    () =>
      pickableCatalog
        ? (Object.keys(pickableCatalog.categories) as Category[]).filter(
            (category) => (pickableCatalog.categories[category]?.length ?? 0) > 0,
          )
        : [],
    [pickableCatalog],
  );
  const recipeDef = !isLegacyFilterType(sectionType)
    ? lookupRecipe(props.recipeCatalog, sectionType)
    : undefined;
  const isKnownRecipe = Boolean(recipeDef);
  const showCollectionPicker = sectionType === "collection";
  const showLegacyFilter = isLegacyFilterType(sectionType);
  const showRecipeParams = !showCollectionPicker && !showLegacyFilter && isKnownRecipe;

  useEffect(() => {
    if (!props.open) return;
    if (props.section) {
      setSectionType(props.section.section_type);
      setTitle(props.section.title);
      setItemLimit(props.section.item_limit);
      setFeatured(props.section.featured);
      setEnabled("enabled" in props.section ? Boolean(props.section.enabled) : true);
      setQueryDefinition(queryDefinitionFromSectionConfig(props.section.config));
      setSelectedCollectionId(getCollectionId(props.section.config));
      setRecipeParams(parseRecipeParams(props.section.config));
    } else {
      setSectionType("recently_added");
      setTitle("");
      setItemLimit(20);
      setFeatured(false);
      setEnabled(true);
      setQueryDefinition(queryDefinitionFromSectionConfig());
      setSelectedCollectionId("");
      setRecipeParams({});
    }
  }, [props.open, props.section]);

  useEffect(() => {
    if (!props.open || showCollectionPicker || showLegacyFilter) return;
    if (Object.keys(recipeParams).length > 0) return;
    const seed = recipeDef?.presets[0]?.default_params;
    if (seed && Object.keys(seed).length > 0) {
      setRecipeParams({ ...seed });
    }
  }, [props.open, showCollectionPicker, showLegacyFilter, recipeDef, recipeParams]);

  async function handleSave() {
    if (isSubmitting) return;
    if (props.mode === "profile") {
      setProfileSubmitting(true);
      try {
        await props.onSave(
          buildProfileSectionSaveEntry({
            section: props.section,
            sectionType,
            title,
            itemLimit,
            featured,
            queryDefinition,
            selectedCollectionId,
            recipeParams,
            collections,
          }),
        );
        props.onOpenChange(false);
      } catch {
        // The owner reports the error; retain the draft for retry.
      } finally {
        setProfileSubmitting(false);
      }
    } else {
      props.onSave(
        buildAdminSectionPayload({
          section: props.section,
          scope: props.scope,
          currentLibraryId: props.currentLibraryId,
          sectionType,
          title,
          itemLimit,
          featured,
          enabled,
          queryDefinition,
          selectedCollectionId,
          recipeParams,
          collections,
        }),
      );
    }
  }

  const saveDisabled =
    (showCollectionPicker && !selectedCollectionId) ||
    (props.mode === "admin" && props.scope === "library" && props.currentLibraryId == null);

  return (
    <Sheet
      open={props.open}
      onOpenChange={(open) => {
        if (!isSubmitting) props.onOpenChange(open);
      }}
    >
      <SheetContent side="right" className="overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{isEdit ? "Edit Section" : "Add Section"}</SheetTitle>
          <SheetDescription>
            {isEdit ? "Modify this section's settings" : "Configure a new section."}
          </SheetDescription>
        </SheetHeader>

        <div className="space-y-4 px-4">
          <div className="space-y-2">
            <Label>Section Type</Label>
            {lockSectionType ? (
              <div className="bg-muted text-muted-foreground rounded-md px-3 py-2 text-sm">
                {sectionTypeLabel(sectionType)}
              </div>
            ) : (
              <Select
                value={sectionType}
                onValueChange={(value) => {
                  setSectionType(value);
                  setRecipeParams({});
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {!lookupRecipe(pickableCatalog, sectionType) &&
                  sectionType &&
                  (catalogCategories.length > 0 ||
                    !pickableFallbackTypes.some((type) => type.value === sectionType)) ? (
                    <SelectItem value={sectionType}>
                      {matchRecipePresetFor(props.recipeCatalog, sectionType, recipeParams)
                        ?.display_name ?? sectionTypeLabel(sectionType)}
                    </SelectItem>
                  ) : null}
                  {catalogCategories.length > 0
                    ? catalogCategories.map((category) => (
                        <SelectGroup key={category}>
                          <SelectLabel>{CATEGORY_LABELS[category] ?? category}</SelectLabel>
                          {(pickableCatalog?.categories[category] ?? []).map((definition) => {
                            // The selected type is labelled by the preset its
                            // params match, so a weekly trending section reads
                            // "TMDB Trending This Week" rather than the first preset.
                            const preset =
                              definition.type === sectionType
                                ? matchRecipePreset(definition, recipeParams)
                                : definition.presets[0];
                            const label = preset?.display_name ?? definition.type;
                            const icon = preset?.icon;
                            return (
                              <SelectItem key={definition.type} value={definition.type}>
                                {icon ? `${icon} ${label}` : label}
                              </SelectItem>
                            );
                          })}
                        </SelectGroup>
                      ))
                    : pickableFallbackTypes.map((type) => (
                        <SelectItem key={type.value} value={type.value}>
                          {type.label}
                        </SelectItem>
                      ))}
                </SelectContent>
              </Select>
            )}
            {!lockSectionType && !allowAdminOnlyRecipes ? (
              <p className="text-muted-foreground text-xs">
                Some section types, such as custom filters, are available only to admins on this
                server.
              </p>
            ) : null}
          </div>

          <div className="space-y-2">
            <Label>Title</Label>
            <Input
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder={sectionTypeLabel(sectionType)}
            />
          </div>

          <div className="space-y-2">
            <Label>Item Limit</Label>
            <Input
              type="number"
              value={itemLimit}
              onChange={(event) => setItemLimit(Number(event.target.value))}
              min={1}
              max={100}
            />
          </div>

          <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-3">
            <div className="space-y-1">
              <Label htmlFor="section-featured">Featured</Label>
              <p className="text-muted-foreground text-sm">
                Use this section as the hero banner on the home screen.
              </p>
            </div>
            <Switch id="section-featured" checked={featured} onCheckedChange={setFeatured} />
          </div>

          {props.mode === "admin" ? (
            <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-3">
              <Label htmlFor="section-enabled">Enabled</Label>
              <Switch id="section-enabled" checked={enabled} onCheckedChange={setEnabled} />
            </div>
          ) : null}

          <div className="border-border border-t" />

          {showCollectionPicker ? (
            <div className="space-y-2">
              <Label>Collection</Label>
              <CollectionSearchableSelect
                options={collections}
                value={selectedCollectionId}
                onChange={setSelectedCollectionId}
                disabled={collectionsLoading}
                isLoading={collectionsLoading}
              />
            </div>
          ) : null}

          {/* Rows resolve per viewer, so the server accepts personalized rules and sorts. */}
          {showLegacyFilter ? (
            <CollectionRulesEditor
              value={queryDefinition}
              onChange={setQueryDefinition}
              libraries={props.libraries}
              showMediaScopeSelector
              allowLibrarySelection
              allowPersonalizedFilters
              allowPersonalizedSorts
            />
          ) : null}

          {showRecipeParams && recipeDef ? (
            <RecipeParamFields
              def={recipeDef}
              params={recipeParams}
              onChange={setRecipeParams}
              libraryScoped={
                props.mode === "admin" ? props.scope === "library" : Boolean(props.libraryScoped)
              }
              libraries={props.mode === "admin" ? props.libraries : undefined}
            />
          ) : null}
        </div>

        {props.mode === "admin" && props.conflict && (
          <div role="alert" className="px-6">
            <p>
              This section changed. Your draft is preserved. Reload to discard it and edit the
              current section.
            </p>
            <Button variant="outline" onClick={props.onReload}>
              Reload section
            </Button>
          </div>
        )}
        <SheetFooter>
          <Button
            variant="outline"
            disabled={isSubmitting}
            onClick={() => props.onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button
            onClick={handleSave}
            disabled={saveDisabled || isSubmitting || (props.mode === "admin" && props.conflict)}
          >
            {isEdit ? "Save" : "Add Section"}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

export {
  buildAdminSectionPayload,
  buildProfileSectionSaveEntry,
  buildProfileSectionSaveEntry as buildSectionSaveEntry,
};
