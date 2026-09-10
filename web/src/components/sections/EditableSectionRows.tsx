import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { sectionTypeLabel } from "@/lib/sectionTypes";
import { queryDefinitionFromSectionConfig } from "@/api/types";
import type { Library } from "@/api/types";
import type { GalleryPreset, RecipeCatalogResponse } from "@/lib/recipes";
import { Eye, EyeOff, GripVertical, Pencil, Star, Trash2 } from "lucide-react";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { TableCell, TableRow } from "@/components/ui/table";
import { BulkSelectionCheckbox } from "@/components/BulkSelectionCheckbox";

export interface EditableSectionViewModel {
  id: string;
  title: string;
  sectionType: string;
  itemLimit: number;
  featured: boolean;
  hidden?: boolean;
  enabled?: boolean;
  isCustom?: boolean;
  config?: Record<string, unknown>;
}

function paramsEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a === null || b === null) return false;
  if (typeof a !== "object" || typeof b !== "object") return false;
  return JSON.stringify(a) === JSON.stringify(b);
}

/**
 * How specifically a preset matches a saved section config: the number of
 * default_params the config reproduces, or -1 when any of them disagrees.
 *
 * A preset carrying no default_params scores 0 — it describes the recipe
 * generically, so it matches anything but loses to any preset that matches on
 * real parameters.
 */
function presetMatchScore(
  preset: GalleryPreset,
  config: Record<string, unknown> | undefined,
): number {
  const keys = Object.keys(preset.default_params ?? {});
  if (keys.length === 0) return 0;
  if (!config) return -1;
  for (const key of keys) {
    if (!paramsEqual(preset.default_params[key], config[key])) return -1;
  }
  return keys.length;
}

/**
 * The preset a saved section was created from, identified by matching its
 * default_params against the section's config.
 *
 * Presets within one recipe differ only by those params — trending_discover
 * ships tmdb/day, tmdb/week and trakt/week — so the first preset is not a
 * usable stand-in for the rest. Falls back to the first preset when nothing
 * matches, which keeps sections saved before a preset existed labelled as they
 * were.
 */
function matchingPreset(
  presets: GalleryPreset[] | undefined,
  config: Record<string, unknown> | undefined,
): GalleryPreset | undefined {
  let best: GalleryPreset | undefined;
  let bestScore = -1;
  for (const preset of presets ?? []) {
    const score = presetMatchScore(preset, config);
    if (score > bestScore) {
      best = preset;
      bestScore = score;
    }
  }
  return bestScore >= 0 ? best : presets?.[0];
}

export function recipeLabel(
  catalog: RecipeCatalogResponse | undefined,
  type: string,
  config?: Record<string, unknown>,
): string {
  if (catalog) {
    for (const defs of Object.values(catalog.categories)) {
      const found = defs?.find((def) => def.type === type);
      const label = matchingPreset(found?.presets, config)?.display_name;
      if (label) return label;
    }
  }
  return sectionTypeLabel(type);
}

function continueTypeLabel(config?: Record<string, unknown>): string | null {
  const value = config?.continue_type;
  if (value === "listening") return "Listening";
  if (value === "watching") return "Watching";
  if (value === "reading") return "Reading";
  if (config?.filter_type === "audiobook" || config?.media_scope === "audiobook") {
    return "Listening";
  }
  return null;
}

export function SectionSummaryBadges({
  section,
  libraries,
  collectionLabels,
  catalog,
  showVisibility = false,
  showEnabled = false,
}: {
  section: EditableSectionViewModel;
  libraries?: Library[];
  collectionLabels?: Map<string, string>;
  catalog?: RecipeCatalogResponse;
  showVisibility?: boolean;
  showEnabled?: boolean;
}) {
  const queryDefinition = queryDefinitionFromSectionConfig(section.config);
  const collectionId =
    typeof section.config?.library_collection_id === "string"
      ? section.config.library_collection_id
      : typeof section.config?.user_collection_id === "string"
        ? section.config.user_collection_id
        : undefined;
  const collectionLabel = collectionId ? collectionLabels?.get(collectionId) : undefined;
  const resumeLabel =
    section.sectionType === "continue_watching" ? continueTypeLabel(section.config) : null;

  return (
    <div className="flex flex-wrap gap-1">
      <Badge variant="secondary">{recipeLabel(catalog, section.sectionType, section.config)}</Badge>
      {resumeLabel ? <Badge variant="outline">{resumeLabel}</Badge> : null}
      {queryDefinition.media_scope === "movie" ? <Badge variant="outline">Movies</Badge> : null}
      {queryDefinition.media_scope === "series" ? <Badge variant="outline">Series</Badge> : null}
      {queryDefinition.media_scope === "episode" ? <Badge variant="outline">Episodes</Badge> : null}
      {queryDefinition.media_scope === "audiobook" ? (
        <Badge variant="outline">Audiobooks</Badge>
      ) : null}
      {queryDefinition.media_scope === "ebook" ? <Badge variant="outline">Ebooks</Badge> : null}
      {libraries
        ? queryDefinition.library_ids.map((libraryId) => {
            const library = libraries.find((entry) => entry.id === libraryId);
            return library ? (
              <Badge key={libraryId} variant="outline">
                {library.name}
              </Badge>
            ) : null;
          })
        : null}
      {collectionLabel ? <Badge variant="outline">{collectionLabel}</Badge> : null}
      {section.featured ? <Badge variant="default">Featured</Badge> : null}
      {showVisibility ? (
        <Badge variant={section.hidden ? "secondary" : "outline"}>
          {section.hidden ? "Hidden" : "Visible"}
        </Badge>
      ) : null}
      {showEnabled ? (
        <Badge variant={section.enabled ? "default" : "secondary"}>
          {section.enabled ? "On" : "Off"}
        </Badge>
      ) : null}
    </div>
  );
}

