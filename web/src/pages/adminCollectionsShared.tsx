import { useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import type { CreateLibraryCollectionRequest, Library, LibraryCollection } from "@/api/types";
import { normalizeQueryDefinition } from "@/api/types";
import {
  COLLECTION_MAX_ITEMS,
  libraryEligibilityForMediaKind,
  type LibraryEligibility,
} from "@/lib/collectionTemplates";
import {
  useCreateAdminCollection,
  useDeleteCollectionImage,
  useImportMDBListCollection,
  useImportTMDBCollection,
  useImportTraktCollection,
  useUpdateAdminCollection,
} from "@/hooks/queries/admin/collections";
import { useProfiles } from "@/hooks/queries/profiles";
import { ImageUploadField } from "@/components/ImageUploadField";
import CollectionBuilder, {
  createCollectionBuilderValue,
  type CollectionBuilderValue,
} from "@/components/collections/CollectionBuilder";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Download, ListPlus, Sparkles, TrendingUp } from "lucide-react";
import { SyncScheduleField } from "@/components/collections/SyncScheduleField";

export type CollectionSourceType = "manual" | "mdblist" | "tmdb" | "trakt";
export type TMDBPreset =
  | "trending"
  | "popular"
  | "top_rated"
  | "now_playing"
  | "upcoming"
  | "airing_today"
  | "on_the_air";
type TMDBMediaType = "movie" | "tv" | "all";
type TMDBTimeWindow = "day" | "week";
type TraktSourceKind = "preset" | "list";
type TraktPreset = "trending" | "popular" | "recommended";
type TraktMediaType = "movie" | "tv";

interface TMDBPresetSourceConfig {
  preset: TMDBPreset;
  mediaType: TMDBMediaType;
  timeWindow: TMDBTimeWindow;
  limit: string;
}

interface TraktPresetSourceConfig {
  preset: TraktPreset;
  mediaType: TraktMediaType;
  profileId: string;
  limit: string;
}

interface TraktSourceConfig extends TraktPresetSourceConfig {
  sourceKind: TraktSourceKind;
  listUrl: string;
}

export function buildAdminCollectionEditorPath(id: "new" | string, libraryId?: number | null) {
  const base = id === "new" ? "/admin/collections/new" : `/admin/collections/${id}/edit`;
  return libraryId ? `${base}?libraryId=${libraryId}` : base;
}

export function buildAdminCollectionsReturnPath(libraryId?: number | null) {
  return libraryId ? `/admin/collections?libraryId=${libraryId}` : "/admin/collections";
}

export function toAdminCollectionBuilderValue(
  collection: LibraryCollection | null,
  initialLibraryId: number | null,
): CollectionBuilderValue {
  const libraryIds =
    collection?.library_ids && collection.library_ids.length > 0
      ? collection.library_ids
      : collection?.library_id
        ? [collection.library_id]
        : initialLibraryId
          ? [initialLibraryId]
          : [];

  return createCollectionBuilderValue({
    title: collection?.title ?? "",
    description: collection?.description ?? "",
    collection_type: collection?.collection_type === "manual" ? "manual" : "smart",
    visibility: collection?.visibility ?? "visible",
    featured: collection?.featured ?? false,
    query_definition: normalizeQueryDefinition({
      ...collection?.query_definition,
      library_ids: libraryIds,
    }),
    sort_config: collection?.sort_config ?? {},
  });
}

export function toAdminCollectionRequest(
  value: CollectionBuilderValue,
): CreateLibraryCollectionRequest {
  const libraryIds = value.query_definition.library_ids;

  return {
    library_ids: libraryIds,
    title: value.title,
    description: value.description,
    collection_type: value.collection_type,
    visibility: value.visibility,
    featured: value.featured,
    query_definition: value.collection_type === "smart" ? value.query_definition : undefined,
    sort_config: value.collection_type === "smart" ? value.sort_config : undefined,
  };
}

export function parseOptionalPositiveInteger(value: string): number | undefined {
  const trimmed = value.trim();
  if (trimmed.length === 0) {
    return undefined;
  }

  const parsed = Number.parseInt(trimmed, 10);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return undefined;
  }

  return parsed;
}

function getMDBListLimitValue(collection: LibraryCollection | null): string {
  const limit = collection?.source_config?.limit;
  return typeof limit === "number" && Number.isFinite(limit) && limit > 0 ? String(limit) : "";
}

function getTMDBPresetLabel(preset: TMDBPreset): string {
  switch (preset) {
    case "trending":
      return "Trending";
    case "popular":
      return "Popular";
    case "top_rated":
      return "Top Rated";
    case "now_playing":
      return "Now Playing";
    case "upcoming":
      return "Upcoming";
    case "airing_today":
      return "Airing Today";
    case "on_the_air":
      return "On The Air";
  }
}

function getTraktPresetLabel(preset: TraktPreset): string {
  switch (preset) {
    case "trending":
      return "Trending";
    case "popular":
      return "Popular";
    case "recommended":
      return "Recommended";
  }
}

function getTMDBAllowedMediaTypes(preset: TMDBPreset): TMDBMediaType[] {
  switch (preset) {
    case "trending":
      return ["all", "movie", "tv"];
    case "popular":
    case "top_rated":
      return ["movie", "tv"];
    case "now_playing":
    case "upcoming":
      return ["movie"];
    case "airing_today":
    case "on_the_air":
      return ["tv"];
  }
}

function getDefaultTMDBMediaType(preset: TMDBPreset): TMDBMediaType {
  switch (preset) {
    case "trending":
      return "all";
    case "popular":
    case "top_rated":
    case "now_playing":
    case "upcoming":
      return "movie";
    case "airing_today":
    case "on_the_air":
      return "tv";
  }
}

function tmdbPresetNeedsTimeWindow(preset: TMDBPreset): boolean {
  return preset === "trending";
}

function normalizeTMDBPresetMediaType(preset: TMDBPreset, mediaType: TMDBMediaType): TMDBMediaType {
  return getTMDBAllowedMediaTypes(preset).includes(mediaType)
    ? mediaType
    : getDefaultTMDBMediaType(preset);
}

