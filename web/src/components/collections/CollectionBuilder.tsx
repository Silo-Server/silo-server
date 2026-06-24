import { useState } from "react";
import type { ReactNode } from "react";

import type {
  CollectionPreviewRequest,
  DisplayQueryDefinition,
  QueryDefinition,
  QueryDefinitionInput,
  SmartCollectionAccess,
  UserCollectionMediaFilter,
  UserCollectionWatchFilter,
} from "@/api/types";
import { createEmptyQueryDefinition, normalizeQueryDefinition } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  COLLECTION_MEDIA_FILTER_OPTIONS,
  COLLECTION_WATCH_FILTER_OPTIONS,
  displayFiltersToQueryDefinition,
  queryDefinitionToDisplayFilters,
} from "@/lib/collectionDisplayFilters";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  buildCollectionPreviewRequest,
  useAdminCollectionPreview,
  useUserCollectionPreview,
} from "@/hooks/queries/collectionPreviews";

import CollectionAccessEditor from "./CollectionAccessEditor";
import CollectionGuidedRulesEditor from "./CollectionGuidedRulesEditor";
import CollectionOrderingEditor from "./CollectionOrderingEditor";
import CollectionPreviewPane from "./CollectionPreviewPane";
import CollectionRulesEditor from "./CollectionRulesEditor";
import {
  SMART_COLLECTION_DEFAULT_LIMIT,
  SMART_COLLECTION_MAX_LIMIT,
  withSmartCollectionLimit,
} from "./smartCollectionLimits";

export interface CollectionBuilderValue {
  title: string;
  description: string;
  collection_type: "manual" | "smart";
  visibility: "visible" | "hidden";
  featured: boolean;
  query_definition: QueryDefinition;
  sort_config: Record<string, unknown>;
  access: SmartCollectionAccess;
  include_in_server_collections: boolean;
  /** Filter-only QueryDefinition fragment for the display presets; undefined = no filter. */
  display_query_definition?: DisplayQueryDefinition;
}

export interface CollectionBuilderProps {
  mode: "admin" | "user";
  value: CollectionBuilderValue;
  onChange: (value: CollectionBuilderValue) => void;
  onSubmit: () => void;
  submitLabel?: string;
  libraries?: Array<{ id: number; name: string }>;
  profiles?: Array<{ id: string; name: string }>;
  isPending?: boolean;
  readOnly?: boolean;
  allowLibrarySelection?: boolean;
  allowAccessControls?: boolean;
  creatorProfileId?: string | null;
  defaultAdvanced?: boolean;
  previewLayout?: "stacked" | "sidebar";
  sidebarContent?: ReactNode;
  children?: ReactNode;
}

export function createCollectionBuilderValue(
  overrides?: Omit<Partial<CollectionBuilderValue>, "query_definition"> & {
    query_definition?: QueryDefinitionInput | null;
  },
): CollectionBuilderValue {
  const collectionType = overrides?.collection_type ?? "smart";
  const queryDefinition = normalizeQueryDefinition(
    overrides?.query_definition ?? createEmptyQueryDefinition(),
  );

  return {
    title: overrides?.title ?? "",
    description: overrides?.description ?? "",
    collection_type: collectionType,
    visibility: overrides?.visibility ?? "visible",
    featured: overrides?.featured ?? false,
    query_definition:
      collectionType === "smart" ? withSmartCollectionLimit(queryDefinition) : queryDefinition,
    sort_config: overrides?.sort_config ?? {},
    access: overrides?.access ?? { is_shared: false, allowed_profile_ids: [] },
    include_in_server_collections: overrides?.include_in_server_collections ?? false,
    display_query_definition: overrides?.display_query_definition,
  };
}

export function buildCollectionBuilderPreviewRequest(
  value: CollectionBuilderValue,
  limit = 12,
): CollectionPreviewRequest | null {
  if (value.collection_type !== "smart") {
    return null;
  }
  return buildCollectionPreviewRequest(value.query_definition, limit);
}

