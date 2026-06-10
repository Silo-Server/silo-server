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
import FilterEasyMode from "@/components/FilterEasyMode/FilterEasyMode";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { CollectionSearchableSelect } from "@/components/CollectionSearchableSelect";
import RecipeParamFields from "@/components/RecipeGallery/RecipeParamFields";
import { SECTION_TYPES, FILTER_SECTION_TYPES, sectionTypeLabel } from "@/lib/sectionTypes";
import type { Category, RecipeCatalogResponse, RecipeDefinition } from "@/lib/recipes";
import {
  queryDefinitionFromSectionConfig,
  queryDefinitionToSectionConfig,
  type PageSectionConfig,
  type QueryDefinition,
  type SettingsSectionEntry,
} from "@/api/types";
import {
  useAllUserCollections,
  type CollectionOption,
} from "@/hooks/queries/useAllUserCollections";

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

function parseRecipeParams(config: unknown): Record<string, unknown> {
  if (config && typeof config === "object" && !Array.isArray(config)) {
    return { ...(config as Record<string, unknown>) };
  }
  return {};
}

function preserveGeneratedSectionMetadata(
  existingConfig: Record<string, unknown> | undefined,
  nextConfig: Record<string, unknown>,
): Record<string, unknown> {
  if (!existingConfig) {
    return nextConfig;
  }

  const merged = { ...nextConfig };
  if (typeof existingConfig.generated_source === "string" && existingConfig.generated_source) {
    merged.generated_source = existingConfig.generated_source;
  }
  if (
    typeof existingConfig.filter_library_id === "number" &&
    Number.isInteger(existingConfig.filter_library_id)
  ) {
    merged.filter_library_id = existingConfig.filter_library_id;
  }
  return merged;
}

interface BuildProfileSectionSaveEntryInput {
  section: SettingsSectionEntry | null;
  sectionType: string;
  title: string;
  itemLimit: number;
  featured: boolean;
  queryDefinition: QueryDefinition;
  selectedCollectionId: string;
  recipeParams?: Record<string, unknown>;
  collections?: CollectionOption[];
}

export function buildProfileSectionSaveEntry({
  section,
  sectionType,
  title,
  itemLimit,
  featured,
  queryDefinition,
  selectedCollectionId,
  recipeParams,
  collections,
}: BuildProfileSectionSaveEntryInput): SettingsSectionEntry {
  let config: Record<string, unknown>;
  if (sectionType === "collection") {
    const selected = collections?.find((collection) => collection.id === selectedCollectionId);
    config =
      selected?.source === "user"
        ? { user_collection_id: selectedCollectionId }
        : { library_collection_id: selectedCollectionId };
  } else if (isLegacyFilterType(sectionType)) {
    config = preserveGeneratedSectionMetadata(
      section?.config,
      queryDefinitionToSectionConfig(queryDefinition),
    );
  } else {
    config = preserveGeneratedSectionMetadata(section?.config, recipeParams ?? {});
  }

  return {
    id: section?.id ?? crypto.randomUUID(),
    section_type: sectionType,
    title: title || sectionTypeLabel(sectionType),
    featured,
    item_limit: itemLimit,
    hidden: section?.hidden ?? false,
    is_custom: section?.is_custom ?? true,
    customized: section?.customized ?? false,
    position: section?.position ?? 0,
    config,
  };
}

interface BuildAdminSectionPayloadInput {
  section: PageSectionConfig | null;
  scope: string;
  currentLibraryId: number | null;
  sectionType: string;
  title: string;
  itemLimit: number;
  featured: boolean;
  enabled: boolean;
  queryDefinition: QueryDefinition;
  selectedCollectionId: string;
  recipeParams?: Record<string, unknown>;
  collections?: CollectionOption[];
}

export function buildAdminSectionPayload({
  section,
  scope,
  currentLibraryId,
  sectionType,
  title,
  itemLimit,
  featured,
  enabled,
  queryDefinition,
  selectedCollectionId,
  recipeParams,
  collections,
}: BuildAdminSectionPayloadInput): Partial<PageSectionConfig> & { id?: string } {
  let config: Record<string, unknown>;
  if (sectionType === "collection") {
    const selected = collections?.find((collection) => collection.id === selectedCollectionId);
    config =
      selected?.source === "user"
        ? { user_collection_id: selectedCollectionId }
        : { library_collection_id: selectedCollectionId };
  } else if (isLegacyFilterType(sectionType)) {
    config = queryDefinitionToSectionConfig(queryDefinition);
  } else {
    config = recipeParams ?? {};
  }

  const safeTitle = title.trim() || sectionTypeLabel(sectionType);

  return {
    ...(section ? { id: section.id } : {}),
    scope,
    ...(scope === "library" && currentLibraryId != null ? { library_id: currentLibraryId } : {}),
    title: safeTitle,
    section_type: sectionType,
    item_limit: itemLimit,
    featured,
    enabled,
    config,
  };
}

type ProfileDrawerProps = {
  mode: "profile";
  open: boolean;
  onOpenChange: (open: boolean) => void;
  section: SettingsSectionEntry | null;
  libraries: Array<{ id: number; name: string }>;
  recipeCatalog?: RecipeCatalogResponse;
  onSave: (section: SettingsSectionEntry) => void;
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
  onSave: (section: Partial<PageSectionConfig> & { id?: string }) => void;
};

type SectionEditorDrawerProps = ProfileDrawerProps | AdminDrawerProps;