export function parseTMDBPresetSourceConfig(
  collection: LibraryCollection | null,
): TMDBPresetSourceConfig {
  const cfg = collection?.source_config;
  const preset: TMDBPreset =
    cfg?.preset === "trending" ||
    cfg?.preset === "popular" ||
    cfg?.preset === "top_rated" ||
    cfg?.preset === "now_playing" ||
    cfg?.preset === "upcoming" ||
    cfg?.preset === "airing_today" ||
    cfg?.preset === "on_the_air"
      ? cfg.preset
      : "trending";
  const mediaType = normalizeTMDBPresetMediaType(
    preset,
    cfg?.media_type === "movie" || cfg?.media_type === "tv" || cfg?.media_type === "all"
      ? (cfg.media_type as TMDBMediaType)
      : getDefaultTMDBMediaType(preset),
  );
  const timeWindow =
    cfg?.time_window === "day" || cfg?.time_window === "week"
      ? (cfg.time_window as TMDBTimeWindow)
      : "day";
  const limit =
    typeof cfg?.limit === "number" && Number.isFinite(cfg.limit) && cfg.limit > 0
      ? String(cfg.limit)
      : "";

  return { preset, mediaType, timeWindow, limit };
}

export function parseTraktPresetSourceConfig(
  collection: LibraryCollection | null,
): TraktSourceConfig {
  const cfg = collection?.source_config;
  const sourceKind: TraktSourceKind = cfg?.mode === "trakt_list" ? "list" : "preset";
  const preset: TraktPreset =
    cfg?.preset === "popular" || cfg?.preset === "recommended" || cfg?.preset === "trending"
      ? cfg.preset
      : "trending";
  const mediaType: TraktMediaType = cfg?.media_type === "tv" ? "tv" : "movie";
  const profileId = typeof cfg?.profile_id === "string" ? cfg.profile_id : "";
  const configListUrl =
    typeof cfg?.list_url === "string" && cfg.list_url.trim().length > 0
      ? cfg.list_url
      : typeof cfg?.url === "string" && cfg.url.trim().length > 0
        ? cfg.url
        : "";
  const listUrl = configListUrl || collection?.source_url || "";
  const limit =
    typeof cfg?.limit === "number" && Number.isFinite(cfg.limit) && cfg.limit > 0
      ? String(cfg.limit)
      : "";
  return { sourceKind, preset, mediaType, profileId, listUrl, limit };
}

export function buildTMDBPresetSourceInput({
  preset,
  mediaType,
  timeWindow,
  limit,
}: TMDBPresetSourceConfig): {
  source_url: string;
  source_config: Record<string, unknown>;
} {
  const normalizedMediaType = normalizeTMDBPresetMediaType(preset, mediaType);
  const parsedLimit = parseOptionalPositiveInteger(limit);
  const source_config: Record<string, unknown> = {
    mode: "tmdb_preset",
    preset,
    media_type: normalizedMediaType,
  };
  if (tmdbPresetNeedsTimeWindow(preset)) {
    source_config.time_window = timeWindow;
  }
  if (parsedLimit !== undefined) {
    source_config.limit = parsedLimit;
  }

  return {
    source_url: tmdbPresetNeedsTimeWindow(preset)
      ? `tmdb://${preset}/${normalizedMediaType}/${timeWindow}`
      : `tmdb://${preset}/${normalizedMediaType}`,
    source_config,
  };
}

function buildTraktListSourceInput({ listUrl, limit }: { listUrl: string; limit: string }): {
  source_url: string;
  source_config: Record<string, unknown>;
} {
  const trimmedListUrl = listUrl.trim();
  const parsedLimit = parseOptionalPositiveInteger(limit);
  const source_config: Record<string, unknown> = {
    mode: "trakt_list",
    provider: "trakt",
    url: trimmedListUrl,
    list_url: trimmedListUrl,
  };
  if (parsedLimit !== undefined) {
    source_config.limit = parsedLimit;
  }

  return {
    source_url: trimmedListUrl,
    source_config,
  };
}

