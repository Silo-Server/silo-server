import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { rowKindLabel } from "@/lib/homeRows/catalog";
import { queryDefinitionFromSectionConfig } from "@/api/types";
import type { Library } from "@/api/types";
import { matchRecipePreset, type RecipeCatalogResponse } from "@/lib/recipes";
import { Eye, EyeOff, GripVertical, Pencil, Star, Trash2 } from "lucide-react";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { TableCell, TableRow } from "@/components/ui/table";
import { BulkSelectionCheckbox } from "@/components/BulkSelectionCheckbox";
import { useT, type MessageKey } from "@/i18n";

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

export function recipeLabel(
  catalog: RecipeCatalogResponse | undefined,
  type: string,
  config?: Record<string, unknown>,
): string {
  if (catalog) {
    for (const defs of Object.values(catalog.categories)) {
      const found = defs?.find((def) => def.type === type);
      const label = found ? matchRecipePreset(found, config)?.display_name : undefined;
      if (label) return label;
    }
  }
  return rowKindLabel(type);
}

function continueTypeLabel(config?: Record<string, unknown>): MessageKey | null {
  const value = config?.continue_type;
  if (value === "listening") return "web.home.rows.sections.listening";
  if (value === "watching") return "web.home.rows.sections.watching";
  if (value === "reading") return "web.home.rows.sections.reading";
  if (config?.filter_type === "audiobook" || config?.media_scope === "audiobook") {
    return "web.home.rows.sections.listening";
  }
  return null;
}

/** Badges for a section that holds one kind of media. */
const MEDIA_SCOPE_LABELS: Partial<Record<string, MessageKey>> = {
  movie: "web.home.rows.sections.movies",
  series: "web.home.rows.sections.series",
  episode: "web.home.rows.sections.episodes",
  audiobook: "web.home.rows.sections.audiobooks",
  ebook: "web.home.rows.sections.ebooks",
};

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
  const t = useT();
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
  const scopeLabel = queryDefinition.media_scope
    ? MEDIA_SCOPE_LABELS[queryDefinition.media_scope]
    : undefined;

  return (
    <div className="flex flex-wrap gap-1">
      <Badge variant="secondary">{recipeLabel(catalog, section.sectionType, section.config)}</Badge>
      {resumeLabel ? <Badge variant="outline">{t(resumeLabel)}</Badge> : null}
      {scopeLabel ? <Badge variant="outline">{t(scopeLabel)}</Badge> : null}
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
      {section.featured ? (
        <Badge variant="default">{t("web.home.rows.sections.featured")}</Badge>
      ) : null}
      {showVisibility ? (
        <Badge variant={section.hidden ? "secondary" : "outline"}>
          {section.hidden
            ? t("web.home.rows.sections.hidden")
            : t("web.home.rows.sections.visible")}
        </Badge>
      ) : null}
      {showEnabled ? (
        <Badge variant={section.enabled ? "default" : "secondary"}>
          {section.enabled ? t("web.home.rows.sections.on") : t("web.home.rows.sections.off")}
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
      <Badge variant="secondary" className="ms-2">
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
  const t = useT();
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
            aria-label={t("web.home.rows.sections.drag", { section: section.title })}
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
          {section.enabled ? t("web.home.rows.sections.on") : t("web.home.rows.sections.off")}
        </Badge>
      </TableCell>
      <TableCell>
        <div className="flex gap-1">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 w-7 p-0"
            onClick={onEdit}
            aria-label={t("web.home.rows.sections.edit", { section: section.title })}
          >
            <Pencil className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive h-7 w-7 p-0"
            onClick={onDelete}
            aria-label={t("web.home.rows.sections.delete", { section: section.title })}
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
  /** Omit to leave the row without an edit button. */
  onEdit?: () => void;
  /** Omit to leave the row without a delete button. */
  onDelete?: () => void;
  actions?: ReactNode;
}) {
  const t = useT();
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
        aria-label={t("web.home.rows.sections.drag", { section: section.title })}
        className="hover:bg-surface-hover mt-0.5 cursor-grab touch-none rounded-md p-1 transition-colors"
        disabled={disabled}
        {...attributes}
        {...listeners}
      >
        <GripVertical className="text-muted-foreground h-4 w-4" />
      </button>
      <button
        type="button"
        aria-label={t(
          section.hidden ? "web.home.rows.sections.show" : "web.home.rows.sections.hide",
          { section: section.title },
        )}
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
          {t("web.home.rows.sections.kindAndLimit", {
            item_count: section.itemLimit,
            kind: rowKindLabel(section.sectionType),
            items: section.itemLimit,
          })}
        </div>
      </div>
      {actions}
      {onEdit ? (
        <Button
          variant="ghost"
          size="sm"
          className="h-8 w-8 p-0"
          aria-label={t("web.home.rows.sections.edit", { section: section.title })}
          disabled={disabled}
          onClick={onEdit}
        >
          <Pencil className="h-3.5 w-3.5" />
        </Button>
      ) : null}
      {onDelete ? (
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:bg-destructive/10 hover:text-destructive h-8 w-8 p-0"
          aria-label={t("web.home.rows.sections.delete", { section: section.title })}
          disabled={disabled}
          onClick={onDelete}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      ) : null}
    </div>
  );
}
