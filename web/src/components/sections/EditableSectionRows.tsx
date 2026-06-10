import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { sectionTypeLabel } from "@/lib/sectionTypes";
import { queryDefinitionFromSectionConfig } from "@/api/types";
import type { Library } from "@/api/types";
import type { RecipeCatalogResponse } from "@/lib/recipes";
import { Eye, EyeOff, GripVertical, Pencil, Star, Trash2 } from "lucide-react";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { TableCell, TableRow } from "@/components/ui/table";

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

export function recipeLabel(catalog: RecipeCatalogResponse | undefined, type: string): string {
  if (catalog) {
    for (const defs of Object.values(catalog.categories)) {
      const found = defs?.find((def) => def.type === type);
      if (found?.presets[0]?.display_name) return found.presets[0].display_name;
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
      <Badge variant="secondary">{recipeLabel(catalog, section.sectionType)}</Badge>
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
        {recipeLabel(catalog, section.sectionType)}
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
  onEdit,
  onDelete,
}: {
  section: EditableSectionViewModel;
  canReorder: boolean;
  libraries: Library[];
  collectionLabels: Map<string, string>;
  catalog?: RecipeCatalogResponse;
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
    <TableRow ref={setNodeRef} style={style}>
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