export function LibraryPicker({
  libraries,
  value,
  onChange,
}: {
  libraries: Library[];
  value: number | null;
  onChange: (libraryId: number) => void;
}) {
  return (
    <Select
      value={value ? String(value) : undefined}
      onValueChange={(next) => onChange(Number(next))}
    >
      <SelectTrigger className="w-full sm:w-[220px]">
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

// Accepts any library shape with id/name and an optional kind/type, so the
// same picker works for the admin Library type and the trimmed UserLibrary.
export function CollectionLibraryPicker({
  libraries,
  value,
  onChange,
  eligibility,
}: {
  libraries: Array<{ id: number; name: string; type?: string }>;
  value: number[];
  onChange: (libraryIds: number[]) => void;
  eligibility?: LibraryEligibility;
}) {
  return (
    <LibraryMultiSelect
      libraries={libraries}
      value={value}
      onChange={onChange}
      eligibleKinds={eligibility?.kinds}
      hideAllOption
      emptyLabel="Choose libraries"
      ineligibleReason={eligibility?.hint}
    />
  );
}

function AdminCollectionSummary({
  value,
  collection,
  libraries,
  sourceLabel,
}: {
  value: CollectionBuilderValue;
  collection: LibraryCollection | null;
  libraries: Library[];
  sourceLabel: string;
}) {
  const selectedLibraries = libraries
    .filter((library) => value.query_definition.library_ids.includes(library.id))
    .map((library) => library.name);

  return (
    <Card className="surface-panel gap-0 rounded-[1.5rem] border-0 shadow-none">
      <CardHeader>
        <CardTitle>Collection Summary</CardTitle>
        <CardDescription>Keep the important state visible while you build.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <SummaryRow label="Mode" value={value.collection_type === "smart" ? "Smart" : "Manual"} />
        <SummaryRow label="Source" value={sourceLabel} />
        <SummaryRow
          label="Visibility"
          value={value.visibility === "visible" ? "Visible" : "Hidden"}
        />
        <SummaryRow label="Featured" value={value.featured ? "Yes" : "No"} />
        <SummaryRow
          label="Libraries"
          value={selectedLibraries.length > 0 ? selectedLibraries.join(", ") : "None selected"}
        />
        {collection ? (
          <SummaryRow
            label="Items"
            value={
              collection.collection_type === "smart" ? "\u2014" : String(collection.item_count)
            }
          />
        ) : null}
      </CardContent>
    </Card>
  );
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="max-w-[16rem] text-right font-medium">{value}</span>
    </div>
  );
}

export function CollectionForm({
  libraries,
  collection,
  initialLibraryId,
  onClose,
}: {
  libraries: Library[];
  collection: LibraryCollection | null;
  initialLibraryId: number | null;
  onClose: () => void;
}) {
  const createMutation = useCreateAdminCollection();
  const updateMutation = useUpdateAdminCollection();
  const [draft, setDraft] = useState(() =>
    toAdminCollectionBuilderValue(collection, initialLibraryId),
  );
  const [posterFile, setPosterFile] = useState<File | null>(null);
  const [backdropFile, setBackdropFile] = useState<File | null>(null);
  const [posterSourceUrl, setPosterSourceUrl] = useState("");
  const [backdropSourceUrl, setBackdropSourceUrl] = useState("");
  const deleteImage = useDeleteCollectionImage();

  useEffect(() => {
    setDraft(toAdminCollectionBuilderValue(collection, initialLibraryId));
    setPosterFile(null);
    setBackdropFile(null);
    setPosterSourceUrl("");
    setBackdropSourceUrl("");
  }, [collection, initialLibraryId]);

  const isPending = createMutation.isPending || updateMutation.isPending;

  const libraryIds = draft.query_definition.library_ids;
  const setLibraryIds = (next: number[]) =>
    setDraft({
      ...draft,
      query_definition: { ...draft.query_definition, library_ids: next },
    });

  return (
    <CollectionBuilder
      mode="admin"
      value={draft}
      onChange={setDraft}
      defaultAdvanced
      allowLibrarySelection={false}
      onSubmit={() => {
        const body = {
          ...toAdminCollectionRequest(draft),
          poster_source_url: posterSourceUrl.trim() || undefined,
          backdrop_source_url: backdropSourceUrl.trim() || undefined,
        };
        if (collection) {
          updateMutation.mutate(
            { id: collection.id, body, poster: posterFile, backdrop: backdropFile },
            { onSuccess: onClose },
          );
          return;
        }

        createMutation.mutate(
          { body, poster: posterFile, backdrop: backdropFile },
          { onSuccess: onClose },
        );
      }}
      submitLabel="Save Collection"
      libraries={libraries.map((library) => ({ id: library.id, name: library.name }))}
      isPending={isPending}
      previewLayout="sidebar"
      sidebarContent={
        <AdminCollectionSummary
          value={draft}
          collection={collection}
          libraries={libraries}
          sourceLabel="Manual / Smart"
        />
      }
    >
      <section className="space-y-4">
        <div>
          <h2 className="text-lg font-semibold">Libraries</h2>
          <p className="text-muted-foreground mt-1 text-sm">
            Choose which libraries this collection should appear in. Smart rules apply across the
            selected libraries.
          </p>
        </div>
        <CollectionLibraryPicker
          libraries={libraries}
          value={libraryIds}
          onChange={setLibraryIds}
        />
      </section>

      <section className="space-y-4">
        <div>
          <h2 className="text-lg font-semibold">Artwork</h2>
          <p className="text-muted-foreground mt-1 text-sm">
            Upload collection art that will be reused across library surfaces.
          </p>
        </div>
        <div className="grid gap-4 md:grid-cols-2">
          <ImageUploadField
            label="Poster"
            currentUrl={collection?.poster_url}
            file={posterFile}
            onFileChange={setPosterFile}
            sourceUrl={posterSourceUrl}
            onSourceUrlChange={setPosterSourceUrl}
            onDelete={
              collection
                ? () =>
                    deleteImage.mutate({
                      id: collection.id,
                      type: "poster",
                      libraryId: collection.library_id,
                    })
                : undefined
            }
          />
          <ImageUploadField
            label="Backdrop"
            currentUrl={collection?.backdrop_url}
            file={backdropFile}
            onFileChange={setBackdropFile}
            sourceUrl={backdropSourceUrl}
            onSourceUrlChange={setBackdropSourceUrl}
            onDelete={
              collection
                ? () =>
                    deleteImage.mutate({
                      id: collection.id,
                      type: "backdrop",
                      libraryId: collection.library_id,
                    })
                : undefined
            }
          />
        </div>
      </section>
    </CollectionBuilder>
  );
}

export type CollectionSourcePick = CollectionSourceType | "templates";

export function SourceTypeSelector({
  onSelect,
  showTemplates = false,
}: {
  onSelect: (type: CollectionSourcePick) => void;
  showTemplates?: boolean;
}) {
  const options: {
    type: CollectionSourcePick;
    icon: typeof ListPlus;
    label: string;
    subtitle: string;
    highlight?: boolean;
  }[] = [];

  if (showTemplates) {
    options.push({
      type: "templates",
      icon: Sparkles,
      label: "Browse Templates",
      subtitle: "Start from a curated TMDB, Trakt, or MDBList preset",
      highlight: true,
    });
  }

  options.push(
    { type: "manual", icon: ListPlus, label: "Manual", subtitle: "Curate items by hand" },
    { type: "mdblist", icon: Download, label: "MDBList", subtitle: "Sync from an MDBList URL" },
    { type: "tmdb", icon: TrendingUp, label: "TMDB", subtitle: "Auto-populate from TMDB presets" },
    {
      type: "trakt",
      icon: TrendingUp,
      label: "Trakt",
      subtitle: "Sync trending, popular, and profile recommendations",
    },
  );

  return (
    <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
      {options.map((opt) => (
        <button
          key={opt.type}
          type="button"
          onClick={() => onSelect(opt.type)}
          className={
            "flex flex-col items-start gap-3 rounded-2xl border p-5 text-left transition-colors " +
            (opt.highlight
              ? "border-primary/60 bg-primary/5 hover:border-primary hover:bg-primary/10"
              : "border-border hover:border-primary hover:bg-accent")
          }
        >
          <opt.icon
            className={opt.highlight ? "text-primary h-8 w-8" : "text-muted-foreground h-8 w-8"}
          />
          <div>
            <p className="text-sm font-medium">{opt.label}</p>
            <p className="text-muted-foreground mt-1 text-xs">{opt.subtitle}</p>
          </div>
        </button>
      ))}
    </div>
  );
}

function ExternalEditorShell({
  title,
  description,
  summary,
  children,
}: {
  title: string;
  description: string;
  summary: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_20rem]">
      <Card>
        <CardHeader>
          <CardTitle>{title}</CardTitle>
          <CardDescription>{description}</CardDescription>
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
      <div className="xl:sticky xl:top-6 xl:self-start">{summary}</div>
    </div>
  );
}