export default function SectionEditorDrawer(props: SectionEditorDrawerProps) {
  const isProfile = props.mode === "profile";
  const isEdit = props.section !== null;
  const lockSectionType = isProfile && props.section !== null && !props.section.is_custom;
  const isSubmitting = props.mode === "admin" ? props.isSubmitting : false;
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
  const [filterMode, setFilterMode] = useState<"easy" | "advanced">("easy");
  const { collections, isLoading: collectionsLoading } = useAllUserCollections();

  const catalogCategories = useMemo(
    () =>
      props.recipeCatalog
        ? (Object.keys(props.recipeCatalog.categories) as Category[]).filter(
            (category) => (props.recipeCatalog?.categories[category]?.length ?? 0) > 0,
          )
        : [],
    [props.recipeCatalog],
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
    if (!props.open) return;
    const cfg = props.section
      ? queryDefinitionFromSectionConfig(props.section.config)
      : queryDefinitionFromSectionConfig();
    const easyCompatible =
      cfg.groups.length <= 1 && (cfg.match === "all" || cfg.groups.length === 0);
    setFilterMode(easyCompatible ? "easy" : "advanced");
  }, [props.open, props.section]);

  useEffect(() => {
    if (!props.open || showCollectionPicker || showLegacyFilter) return;
    if (Object.keys(recipeParams).length > 0) return;
    const seed = recipeDef?.presets[0]?.default_params;
    if (seed && Object.keys(seed).length > 0) {
      setRecipeParams({ ...seed });
    }
  }, [props.open, showCollectionPicker, showLegacyFilter, recipeDef, recipeParams]);

  function handleSave() {
    if (props.mode === "profile") {
      props.onSave(
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
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
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
                  {!lookupRecipe(props.recipeCatalog, sectionType) &&
                  sectionType &&
                  (catalogCategories.length > 0 ||
                    !SECTION_TYPES.some((type) => type.value === sectionType)) ? (
                    <SelectItem value={sectionType}>{sectionTypeLabel(sectionType)}</SelectItem>
                  ) : null}
                  {catalogCategories.length > 0
                    ? catalogCategories.map((category) => (
                        <SelectGroup key={category}>
                          <SelectLabel>{CATEGORY_LABELS[category] ?? category}</SelectLabel>
                          {(props.recipeCatalog?.categories[category] ?? []).map((definition) => {
                            const label = definition.presets[0]?.display_name ?? definition.type;
                            const icon = definition.presets[0]?.icon;
                            return (
                              <SelectItem key={definition.type} value={definition.type}>
                                {icon ? `${icon} ${label}` : label}
                              </SelectItem>
                            );
                          })}
                        </SelectGroup>
                      ))
                    : SECTION_TYPES.map((type) => (
                        <SelectItem key={type.value} value={type.value}>
                          {type.label}
                        </SelectItem>
                      ))}
                </SelectContent>
              </Select>
            )}
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

          {showLegacyFilter ? (
            <div className="space-y-4">
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label>Media Scope</Label>
                  <Select
                    value={queryDefinition.media_scope ?? "all"}
                    onValueChange={(value) =>
                      setQueryDefinition({
                        ...queryDefinition,
                        media_scope:
                          value === "all"
                            ? undefined
                            : (value as "movie" | "series" | "episode" | "audiobook" | "ebook"),
                      })
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">All Media</SelectItem>
                      <SelectItem value="movie">Movies</SelectItem>
                      <SelectItem value="series">Series</SelectItem>
                      <SelectItem value="episode">Episodes</SelectItem>
                      <SelectItem value="audiobook">Audiobooks</SelectItem>
                      <SelectItem value="ebook">Ebooks</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label>Libraries</Label>
                  <LibraryMultiSelect
                    libraries={props.libraries}
                    value={queryDefinition.library_ids}
                    onChange={(libraryIds) =>
                      setQueryDefinition({ ...queryDefinition, library_ids: libraryIds })
                    }
                  />
                </div>
              </div>
              <div className="space-y-2">
                <Label>Filter Rules</Label>
                <div className="mb-3 flex items-center gap-1 text-xs">
                  <button
                    type="button"
                    onClick={() => setFilterMode("easy")}
                    className={`rounded px-2 py-0.5 ${filterMode === "easy" ? "bg-indigo-500 text-white" : "bg-white/5"}`}
                    aria-pressed={filterMode === "easy"}
                  >
                    Easy
                  </button>
                  <button
                    type="button"
                    onClick={() => setFilterMode("advanced")}
                    className={`rounded px-2 py-0.5 ${filterMode === "advanced" ? "bg-indigo-500 text-white" : "bg-white/5"}`}
                    aria-pressed={filterMode === "advanced"}
                  >
                    Advanced
                  </button>
                </div>
                {filterMode === "easy" ? (
                  <FilterEasyMode
                    initialConfig={{
                      match: queryDefinition.match,
                      groups: queryDefinition.groups,
                    }}
                    onChange={(filter) =>
                      setQueryDefinition({
                        ...queryDefinition,
                        match: filter.match,
                        groups: filter.groups,
                      })
                    }
                  />
                ) : (
                  <CollectionRulesEditor
                    value={queryDefinition}
                    onChange={setQueryDefinition}
                    libraries={props.libraries}
                    showMediaScopeSelector
                    allowLibrarySelection
                  />
                )}
              </div>
            </div>
          ) : null}

          {showRecipeParams && recipeDef ? (
            <RecipeParamFields def={recipeDef} params={recipeParams} onChange={setRecipeParams} />
          ) : null}
        </div>

        <SheetFooter>
          <Button variant="outline" onClick={() => props.onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={saveDisabled || isSubmitting}>
            {isEdit ? "Save" : "Add Section"}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

export { buildProfileSectionSaveEntry as buildSectionSaveEntry };