export function SectionDragOverlay({
  section,
  catalog,
}: {
  section: EditableSectionViewModel;
  catalog?: RecipeCatalogResponse;
}) {
  return (
    <div className="surface-panel flex items-center gap-2 rounded-xl border-0 px-3 py-2 shadow-lg">
      <GripVertical className="text-muted-foreground h-4 w-4" />
      <span className="font-medium">{section.title}</span>
      <Badge variant="secondary" className="ml-2">
        {recipeLabel(catalog, section.sectionType, section.config)}
      </Badge>
    </div>
  );
}

export function SortableSectionTableRow({
  section,
  canReorder,
  libraries,
  collectionLabels,
  catalog,
  selected,
  selectionLabel,
  onSelectionChange,
  onEdit,
  onDelete,
}: {
  section: EditableSectionViewModel;
  canReorder: boolean;
  libraries: Library[];
  collectionLabels: Map<string, string>;
  catalog?: RecipeCatalogResponse;
  selected: boolean;
  selectionLabel: string;
  onSelectionChange: (checked: boolean, extendRange: boolean) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: section.id,
    disabled: !canReorder,
  });
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.4 : 1,
  };

  return (
    <TableRow ref={setNodeRef} style={style} data-state={selected ? "selected" : undefined}>
      <TableCell className="w-10">
        <BulkSelectionCheckbox
          label={selectionLabel}
          selected={selected}
          onSelectionChange={onSelectionChange}
        />
      </TableCell>
      <TableCell>
        {canReorder ? (
          <button
            type="button"
            className="cursor-grab touch-none"
            aria-label={`Drag ${section.title}`}
            {...attributes}
            {...listeners}
          >
            <GripVertical className="text-muted-foreground h-4 w-4" />
          </button>
        ) : (
          <GripVertical className="text-muted-foreground h-4 w-4" />
        )}
      </TableCell>
      <TableCell className="font-medium">{section.title}</TableCell>
      <TableCell>
        <SectionSummaryBadges
          section={section}
          libraries={libraries}
          collectionLabels={collectionLabels}
          catalog={catalog}
        />
      </TableCell>
      <TableCell>{section.itemLimit}</TableCell>
      <TableCell>
        {section.featured ? <Star className="h-4 w-4 fill-yellow-500 text-yellow-500" /> : null}
      </TableCell>
      <TableCell>
        <Badge variant={section.enabled ? "default" : "secondary"}>
          {section.enabled ? "On" : "Off"}
        </Badge>
      </TableCell>
      <TableCell>
        <div className="flex gap-1">
          <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={onEdit}>
            <Pencil className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive h-7 w-7 p-0"
            onClick={onDelete}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}

export function SortableSectionCardRow({
  section,
  disabled = false,
  catalog,
  onToggleHidden,
  onEdit,
  onDelete,
  actions,
}: {
  section: EditableSectionViewModel;
  disabled?: boolean;
  catalog?: RecipeCatalogResponse;
  onToggleHidden: () => void;
  onEdit: () => void;
  onDelete: () => void;
  actions?: ReactNode;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: section.id,
  });
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.4 : 1,
  };

  return (
    <div
      ref={setNodeRef}
      style={style}
      className="surface-panel-subtle flex items-start gap-3 rounded-[1.2rem] px-3 py-3"
    >
      <button
        type="button"
        aria-label={`Drag ${section.title}`}
        className="hover:bg-surface-hover mt-0.5 cursor-grab touch-none rounded-md p-1 transition-colors"
        disabled={disabled}
        {...attributes}
        {...listeners}
      >
        <GripVertical className="text-muted-foreground h-4 w-4" />
      </button>
      <button
        type="button"
        aria-label={`${section.hidden ? "Show" : "Hide"} ${section.title}`}
        aria-pressed={!section.hidden}
        className="hover:bg-surface-hover mt-0.5 rounded-md p-1 transition-colors"
        disabled={disabled}
        onClick={onToggleHidden}
      >
        {section.hidden ? (
          <EyeOff className="text-muted-foreground h-4 w-4" />
        ) : (
          <Eye className="text-foreground h-4 w-4" />
        )}
      </button>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span
            className={`font-medium ${section.hidden ? "text-muted-foreground line-through" : ""}`}
          >
            {section.title}
          </span>
          <SectionSummaryBadges section={section} catalog={catalog} showVisibility />
        </div>
        <div className="text-muted-foreground text-[13px]">
          {sectionTypeLabel(section.sectionType)} . {section.itemLimit} items
        </div>
      </div>
      {actions}
      <Button
        variant="ghost"
        size="sm"
        className="h-8 w-8 p-0"
        aria-label={`Edit ${section.title}`}
        disabled={disabled}
        onClick={onEdit}
      >
        <Pencil className="h-3.5 w-3.5" />
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className="text-destructive hover:bg-destructive/10 hover:text-destructive h-8 w-8 p-0"
        aria-label={`Delete ${section.title}`}
        disabled={disabled}
        onClick={onDelete}
      >
        <Trash2 className="h-3.5 w-3.5" />
      </Button>
    </div>
  );
}