export function TMDBPresetForm({
  libraries,
  initialLibraryId,
  onClose,
}: {
  libraries: Library[];
  initialLibraryId: number | null;
  onClose: () => void;
}) {
  const mutation = useImportTMDBCollection();
  const [libraryIds, setLibraryIds] = useState<number[]>(() =>
    initialLibraryId ? [initialLibraryId] : libraries[0]?.id ? [libraries[0].id] : [],
  );
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [preset, setPreset] = useState<TMDBPreset>("trending");
  const [timeWindow, setTimeWindow] = useState<TMDBTimeWindow>("day");
  const [mediaType, setMediaType] = useState<TMDBMediaType>("all");
  const [limit, setLimit] = useState("");
  const [featured, setFeatured] = useState(true);
  const [posterFile, setPosterFile] = useState<File | null>(null);
  const [backdropFile, setBackdropFile] = useState<File | null>(null);
  const [posterSourceUrl, setPosterSourceUrl] = useState("");
  const [backdropSourceUrl, setBackdropSourceUrl] = useState("");
  const [tmdbSyncSchedule, setTmdbSyncSchedule] = useState("");
  const parsedLimit = parseOptionalPositiveInteger(limit);
  const hasInvalidLimit = limit.trim().length > 0 && parsedLimit === undefined;
  const allowedMediaTypes = getTMDBAllowedMediaTypes(preset);
  const normalizedMediaType = normalizeTMDBPresetMediaType(preset, mediaType);
  const eligibility = libraryEligibilityForMediaKind(normalizedMediaType);

  useEffect(() => {
    if (mediaType !== normalizedMediaType) {
      setMediaType(normalizedMediaType);
    }
  }, [mediaType, normalizedMediaType]);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate(
      {
        body: {
          library_ids: libraryIds,
          title,
          description,
          preset,
          time_window: tmdbPresetNeedsTimeWindow(preset) ? timeWindow : undefined,
          media_type: normalizedMediaType,
          limit: parsedLimit,
          featured,
          poster_source_url: posterSourceUrl.trim() || undefined,
          backdrop_source_url: backdropSourceUrl.trim() || undefined,
          sync_schedule: tmdbSyncSchedule.trim() || undefined,
        },
        poster: posterFile,
        backdrop: backdropFile,
      },
      { onSuccess: onClose },
    );
  }

  return (
    <ExternalEditorShell
      title="TMDB Collection"
      description="Choose a TMDB preset once, then let sync keep the shelf fresh."
      summary={
        <Card className="gap-0">
          <CardHeader>
            <CardTitle>Import Summary</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <SummaryRow label="Preset" value={getTMDBPresetLabel(preset)} />
            {tmdbPresetNeedsTimeWindow(preset) ? (
              <SummaryRow label="Window" value={timeWindow === "day" ? "Daily" : "Weekly"} />
            ) : null}
            <SummaryRow
              label="Media"
              value={
                normalizedMediaType === "all"
                  ? "All"
                  : normalizedMediaType === "tv"
                    ? "TV Shows"
                    : "Movies"
              }
            />
            <SummaryRow label="Featured" value={featured ? "Yes" : "No"} />
          </CardContent>
        </Card>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Libraries</Label>
            <CollectionLibraryPicker
              libraries={libraries}
              value={libraryIds}
              onChange={setLibraryIds}
              eligibility={eligibility}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="tmdb-title">Collection Title</Label>
            <Input
              id="tmdb-title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder="Trending Movies This Week"
              required
            />
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="tmdb-description">Description</Label>
          <Input
            id="tmdb-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="Optional collection summary"
          />
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Preset</Label>
            <Select value={preset} onValueChange={(v) => setPreset(v as TMDBPreset)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="trending">Trending</SelectItem>
                <SelectItem value="popular">Popular</SelectItem>
                <SelectItem value="top_rated">Top Rated</SelectItem>
                <SelectItem value="now_playing">Now Playing</SelectItem>
                <SelectItem value="upcoming">Upcoming</SelectItem>
                <SelectItem value="airing_today">Airing Today</SelectItem>
                <SelectItem value="on_the_air">On The Air</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {tmdbPresetNeedsTimeWindow(preset) ? (
            <div className="space-y-2">
              <Label>Time Window</Label>
              <Select value={timeWindow} onValueChange={(v) => setTimeWindow(v as TMDBTimeWindow)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="day">Daily</SelectItem>
                  <SelectItem value="week">Weekly</SelectItem>
                </SelectContent>
              </Select>
            </div>
          ) : null}
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Media Type</Label>
            <Select
              value={normalizedMediaType}
              onValueChange={(v) => setMediaType(v as TMDBMediaType)}
              disabled={allowedMediaTypes.length === 1}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {allowedMediaTypes.includes("all") ? (
                  <SelectItem value="all">All</SelectItem>
                ) : null}
                {allowedMediaTypes.includes("movie") ? (
                  <SelectItem value="movie">Movies</SelectItem>
                ) : null}
                {allowedMediaTypes.includes("tv") ? (
                  <SelectItem value="tv">TV Shows</SelectItem>
                ) : null}
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="tmdb-limit">Max Items</Label>
          <Input
            id="tmdb-limit"
            type="number"
            min={1}
            max={COLLECTION_MAX_ITEMS}
            step={1}
            inputMode="numeric"
            value={limit}
            onChange={(event) => setLimit(event.target.value)}
            placeholder="Defaults to 20"
          />
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <ImageUploadField
            label="Poster"
            file={posterFile}
            onFileChange={setPosterFile}
            sourceUrl={posterSourceUrl}
            onSourceUrlChange={setPosterSourceUrl}
          />
          <ImageUploadField
            label="Backdrop"
            file={backdropFile}
            onFileChange={setBackdropFile}
            sourceUrl={backdropSourceUrl}
            onSourceUrlChange={setBackdropSourceUrl}
          />
        </div>

        <SyncScheduleField value={tmdbSyncSchedule} onChange={setTmdbSyncSchedule} />

        <div className="border-border flex items-center justify-between rounded-lg border px-4 py-3">
          <div>
            <p className="text-sm font-medium">Feature on library tab</p>
            <p className="text-muted-foreground text-xs">
              Surface this collection in the hero shelves.
            </p>
          </div>
          <Switch checked={featured} onCheckedChange={setFeatured} />
        </div>

        <Button
          type="submit"
          className="w-full"
          disabled={mutation.isPending || libraryIds.length === 0 || hasInvalidLimit}
        >
          {mutation.isPending ? "Importing..." : "Import TMDB Collection"}
        </Button>
      </form>
    </ExternalEditorShell>
  );
}

export function TraktPresetForm({
  libraries,
  initialLibraryId,
  onClose,
}: {
  libraries: Library[];
  initialLibraryId: number | null;
  onClose: () => void;
}) {
  const mutation = useImportTraktCollection();
  const { data: profiles = [] } = useProfiles();
  const [libraryIds, setLibraryIds] = useState<number[]>(() =>
    initialLibraryId ? [initialLibraryId] : libraries[0]?.id ? [libraries[0].id] : [],
  );
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [sourceKind, setSourceKind] = useState<TraktSourceKind>("preset");
  const [listUrl, setListUrl] = useState("");
  const [preset, setPreset] = useState<TraktPreset>("trending");
  const [mediaType, setMediaType] = useState<TraktMediaType>("movie");
  const [profileId, setProfileId] = useState("");
  const [limit, setLimit] = useState("");
  const [featured, setFeatured] = useState(true);
  const [posterFile, setPosterFile] = useState<File | null>(null);
  const [backdropFile, setBackdropFile] = useState<File | null>(null);
  const [posterSourceUrl, setPosterSourceUrl] = useState("");
  const [backdropSourceUrl, setBackdropSourceUrl] = useState("");
  const [syncSchedule, setSyncSchedule] = useState("");
  const parsedLimit = parseOptionalPositiveInteger(limit);
  const hasInvalidLimit = limit.trim().length > 0 && parsedLimit === undefined;
  const isListMode = sourceKind === "list";
  const eligibility = libraryEligibilityForMediaKind(isListMode ? "mixed" : mediaType);
  const requiresProfile = !isListMode && preset === "recommended";
  const missingProfile = requiresProfile && profileId.trim().length === 0;
  const missingListURL = isListMode && listUrl.trim().length === 0;

  useEffect(() => {
    const firstProfileID = profiles[0]?.id;
    if (requiresProfile && profileId === "" && firstProfileID) {
      setProfileId(firstProfileID);
    }
  }, [profileId, profiles, requiresProfile]);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate(
      {
        body: {
          library_ids: libraryIds,
          title,
          description,
          ...(isListMode
            ? { list_url: listUrl.trim() }
            : {
                preset,
                media_type: mediaType,
                profile_id: requiresProfile ? profileId : undefined,
              }),
          limit: parsedLimit,
          featured,
          poster_source_url: posterSourceUrl.trim() || undefined,
          backdrop_source_url: backdropSourceUrl.trim() || undefined,
          sync_schedule: syncSchedule.trim() || undefined,
        },
        poster: posterFile,
        backdrop: backdropFile,
      },
      { onSuccess: onClose },
    );
  }

  return (
    <ExternalEditorShell
      title="Trakt Collection"
      description="Sync shelves from Trakt discovery feeds and profile recommendations."
      summary={
        <Card className="gap-0">
          <CardHeader>
            <CardTitle>Import Summary</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            {isListMode ? (
              <SummaryRow label="Source" value="User list" />
            ) : (
              <>
                <SummaryRow label="Preset" value={getTraktPresetLabel(preset)} />
                <SummaryRow label="Media" value={mediaType === "tv" ? "TV Shows" : "Movies"} />
              </>
            )}
            {requiresProfile ? (
              <SummaryRow
                label="Profile"
                value={profiles.find((profile) => profile.id === profileId)?.name ?? "Required"}
              />
            ) : null}
            <SummaryRow label="Featured" value={featured ? "Yes" : "No"} />
          </CardContent>
        </Card>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Libraries</Label>
            <CollectionLibraryPicker
              libraries={libraries}
              value={libraryIds}
              onChange={setLibraryIds}
              eligibility={eligibility}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="trakt-title">Collection Title</Label>
            <Input
              id="trakt-title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder="Trakt Trending Movies"
              required
            />
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="trakt-description">Description</Label>
          <Input
            id="trakt-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="Optional collection summary"
          />
        </div>

        <div className="space-y-2">
          <Label>Source</Label>
          <Select value={sourceKind} onValueChange={(v) => setSourceKind(v as TraktSourceKind)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="preset">
                Discovery feed (Trending / Popular / Recommended)
              </SelectItem>
              <SelectItem value="list">User list (trakt.tv URL)</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {isListMode ? (
          <div className="space-y-2">
            <Label htmlFor="trakt-list-url">Trakt list URL</Label>
            <Input
              id="trakt-list-url"
              value={listUrl}
              onChange={(event) => setListUrl(event.target.value)}
              placeholder="https://trakt.tv/users/jjjonesjr33/lists/saw-cinematic-universe-in-timeline-order"
              required
            />
            <p className="text-muted-foreground text-xs">
              Paste a public Trakt list URL. Movies and shows in the list are matched against the
              selected libraries in list order.
            </p>
          </div>
        ) : (
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Preset</Label>
              <Select value={preset} onValueChange={(v) => setPreset(v as TraktPreset)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="trending">Trending</SelectItem>
                  <SelectItem value="popular">Popular</SelectItem>
                  <SelectItem value="recommended">Recommended</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>Media Type</Label>
              <Select value={mediaType} onValueChange={(v) => setMediaType(v as TraktMediaType)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="movie">Movies</SelectItem>
                  <SelectItem value="tv">TV Shows</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
        )}

        {requiresProfile ? (
          <div className="space-y-2">
            <Label>Profile</Label>
            <Select value={profileId} onValueChange={setProfileId}>
              <SelectTrigger>
                <SelectValue placeholder="Choose a profile" />
              </SelectTrigger>
              <SelectContent>
                {profiles.map((profile) => (
                  <SelectItem key={profile.id} value={profile.id}>
                    {profile.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-xs">
              Recommended collections use this profile's existing Trakt connection from Watch
              Providers settings.
            </p>
          </div>
        ) : null}

        <div className="space-y-2">
          <Label htmlFor="trakt-limit">Max Items</Label>
          <Input
            id="trakt-limit"
            type="number"
            min={1}
            max={COLLECTION_MAX_ITEMS}
            step={1}
            inputMode="numeric"
            value={limit}
            onChange={(event) => setLimit(event.target.value)}
            placeholder="Defaults to 20"
          />
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <ImageUploadField
            label="Poster"
            file={posterFile}
            onFileChange={setPosterFile}
            sourceUrl={posterSourceUrl}
            onSourceUrlChange={setPosterSourceUrl}
          />
          <ImageUploadField
            label="Backdrop"
            file={backdropFile}
            onFileChange={setBackdropFile}
            sourceUrl={backdropSourceUrl}
            onSourceUrlChange={setBackdropSourceUrl}
          />
        </div>

        <SyncScheduleField value={syncSchedule} onChange={setSyncSchedule} />

        <div className="border-border flex items-center justify-between rounded-lg border px-4 py-3">
          <div>
            <p className="text-sm font-medium">Feature on library tab</p>
            <p className="text-muted-foreground text-xs">
              Surface this collection in the hero shelves.
            </p>
          </div>
          <Switch checked={featured} onCheckedChange={setFeatured} />
        </div>

        <Button
          type="submit"
          className="w-full"
          disabled={
            mutation.isPending ||
            libraryIds.length === 0 ||
            hasInvalidLimit ||
            missingProfile ||
            missingListURL
          }
        >
          {mutation.isPending ? "Importing..." : "Import Trakt Collection"}
        </Button>
      </form>
    </ExternalEditorShell>
  );
}

export function MDBListImportForm({
  libraries,
  initialLibraryId,
  onClose,
}: {
  libraries: Library[];
  initialLibraryId: number | null;
  onClose: () => void;
}) {
  const mutation = useImportMDBListCollection();
  const [libraryIds, setLibraryIds] = useState<number[]>(() =>
    initialLibraryId ? [initialLibraryId] : libraries[0]?.id ? [libraries[0].id] : [],
  );
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [url, setURL] = useState("");
  const [limit, setLimit] = useState("");
  const [featured, setFeatured] = useState(true);
  const [posterFile, setPosterFile] = useState<File | null>(null);
  const [backdropFile, setBackdropFile] = useState<File | null>(null);
  const [posterSourceUrl, setPosterSourceUrl] = useState("");
  const [backdropSourceUrl, setBackdropSourceUrl] = useState("");
  const [syncSchedule, setSyncSchedule] = useState("");
  const parsedLimit = parseOptionalPositiveInteger(limit);
  const hasInvalidLimit = limit.trim().length > 0 && parsedLimit === undefined;

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate(
      {
        body: {
          library_ids: libraryIds,
          title,
          description,
          url,
          limit: parsedLimit,
          featured,
          poster_source_url: posterSourceUrl.trim() || undefined,
          backdrop_source_url: backdropSourceUrl.trim() || undefined,
          sync_schedule: syncSchedule.trim() || undefined,
        },
        poster: posterFile,
        backdrop: backdropFile,
      },
      { onSuccess: onClose },
    );
  }

  return (
    <ExternalEditorShell
      title="MDBList Import"
      description="Connect a remote list and keep a curated shelf synced from it."
      summary={
        <Card className="gap-0">
          <CardHeader>
            <CardTitle>Import Summary</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <SummaryRow label="Source" value={url || "MDBList JSON feed"} />
            <SummaryRow label="Featured" value={featured ? "Yes" : "No"} />
            <SummaryRow label="Limit" value={limit || "All items"} />
          </CardContent>
        </Card>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Libraries</Label>
            <CollectionLibraryPicker
              libraries={libraries}
              value={libraryIds}
              onChange={setLibraryIds}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="mdblist-title">Collection Title</Label>
            <Input
              id="mdblist-title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder="Top Watched Movies"
              required
            />
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="mdblist-url">MDBList JSON URL</Label>
          <Input
            id="mdblist-url"
            value={url}
            onChange={(event) => setURL(event.target.value)}
            placeholder="https://mdblist.com/lists/.../json"
            required
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="mdblist-description">Description</Label>
          <Input
            id="mdblist-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="Optional collection summary"
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="mdblist-limit">Max Items</Label>
          <Input
            id="mdblist-limit"
            type="number"
            min={1}
            step={1}
            inputMode="numeric"
            value={limit}
            onChange={(event) => setLimit(event.target.value)}
            placeholder="Leave blank for all items"
          />
          <p className="text-muted-foreground text-xs">
            Store only the first N items from the remote list.
          </p>
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <ImageUploadField
            label="Poster"
            file={posterFile}
            onFileChange={setPosterFile}
            sourceUrl={posterSourceUrl}
            onSourceUrlChange={setPosterSourceUrl}
          />
          <ImageUploadField
            label="Backdrop"
            file={backdropFile}
            onFileChange={setBackdropFile}
            sourceUrl={backdropSourceUrl}
            onSourceUrlChange={setBackdropSourceUrl}
          />
        </div>

        <SyncScheduleField value={syncSchedule} onChange={setSyncSchedule} />

        <div className="border-border flex items-center justify-between rounded-lg border px-4 py-3">
          <div>
            <p className="text-sm font-medium">Feature on library tab</p>
            <p className="text-muted-foreground text-xs">
              New imports can immediately surface in the hero shelves.
            </p>
          </div>
          <Switch checked={featured} onCheckedChange={setFeatured} />
        </div>

        <Button
          type="submit"
          className="w-full"
          disabled={mutation.isPending || libraryIds.length === 0 || hasInvalidLimit}
        >
          {mutation.isPending ? "Importing..." : "Import MDBList Collection"}
        </Button>
      </form>
    </ExternalEditorShell>
  );
}

export function CollectionEditForm({
  libraries,
  collection,
  initialLibraryId,
  onClose,
}: {
  libraries: Library[];
  collection: LibraryCollection;
  initialLibraryId: number | null;
  onClose: () => void;
}) {
  const [libraryIds, setLibraryIds] = useState<number[]>(() => {
    if (collection.library_ids && collection.library_ids.length > 0) return collection.library_ids;
    if (collection.library_id) return [collection.library_id];
    if (initialLibraryId) return [initialLibraryId];
    return libraries[0]?.id ? [libraries[0].id] : [];
  });
  const [title, setTitle] = useState(collection.title ?? "");
  const [description, setDescription] = useState(collection.description ?? "");
  const [featured, setFeatured] = useState(collection.featured ?? false);
  const [visibility, setVisibility] = useState<"visible" | "hidden">(collection.visibility);
  const [posterFile, setPosterFile] = useState<File | null>(null);
  const [backdropFile, setBackdropFile] = useState<File | null>(null);
  const [posterSourceUrl, setPosterSourceUrl] = useState("");
  const [backdropSourceUrl, setBackdropSourceUrl] = useState("");
  const deleteImage = useDeleteCollectionImage();
  const updateMutation = useUpdateAdminCollection();
  const { data: profiles = [] } = useProfiles();
  const [sourceUrl, setSourceUrl] = useState(collection.source_url ?? "");
  const [sourceLimit, setSourceLimit] = useState(getMDBListLimitValue(collection));
  const [editSyncSchedule, setEditSyncSchedule] = useState(collection.sync_schedule ?? "");

  const tmdbDefaults = parseTMDBPresetSourceConfig(collection);
  const [tmdbPreset, setTmdbPreset] = useState<TMDBPreset>(tmdbDefaults.preset);
  const [tmdbTimeWindow, setTmdbTimeWindow] = useState<TMDBTimeWindow>(tmdbDefaults.timeWindow);
  const [tmdbMediaType, setTmdbMediaType] = useState<TMDBMediaType>(tmdbDefaults.mediaType);
  const [tmdbLimit, setTmdbLimit] = useState(tmdbDefaults.limit);
  const traktDefaults = parseTraktPresetSourceConfig(collection);
  const [traktSourceKind, setTraktSourceKind] = useState<TraktSourceKind>(traktDefaults.sourceKind);
  const [traktListUrl, setTraktListUrl] = useState(traktDefaults.listUrl);
  const [traktPreset, setTraktPreset] = useState<TraktPreset>(traktDefaults.preset);
  const [traktMediaType, setTraktMediaType] = useState<TraktMediaType>(traktDefaults.mediaType);
  const [traktProfileId, setTraktProfileId] = useState(traktDefaults.profileId);
  const [traktLimit, setTraktLimit] = useState(traktDefaults.limit);

  const isMDBListCollection = collection.collection_type === "mdblist";
  const isTMDBCollection = collection.collection_type === "tmdb";
  const isTraktCollection = collection.collection_type === "trakt";
  const parsedSourceLimit = parseOptionalPositiveInteger(sourceLimit);
  const hasInvalidSourceLimit = sourceLimit.trim().length > 0 && parsedSourceLimit === undefined;
  const missingSourceURL = isMDBListCollection && sourceUrl.trim().length === 0;
  const parsedTmdbLimit = parseOptionalPositiveInteger(tmdbLimit);
  const hasInvalidTmdbLimit = tmdbLimit.trim().length > 0 && parsedTmdbLimit === undefined;
  const parsedTraktLimit = parseOptionalPositiveInteger(traktLimit);
  const hasInvalidTraktLimit = traktLimit.trim().length > 0 && parsedTraktLimit === undefined;
  const isTraktListMode = isTraktCollection && traktSourceKind === "list";
  const traktNeedsProfile = !isTraktListMode && traktPreset === "recommended";
  const missingTraktProfile = isTraktCollection && traktNeedsProfile && traktProfileId === "";
  const missingTraktListURL = isTraktListMode && traktListUrl.trim().length === 0;
  const allowedTMDBMediaTypes = getTMDBAllowedMediaTypes(tmdbPreset);
  const normalizedTMDBMediaType = normalizeTMDBPresetMediaType(tmdbPreset, tmdbMediaType);
  const editEligibility: LibraryEligibility | undefined = isTMDBCollection
    ? libraryEligibilityForMediaKind(normalizedTMDBMediaType)
    : isTraktCollection
      ? libraryEligibilityForMediaKind(isTraktListMode ? "mixed" : traktMediaType)
      : undefined;

  useEffect(() => {
    if (tmdbMediaType !== normalizedTMDBMediaType) {
      setTmdbMediaType(normalizedTMDBMediaType);
    }
  }, [normalizedTMDBMediaType, tmdbMediaType]);

  useEffect(() => {
    const firstProfileID = profiles[0]?.id;
    if (isTraktCollection && traktNeedsProfile && traktProfileId === "" && firstProfileID) {
      setTraktProfileId(firstProfileID);
    }
  }, [isTraktCollection, profiles, traktNeedsProfile, traktProfileId]);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();

    let sourceConfig: Record<string, unknown> | undefined = collection.source_config;
    let sourceUrlValue: string | undefined;

    if (isMDBListCollection) {
      sourceUrlValue = sourceUrl;
      sourceConfig = {
        mode: "mdblist_json",
        url: sourceUrl,
        ...(parsedSourceLimit ? { limit: parsedSourceLimit } : {}),
      };
    } else if (isTMDBCollection) {
      const tmdbSource = buildTMDBPresetSourceInput({
        preset: tmdbPreset,
        mediaType: normalizedTMDBMediaType,
        timeWindow: tmdbTimeWindow,
        limit: tmdbLimit,
      });
      sourceUrlValue = tmdbSource.source_url;
      sourceConfig = tmdbSource.source_config;
    } else if (isTraktCollection) {
      if (isTraktListMode) {
        const traktListSource = buildTraktListSourceInput({
          listUrl: traktListUrl,
          limit: traktLimit,
        });
        sourceUrlValue = traktListSource.source_url;
        sourceConfig = traktListSource.source_config;
      } else {
        sourceUrlValue =
          traktPreset === "recommended"
            ? `trakt://${traktPreset}/${traktMediaType}/${traktProfileId}`
            : `trakt://${traktPreset}/${traktMediaType}`;
        sourceConfig = {
          mode: "trakt_preset",
          provider: "trakt",
          preset: traktPreset,
          media_type: traktMediaType,
          ...(traktPreset === "recommended" ? { profile_id: traktProfileId } : {}),
          ...(parsedTraktLimit ? { limit: parsedTraktLimit } : {}),
        };
      }
    }

    const body: CreateLibraryCollectionRequest = {
      library_ids: libraryIds,
      title,
      description,
      featured,
      visibility,
      collection_type: collection.collection_type,
      poster_source_url: posterSourceUrl.trim() || undefined,
      backdrop_source_url: backdropSourceUrl.trim() || undefined,
      source_url: sourceUrlValue,
      source_config: sourceConfig,
      sync_schedule: editSyncSchedule.trim(),
    };

    updateMutation.mutate(
      { id: collection.id, body, poster: posterFile, backdrop: backdropFile },
      { onSuccess: onClose },
    );
  }

  if (!isMDBListCollection && !isTMDBCollection && !isTraktCollection) {
    return (
      <CollectionForm
        libraries={libraries}
        collection={collection}
        initialLibraryId={initialLibraryId}
        onClose={onClose}
      />
    );
  }

  const sourceLabel = isMDBListCollection ? "MDBList" : isTMDBCollection ? "TMDB" : "Trakt";

  return (
    <ExternalEditorShell
      title={`Edit ${sourceLabel}`}
      description="Update the source configuration, artwork, and visibility for this collection."
      summary={
        <Card className="gap-0">
          <CardHeader>
            <CardTitle>Collection Summary</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <SummaryRow label="Source" value={sourceLabel} />
            <SummaryRow label="Visibility" value={visibility} />
            <SummaryRow label="Featured" value={featured ? "Yes" : "No"} />
            <SummaryRow
              label="Items"
              value={
                collection.collection_type === "smart" ? "\u2014" : String(collection.item_count)
              }
            />
          </CardContent>
        </Card>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Libraries</Label>
            <CollectionLibraryPicker
              libraries={libraries}
              value={libraryIds}
              onChange={setLibraryIds}
              eligibility={editEligibility}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="collection-title">Title</Label>
            <Input
              id="collection-title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              required
            />
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="collection-description">Description</Label>
          <textarea
            id="collection-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            rows={4}
            className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 flex min-h-[110px] w-full rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px]"
          />
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <ImageUploadField
            label="Poster"
            currentUrl={collection.poster_url}
            file={posterFile}
            onFileChange={setPosterFile}
            sourceUrl={posterSourceUrl}
            onSourceUrlChange={setPosterSourceUrl}
            onDelete={() =>
              deleteImage.mutate({
                id: collection.id,
                type: "poster",
                libraryId: collection.library_id,
              })
            }
          />
          <ImageUploadField
            label="Backdrop"
            currentUrl={collection.backdrop_url}
            file={backdropFile}
            onFileChange={setBackdropFile}
            sourceUrl={backdropSourceUrl}
            onSourceUrlChange={setBackdropSourceUrl}
            onDelete={() =>
              deleteImage.mutate({
                id: collection.id,
                type: "backdrop",
                libraryId: collection.library_id,
              })
            }
          />
        </div>

        {isMDBListCollection ? (
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2 md:col-span-2">
              <Label htmlFor="collection-source-url">MDBList JSON URL</Label>
              <Input
                id="collection-source-url"
                value={sourceUrl}
                onChange={(event) => setSourceUrl(event.target.value)}
                placeholder="https://mdblist.com/lists/.../json"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="collection-source-limit">Max Items</Label>
              <Input
                id="collection-source-limit"
                type="number"
                min={1}
                step={1}
                inputMode="numeric"
                value={sourceLimit}
                onChange={(event) => setSourceLimit(event.target.value)}
                placeholder="Leave blank for all items"
              />
            </div>
          </div>
        ) : null}

        <SyncScheduleField value={editSyncSchedule} onChange={setEditSyncSchedule} />

        {isTMDBCollection ? (
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Preset</Label>
              <Select value={tmdbPreset} onValueChange={(v) => setTmdbPreset(v as TMDBPreset)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="trending">Trending</SelectItem>
                  <SelectItem value="popular">Popular</SelectItem>
                  <SelectItem value="top_rated">Top Rated</SelectItem>
                  <SelectItem value="now_playing">Now Playing</SelectItem>
                  <SelectItem value="upcoming">Upcoming</SelectItem>
                  <SelectItem value="airing_today">Airing Today</SelectItem>
                  <SelectItem value="on_the_air">On The Air</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {tmdbPresetNeedsTimeWindow(tmdbPreset) ? (
              <div className="space-y-2">
                <Label>Time Window</Label>
                <Select
                  value={tmdbTimeWindow}
                  onValueChange={(v) => setTmdbTimeWindow(v as TMDBTimeWindow)}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="day">Daily</SelectItem>
                    <SelectItem value="week">Weekly</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            ) : null}
            <div className="space-y-2">
              <Label>Media Type</Label>
              <Select
                value={normalizedTMDBMediaType}
                onValueChange={(v) => setTmdbMediaType(v as TMDBMediaType)}
                disabled={allowedTMDBMediaTypes.length === 1}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {allowedTMDBMediaTypes.includes("all") ? (
                    <SelectItem value="all">All</SelectItem>
                  ) : null}
                  {allowedTMDBMediaTypes.includes("movie") ? (
                    <SelectItem value="movie">Movies</SelectItem>
                  ) : null}
                  {allowedTMDBMediaTypes.includes("tv") ? (
                    <SelectItem value="tv">TV Shows</SelectItem>
                  ) : null}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="collection-tmdb-limit">Max Items</Label>
              <Input
                id="collection-tmdb-limit"
                type="number"
                min={1}
                max={COLLECTION_MAX_ITEMS}
                step={1}
                inputMode="numeric"
                value={tmdbLimit}
                onChange={(event) => setTmdbLimit(event.target.value)}
                placeholder="Defaults to 20"
              />
            </div>
          </div>
        ) : null}

        {isTraktCollection ? (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Source</Label>
              <Select
                value={traktSourceKind}
                onValueChange={(v) => setTraktSourceKind(v as TraktSourceKind)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="preset">
                    Discovery feed (Trending / Popular / Recommended)
                  </SelectItem>
                  <SelectItem value="list">User list (trakt.tv URL)</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {isTraktListMode ? (
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2 md:col-span-2">
                  <Label htmlFor="collection-trakt-list-url">Trakt list URL</Label>
                  <Input
                    id="collection-trakt-list-url"
                    value={traktListUrl}
                    onChange={(event) => setTraktListUrl(event.target.value)}
                    placeholder="https://trakt.tv/users/jjjonesjr33/lists/saw-cinematic-universe-in-timeline-order"
                    required
                  />
                  <p className="text-muted-foreground text-xs">
                    Paste a public Trakt list URL. Movies and shows in the list are matched against
                    the selected libraries in list order.
                  </p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="collection-trakt-limit">Max Items</Label>
                  <Input
                    id="collection-trakt-limit"
                    type="number"
                    min={1}
                    max={COLLECTION_MAX_ITEMS}
                    step={1}
                    inputMode="numeric"
                    value={traktLimit}
                    onChange={(event) => setTraktLimit(event.target.value)}
                    placeholder="Defaults to 20"
                  />
                </div>
              </div>
            ) : (
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label>Preset</Label>
                  <Select
                    value={traktPreset}
                    onValueChange={(v) => setTraktPreset(v as TraktPreset)}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="trending">Trending</SelectItem>
                      <SelectItem value="popular">Popular</SelectItem>
                      <SelectItem value="recommended">Recommended</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label>Media Type</Label>
                  <Select
                    value={traktMediaType}
                    onValueChange={(v) => setTraktMediaType(v as TraktMediaType)}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="movie">Movies</SelectItem>
                      <SelectItem value="tv">TV Shows</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                {traktNeedsProfile ? (
                  <div className="space-y-2">
                    <Label>Profile</Label>
                    <Select value={traktProfileId} onValueChange={setTraktProfileId}>
                      <SelectTrigger>
                        <SelectValue placeholder="Choose a profile" />
                      </SelectTrigger>
                      <SelectContent>
                        {profiles.map((profile) => (
                          <SelectItem key={profile.id} value={profile.id}>
                            {profile.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                ) : null}
                <div className="space-y-2">
                  <Label htmlFor="collection-trakt-limit">Max Items</Label>
                  <Input
                    id="collection-trakt-limit"
                    type="number"
                    min={1}
                    max={COLLECTION_MAX_ITEMS}
                    step={1}
                    inputMode="numeric"
                    value={traktLimit}
                    onChange={(event) => setTraktLimit(event.target.value)}
                    placeholder="Defaults to 20"
                  />
                </div>
              </div>
            )}
          </div>
        ) : null}

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Visibility</Label>
            <Select
              value={visibility}
              onValueChange={(value) => setVisibility(value as "visible" | "hidden")}
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
          <div className="border-border flex items-center justify-between rounded-lg border px-4 py-3">
            <div>
              <p className="text-sm font-medium">Featured</p>
              <p className="text-muted-foreground text-xs">
                Surface this collection near the top of the library tab.
              </p>
            </div>
            <Switch checked={featured} onCheckedChange={setFeatured} />
          </div>
        </div>

        <Button
          type="submit"
          className="w-full"
          disabled={
            updateMutation.isPending ||
            libraryIds.length === 0 ||
            hasInvalidSourceLimit ||
            missingSourceURL ||
            hasInvalidTmdbLimit ||
            hasInvalidTraktLimit ||
            missingTraktListURL ||
            missingTraktProfile
          }
        >
          {updateMutation.isPending ? "Saving..." : "Save Collection"}
        </Button>
      </form>
    </ExternalEditorShell>
  );
}