export default function CollectionBuilder({
  mode,
  value,
  onChange,
  onSubmit,
  submitLabel = "Save Collection",
  libraries = [],
  profiles = [],
  isPending = false,
  readOnly = false,
  allowLibrarySelection = true,
  allowAccessControls = false,
  creatorProfileId,
  defaultAdvanced = false,
  previewLayout = "stacked",
  sidebarContent,
  children,
}: CollectionBuilderProps) {
  const [advanced, setAdvanced] = useState(defaultAdvanced);

  const previewRequest = buildCollectionBuilderPreviewRequest(value);
  const adminPreview = useAdminCollectionPreview(mode === "admin" ? previewRequest : null);
  const userPreview = useUserCollectionPreview(mode === "user" ? previewRequest : null);
  const previewQuery = mode === "admin" ? adminPreview : userPreview;
  const previewPanel =
    value.collection_type === "smart" ? (
      <div className="space-y-4">
        {sidebarContent}
        <section className="space-y-4">
          <h2 className="text-lg font-semibold">Preview</h2>
          <CollectionPreviewPane
            preview={previewQuery.data}
            enabled
            isLoading={previewQuery.isLoading || previewQuery.isFetching}
          />
        </section>
      </div>
    ) : sidebarContent ? (
      <div className="space-y-4">{sidebarContent}</div>
    ) : null;

  const formSections = (
    <>
      <section className="space-y-4">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-semibold">Basics</h2>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setAdvanced((current) => !current)}
          >
            {advanced ? "Switch to Guided" : "Switch to Advanced"}
          </Button>
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor={`collection-title-${mode}`}>
              {mode === "admin" ? "Title" : "Name"}
            </Label>
            <Input
              id={`collection-title-${mode}`}
              value={value.title}
              onChange={(event) => onChange({ ...value, title: event.target.value })}
              disabled={readOnly}
              required
            />
          </div>

          <div className="space-y-2">
            <Label>Collection Mode</Label>
            <Select
              value={value.collection_type}
              onValueChange={(next) =>
                onChange({
                  ...value,
                  collection_type: next as "manual" | "smart",
                  query_definition:
                    next === "smart"
                      ? withSmartCollectionLimit(value.query_definition)
                      : value.query_definition,
                })
              }
              disabled={readOnly}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="smart">Smart</SelectItem>
                <SelectItem value="manual">Manual</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        {mode === "admin" ? (
          <>
            <div className="space-y-2">
              <Label htmlFor="collection-description">Description</Label>
              <textarea
                id="collection-description"
                rows={4}
                value={value.description}
                onChange={(event) => onChange({ ...value, description: event.target.value })}
                disabled={readOnly}
                className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 flex min-h-[110px] w-full rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px]"
              />
            </div>

            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-2">
                <Label>Visibility</Label>
                <Select
                  value={value.visibility}
                  onValueChange={(next) =>
                    onChange({ ...value, visibility: next as "visible" | "hidden" })
                  }
                  disabled={readOnly}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="visible">Visible</SelectItem>
                    <SelectItem value="hidden">Hidden</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <ToggleRow
                title="Featured"
                description="Surface this collection near the top of the library."
                checked={value.featured}
                onCheckedChange={(checked) => onChange({ ...value, featured: checked })}
                disabled={readOnly}
              />
            </div>
          </>
        ) : null}
      </section>

      {children}

      {mode === "user" && value.collection_type === "manual" ? (
        <DisplayFilterControls mode={mode} value={value} onChange={onChange} readOnly={readOnly} />
      ) : null}

      {value.collection_type === "smart" ? (
        <>
          <section className="space-y-4">
            <h2 className="text-lg font-semibold">{advanced ? "Rules" : "Filters"}</h2>
            {advanced ? (
              <CollectionRulesEditor
                value={value.query_definition}
                onChange={(query_definition) => onChange({ ...value, query_definition })}
                libraries={libraries}
                allowLibrarySelection={allowLibrarySelection}
                allowPersonalizedFilters={mode === "user"}
                allowPersonalizedSorts={mode === "user"}
                readOnly={readOnly}
              />
            ) : (
              <CollectionGuidedRulesEditor
                value={value.query_definition}
                onChange={(query_definition) => onChange({ ...value, query_definition })}
                libraries={libraries}
                allowLibrarySelection={allowLibrarySelection}
                allowPersonalizedFilters={mode === "user"}
                allowPersonalizedSorts={mode === "user"}
                readOnly={readOnly}
              />
            )}
          </section>

          <section className="space-y-4">
            <h2 className="text-lg font-semibold">Limit</h2>
            <SmartCollectionLimitField
              query={value.query_definition}
              onQueryChange={(query_definition) => onChange({ ...value, query_definition })}
              readOnly={readOnly}
            />
          </section>

          {advanced ? (
            <section className="space-y-4">
              <h2 className="text-lg font-semibold">Ordering</h2>
              <CollectionOrderingEditor
                query={value.query_definition}
                sortConfig={value.sort_config}
                onQueryChange={(query_definition) => onChange({ ...value, query_definition })}
                onSortConfigChange={(sort_config) => onChange({ ...value, sort_config })}
                allowPersonalizedSorts={mode === "user"}
                readOnly={readOnly}
              />
            </section>
          ) : null}
        </>
      ) : (
        <section className="space-y-2">
          <h2 className="text-lg font-semibold">Ordering</h2>
          <div className="text-muted-foreground rounded-lg border border-dashed px-4 py-5 text-sm">
            Manual collections are curated after save by adding titles directly.
          </div>
        </section>
      )}

      {allowAccessControls ? (
        <section className="space-y-4">
          <h2 className="text-lg font-semibold">Access</h2>
          <CollectionAccessEditor
            value={value.access}
            onChange={(access) => onChange({ ...value, access })}
            profiles={profiles}
            readOnly={readOnly}
            creatorProfileId={creatorProfileId}
          />
        </section>
      ) : null}

      {mode === "user" ? (
        <section className="space-y-4">
          <h2 className="text-lg font-semibold">Library Collections tab</h2>
          <ToggleRow
            title="Show in my library Collections tab"
            description="Pin this collection to your library's Collections tab alongside the admin shelves. Only you see it — personal collections are private to your user."
            checked={value.include_in_server_collections}
            onCheckedChange={(checked) =>
              onChange({ ...value, include_in_server_collections: checked })
            }
            disabled={readOnly}
          />
        </section>
      ) : null}

      <Button type="submit" className="w-full" disabled={isPending || readOnly}>
        {isPending ? "Saving..." : submitLabel}
      </Button>
    </>
  );

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
      className="space-y-6"
    >
      {previewLayout === "sidebar" && previewPanel ? (
        <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
          <div className="space-y-6">{formSections}</div>
          <div className="xl:sticky xl:top-6 xl:self-start">{previewPanel}</div>
        </div>
      ) : (
        <>
          {formSections}
          {previewLayout === "stacked" ? previewPanel : null}
        </>
      )}
    </form>
  );
}

export function SmartCollectionLimitField({
  query,
  onQueryChange,
  readOnly = false,
}: {
  query: QueryDefinition;
  onQueryChange: (query: QueryDefinition) => void;
  readOnly?: boolean;
}) {
  function commit(input: HTMLInputElement) {
    const parsed = Number.parseInt(input.value, 10);
    const limit =
      Number.isFinite(parsed) && parsed > 0
        ? Math.min(parsed, SMART_COLLECTION_MAX_LIMIT)
        : SMART_COLLECTION_DEFAULT_LIMIT;
    input.value = String(limit);
    if (query.limit !== limit) {
      onQueryChange({ ...query, limit });
    }
  }

  return (
    <div className="grid gap-3 rounded-lg border px-4 py-3 sm:grid-cols-[minmax(0,1fr)_8rem] sm:items-center">
      <div>
        <Label htmlFor="smart-collection-limit">Max items</Label>
        <p className="text-muted-foreground mt-1 text-xs">
          Caps saved collection results. Use 1-{SMART_COLLECTION_MAX_LIMIT}.
        </p>
      </div>
      <Input
        key={query.limit ?? SMART_COLLECTION_DEFAULT_LIMIT}
        id="smart-collection-limit"
        type="number"
        min={1}
        max={SMART_COLLECTION_MAX_LIMIT}
        step={1}
        defaultValue={query.limit ?? SMART_COLLECTION_DEFAULT_LIMIT}
        disabled={readOnly}
        onBlur={(event) => commit(event.currentTarget)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.currentTarget.blur();
          }
        }}
      />
    </div>
  );
}

function DisplayFilterControls({
  mode,
  value,
  onChange,
  readOnly,
}: {
  mode: "admin" | "user";
  value: CollectionBuilderValue;
  onChange: (value: CollectionBuilderValue) => void;
  readOnly?: boolean;
}) {
  const { watch, media } = queryDefinitionToDisplayFilters(value.display_query_definition);

  function commit(nextWatch: UserCollectionWatchFilter, nextMedia: UserCollectionMediaFilter) {
    onChange({
      ...value,
      display_query_definition: displayFiltersToQueryDefinition(nextWatch, nextMedia),
    });
  }

  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-lg font-semibold">Display filters</h2>
        <p className="text-muted-foreground mt-1 text-sm">
          Uses the active profile&rsquo;s watched state. Shared profiles may see different results.
        </p>
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor={`collection-watch-filter-${mode}`}>Watch state</Label>
          <Select
            value={watch}
            onValueChange={(next) => commit(next as UserCollectionWatchFilter, media)}
            disabled={readOnly}
          >
            <SelectTrigger id={`collection-watch-filter-${mode}`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {COLLECTION_WATCH_FILTER_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label htmlFor={`collection-media-filter-${mode}`}>Content</Label>
          <Select
            value={media}
            onValueChange={(next) => commit(watch, next as UserCollectionMediaFilter)}
            disabled={readOnly}
          >
            <SelectTrigger id={`collection-media-filter-${mode}`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {COLLECTION_MEDIA_FILTER_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
    </section>
  );
}

function ToggleRow({
  title,
  description,
  checked,
  onCheckedChange,
  disabled,
}: {
  title: string;
  description: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <div className="border-border flex items-center justify-between rounded-lg border px-4 py-3">
      <div className="pr-4">
        <p className="text-sm font-medium">{title}</p>
        <p className="text-muted-foreground text-xs">{description}</p>
      </div>
      <Switch checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} />
    </div>
  );
}
