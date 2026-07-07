import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  CalendarClock,
  Clock,
  ExternalLink,
  Hash,
  Link2,
  Lock,
  RefreshCw,
  Trash2,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import type {
  Collection,
  UpdateCollectionRequest,
  UserCollectionMediaFilter,
  UserCollectionType,
  UserCollectionWatchFilter,
} from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import CollectionAccessEditor from "@/components/collections/CollectionAccessEditor";
import { ImageUploadField } from "@/components/ImageUploadField";
import {
  useDeleteCollection,
  useDeleteUserCollectionImage,
  useCollectionCapabilities,
  useUpdateCollection,
} from "@/hooks/queries/collections";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useProfiles } from "@/hooks/queries/profiles";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useSyncUserCollection } from "@/hooks/queries/userCollectionImports";
import { libraryEligibilityForMediaKind } from "@/lib/collectionTemplates";
import {
  collectionMediaFilterOptionsFromPresets,
  collectionWatchFilterOptionsFromPresets,
  displayFiltersToQueryDefinition,
  queryDefinitionToDisplayFilters,
} from "@/lib/collectionDisplayFilters";
import { CollectionLibraryPicker } from "@/pages/adminCollectionsShared";

import { isCollectionReadOnly } from "./userCollectionsShared";
import { formatDate as formatPreferredDate } from "@/lib/datetime";

type ImportedType = Extract<UserCollectionType, "mdblist" | "tmdb" | "trakt">;

interface SourceTheme {
  label: string;
  tagline: string;
  /** Hex/RGB seed used for color-mix accents. Should match the brand. */
  accent: string;
  /** Subtle gradient stops for the hero atmosphere. */
  haloFrom: string;
  haloTo: string;
  initials: string;
}

const SOURCE_THEMES: Record<ImportedType, SourceTheme> = {
  tmdb: {
    label: "TMDB",
    tagline: "The Movie Database",
    accent: "#22d3ee",
    haloFrom: "rgba(56,189,248,0.32)",
    haloTo: "rgba(16,185,129,0.18)",
    initials: "Tm",
  },
  trakt: {
    label: "Trakt",
    tagline: "Trakt.tv",
    accent: "#fb7185",
    haloFrom: "rgba(244,63,94,0.30)",
    haloTo: "rgba(251,146,60,0.18)",
    initials: "Tk",
  },
  mdblist: {
    label: "MDBList",
    tagline: "mdblist.com",
    accent: "#fbbf24",
    haloFrom: "rgba(251,191,36,0.32)",
    haloTo: "rgba(249,115,22,0.18)",
    initials: "Mb",
  },
};

interface ImportedCollectionEditorProps {
  collection: Collection;
  onClose: () => void;
}

export function ImportedCollectionEditor({ collection, onClose }: ImportedCollectionEditorProps) {
  const importedType = collection.collection_type as ImportedType;
  const theme = SOURCE_THEMES[importedType];

  const updateMutation = useUpdateCollection();
  const deleteMutation = useDeleteCollection();
  const deletePosterMutation = useDeleteUserCollectionImage();
  const syncMutation = useSyncUserCollection();
  const { data: profiles = [] } = useProfiles();
  const { data: libraries = [] } = useUserLibraries();
  const { data: collectionCapabilities } = useCollectionCapabilities();
  const { profile } = useCurrentProfile();
  const readOnly = isCollectionReadOnly(collection, profile?.id);

  const initialMaxItems = readSourceConfigLimit(collection);
  const initialSourceUrl = collection.source_url ?? "";
  const initialDescription = collection.description ?? "";
  const initialLibraryIds = readSourceConfigLibraryIDs(collection);
  const initialDisplayFilters = queryDefinitionToDisplayFilters(
    collection.display_query_definition,
  );
  const initialWatchFilter = initialDisplayFilters.watch;
  const initialMediaFilter = initialDisplayFilters.media;

  const [name, setName] = useState(collection.name);
  const [description, setDescription] = useState(initialDescription);
  const [libraryIds, setLibraryIds] = useState<number[]>(initialLibraryIds);
  const [watchFilter, setWatchFilter] = useState<UserCollectionWatchFilter>(initialWatchFilter);
  const [mediaFilter, setMediaFilter] = useState<UserCollectionMediaFilter>(initialMediaFilter);
  const [isShared, setIsShared] = useState(collection.is_shared);
  const [allowedProfileIds, setAllowedProfileIds] = useState<string[]>(
    collection.allowed_profile_ids ?? [],
  );
  const [includeOnServer, setIncludeOnServer] = useState(
    collection.include_in_server_collections ?? false,
  );
  const [sourceUrlInput, setSourceUrlInput] = useState(initialSourceUrl);
  const [maxItemsInput, setMaxItemsInput] = useState(
    initialMaxItems != null ? String(initialMaxItems) : "",
  );
  const [posterFile, setPosterFile] = useState<File | null>(null);
  const [posterSourceUrl, setPosterSourceUrl] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);

  const isMDBList = importedType === "mdblist";
  const parsedMaxItems = parseMaxItemsInput(maxItemsInput);

  const builderLibraries = useMemo(
    () => libraries.map((lib) => ({ id: lib.id, name: lib.name, type: lib.type })),
    [libraries],
  );

  const eligibility = useMemo(() => {
    const scope = collection.query_definition.media_scope;
    if (scope === "movie") return libraryEligibilityForMediaKind("movie");
    if (scope === "series") return libraryEligibilityForMediaKind("tv");
    return libraryEligibilityForMediaKind("mixed");
  }, [collection.query_definition.media_scope]);
  const watchFilterOptions = useMemo(
    () =>
      collectionWatchFilterOptionsFromPresets(
        collectionCapabilities?.display_filter_presets.watched,
      ),
    [collectionCapabilities],
  );
  const mediaFilterOptions = useMemo(
    () =>
      collectionMediaFilterOptionsFromPresets(collectionCapabilities?.display_filter_presets.media),
    [collectionCapabilities],
  );

  const isPending = updateMutation.isPending;
  const isSyncing = syncMutation.isPending;

  const trimmedPosterSource = posterSourceUrl.trim();
  const posterDirty = posterFile !== null || trimmedPosterSource !== "";
  const trimmedSourceUrl = sourceUrlInput.trim();
  const sourceUrlDirty = isMDBList && trimmedSourceUrl !== initialSourceUrl;
  const maxItemsDirty = parsedMaxItems !== initialMaxItems;
  const descriptionDirty = description !== initialDescription;
  const dirtyParts = [
    name.trim() !== collection.name,
    descriptionDirty,
    !arraysEqual(libraryIds, initialLibraryIds),
    watchFilter !== initialWatchFilter,
    mediaFilter !== initialMediaFilter,
    isShared !== collection.is_shared,
    !arraysEqual(allowedProfileIds, collection.allowed_profile_ids ?? []),
    includeOnServer !== (collection.include_in_server_collections ?? false),
    sourceUrlDirty,
    maxItemsDirty,
    posterDirty,
  ];
  const dirtyCount = dirtyParts.filter(Boolean).length;
  const dirty = dirtyCount > 0;
  const maxItemsInvalid = maxItemsInput.trim() !== "" && parsedMaxItems == null;
  const sourceUrlInvalid = sourceUrlDirty && trimmedSourceUrl === "";
  const saveBlocked = maxItemsInvalid || sourceUrlInvalid;

  function handleSave() {
    if (!dirty || readOnly || saveBlocked) return;
    const body: UpdateCollectionRequest = {
      name: name.trim() || collection.name,
      is_shared: isShared,
      allowed_profile_ids: allowedProfileIds,
      library_ids: libraryIds,
      display_query_definition: displayFiltersToQueryDefinition(watchFilter, mediaFilter),
      include_in_server_collections: includeOnServer,
      poster_source_url: trimmedPosterSource || undefined,
    };
    if (descriptionDirty) {
      body.description = description;
    }
    if (sourceUrlDirty) {
      body.source_url = trimmedSourceUrl;
    }
    if (maxItemsDirty) {
      body.max_items = parsedMaxItems ?? 0;
    }
    updateMutation.mutate(
      { id: collection.id, body, poster: posterFile },
      {
        onSuccess: () => {
          setPosterFile(null);
          setPosterSourceUrl("");
        },
      },
    );
  }

  function handleDiscard() {
    setName(collection.name);
    setDescription(initialDescription);
    setLibraryIds(initialLibraryIds);
    setWatchFilter(initialWatchFilter);
    setMediaFilter(initialMediaFilter);
    setIsShared(collection.is_shared);
    setAllowedProfileIds(collection.allowed_profile_ids ?? []);
    setIncludeOnServer(collection.include_in_server_collections ?? false);
    setSourceUrlInput(initialSourceUrl);
    setMaxItemsInput(initialMaxItems != null ? String(initialMaxItems) : "");
    setPosterFile(null);
    setPosterSourceUrl("");
  }

  function handleSyncNow() {
    if (isSyncing) return;
    syncMutation.mutate(collection.id);
  }

  function handleDelete() {
    deleteMutation.mutate(collection.id, {
      onSuccess: () => {
        onClose();
      },
    });
  }

  const sourceUrl = readableSourceURL(collection);
  const sourcePresetLabel = sourcePresetSummary(collection, theme.label);

  return (
    <form
      className="relative pb-24"
      onSubmit={(event) => {
        event.preventDefault();
        handleSave();
      }}
    >
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete collection"
        description={`Delete collection "${collection.name}"? This action cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={handleDelete}
      />

      <SourceBanner
        theme={theme}
        collection={collection}
        sourcePresetLabel={sourcePresetLabel}
        sourceUrl={sourceUrl}
        isSyncing={isSyncing}
        onSyncNow={handleSyncNow}
        readOnly={readOnly}
      />

      <div className="mt-8 grid gap-8 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="surface-panel divide-y divide-[color-mix(in_srgb,var(--border)_55%,transparent)] rounded-[1.5rem]">
          <FormSection
            number="01"
            title="Display"
            description="Rename, describe, and scope which libraries this collection's items resolve into."
          >
            <div className="space-y-2">
              <FieldLabel htmlFor="imported-collection-name">Name</FieldLabel>
              <Input
                id="imported-collection-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                required
                disabled={readOnly}
                className="h-11 text-[0.95rem]"
              />
            </div>

            <div className="space-y-2">
              <FieldLabel htmlFor="imported-collection-description">Description</FieldLabel>
              <textarea
                id="imported-collection-description"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                rows={3}
                disabled={readOnly}
                placeholder="A short blurb shown alongside the collection."
                className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 flex min-h-[88px] w-full resize-y rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-60"
              />
              <p className="text-muted-foreground text-xs leading-relaxed">
                Imported with the collection on first sync. Edits stick — future syncs won't
                overwrite this.
              </p>
            </div>

            <div className="grid gap-5 sm:grid-cols-2">
              <div className="space-y-2">
                <FieldLabel htmlFor="imported-collection-watch-filter">Watch state</FieldLabel>
                <Select
                  value={watchFilter}
                  onValueChange={(next) => setWatchFilter(next as UserCollectionWatchFilter)}
                  disabled={readOnly}
                >
                  <SelectTrigger id="imported-collection-watch-filter" className="h-11 w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {watchFilterOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <FieldLabel htmlFor="imported-collection-media-filter">Content</FieldLabel>
                <Select
                  value={mediaFilter}
                  onValueChange={(next) => setMediaFilter(next as UserCollectionMediaFilter)}
                  disabled={readOnly}
                >
                  <SelectTrigger id="imported-collection-media-filter" className="h-11 w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {mediaFilterOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <p className="text-muted-foreground text-xs leading-relaxed">
              Uses the active profile&rsquo;s watched state. Shared profiles may see different
              results.
            </p>

            <div className="space-y-2">
              <FieldLabel>Libraries</FieldLabel>
              <CollectionLibraryPicker
                libraries={builderLibraries}
                value={libraryIds}
                onChange={setLibraryIds}
                eligibility={eligibility}
              />
              <p className="text-muted-foreground text-xs leading-relaxed">
                Items resolve only inside libraries you select. Leave empty to span every library
                you can see.
              </p>
            </div>
          </FormSection>

          <FormSection
            number="02"
            title="Source"
            description="Where this collection pulls its items from and how often."
          >
            <div className="space-y-2">
              <FieldLabel htmlFor="imported-collection-source-url">
                {isMDBList ? "Source URL" : `${theme.label} preset`}
              </FieldLabel>
              {isMDBList ? (
                <>
                  <Input
                    id="imported-collection-source-url"
                    value={sourceUrlInput}
                    onChange={(event) => setSourceUrlInput(event.target.value)}
                    placeholder="https://mdblist.com/lists/user/slug"
                    disabled={readOnly}
                    className="h-11 font-mono text-[0.85rem]"
                  />
                  {sourceUrlInvalid ? (
                    <p className="text-destructive text-xs">A list URL is required.</p>
                  ) : (
                    <p className="text-muted-foreground text-xs leading-relaxed">
                      The MDBList JSON URL the next sync will pull from. Trailing
                      <code className="bg-muted/40 mx-1 rounded px-1 py-px text-[10px]">/json</code>
                      is added automatically.
                    </p>
                  )}
                </>
              ) : (
                <PresetReadout label={sourcePresetLabel} url={sourceUrl} themeLabel={theme.label} />
              )}
            </div>

            <div className="grid gap-5 sm:grid-cols-2">
              <div className="space-y-2">
                <FieldLabel htmlFor="imported-collection-max-items">Max items</FieldLabel>
                <Input
                  id="imported-collection-max-items"
                  type="number"
                  inputMode="numeric"
                  min={0}
                  step={1}
                  value={maxItemsInput}
                  onChange={(event) => setMaxItemsInput(event.target.value)}
                  placeholder="Unlimited"
                  disabled={readOnly}
                  className="h-11 tabular-nums"
                />
                {maxItemsInvalid ? (
                  <p className="text-destructive text-xs">Enter a whole number, or leave empty.</p>
                ) : (
                  <p className="text-muted-foreground text-xs leading-relaxed">
                    Cap how many items the source contributes per sync. Leave empty for no cap.
                  </p>
                )}
              </div>
              <div className="space-y-2">
                <FieldLabel>Sync schedule</FieldLabel>
                <ScheduleReadout schedule={collection.sync_schedule} />
                <p className="text-muted-foreground text-xs leading-relaxed">
                  Set when imported. Recreate the collection from a {theme.label} template to change
                  it.
                </p>
              </div>
            </div>
          </FormSection>

          <FormSection
            number="03"
            title="Sharing"
            description="Decide which profiles on this account can browse the collection."
          >
            <CollectionAccessEditor
              value={{ is_shared: isShared, allowed_profile_ids: allowedProfileIds }}
              onChange={(next) => {
                setIsShared(next.is_shared);
                setAllowedProfileIds(next.allowed_profile_ids);
              }}
              profiles={profiles.map((entry) => ({ id: entry.id, name: entry.name }))}
              readOnly={readOnly}
              creatorProfileId={collection.creator_profile_id}
            />
          </FormSection>

          <FormSection
            number="04"
            title="Library visibility"
            description="Pin this collection to your library's Collections tab. Only you see it — personal collections are private to your user."
          >
            <ToggleRow
              title="Show in my library Collections tab"
              description="Appears in the Collections tab of the libraries you scope this to."
              checked={includeOnServer}
              onCheckedChange={setIncludeOnServer}
              disabled={readOnly}
            />
          </FormSection>

          {!readOnly ? (
            <FormSection
              number="05"
              title="Poster"
              description="Override the default card with a custom poster. Upload an image or paste an image URL."
            >
              <ImageUploadField
                label="Poster"
                currentUrl={collection.poster_url}
                file={posterFile}
                onFileChange={setPosterFile}
                sourceUrl={posterSourceUrl}
                onSourceUrlChange={setPosterSourceUrl}
                onDelete={
                  collection.poster_url
                    ? () => deletePosterMutation.mutate({ id: collection.id, type: "poster" })
                    : undefined
                }
              />
            </FormSection>
          ) : null}
        </div>

        <aside className="space-y-5 xl:sticky xl:top-6 xl:self-start">
          <SourceSpecSheet
            theme={theme}
            collection={collection}
            sourceUrl={sourceUrl}
            sourcePresetLabel={sourcePresetLabel}
          />
        </aside>
      </div>

      <div className="mt-10 flex justify-start">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-muted-foreground hover:text-destructive -ml-2 gap-1.5 text-xs tracking-wide uppercase"
          onClick={() => setConfirmDelete(true)}
          disabled={readOnly || deleteMutation.isPending}
        >
          <Trash2 className="h-3.5 w-3.5" />
          Delete this collection
        </Button>
      </div>

      <SaveDock
        visible={dirty && !readOnly}
        dirtyCount={dirtyCount}
        isSaving={isPending}
        saveBlocked={saveBlocked}
        onDiscard={handleDiscard}
        onCancel={onClose}
      />
    </form>
  );
}

function SourceBanner({
  theme,
  collection,
  sourcePresetLabel,
  sourceUrl,
  isSyncing,
  onSyncNow,
  readOnly,
}: {
  theme: SourceTheme;
  collection: Collection;
  sourcePresetLabel: string;
  sourceUrl: string | null;
  isSyncing: boolean;
  onSyncNow: () => void;
  readOnly: boolean;
}) {
  const last = formatLastSync(collection);
  return (
    <section
      className="surface-panel relative overflow-hidden rounded-[1.7rem] p-px"
      style={{
        backgroundImage: `linear-gradient(135deg, ${theme.haloFrom} 0%, transparent 38%, transparent 62%, ${theme.haloTo} 100%)`,
      }}
    >
      <div className="bg-background/55 relative overflow-hidden rounded-[calc(1.7rem-1px)] backdrop-blur-xl">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0"
          style={{
            backgroundImage: `radial-gradient(70% 100% at 0% 0%, ${theme.haloFrom} 0%, transparent 55%), radial-gradient(50% 100% at 100% 100%, ${theme.haloTo} 0%, transparent 60%)`,
          }}
        />
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 opacity-[0.035] mix-blend-overlay"
          style={{
            backgroundImage:
              "linear-gradient(0deg, currentColor 1px, transparent 1px), linear-gradient(90deg, currentColor 1px, transparent 1px)",
            backgroundSize: "44px 44px",
          }}
        />

        <div className="relative flex flex-col gap-7 p-6 sm:p-8 md:flex-row md:items-center md:justify-between">
          <div className="flex items-center gap-5">
            <BrandMark theme={theme} />
            <div className="space-y-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <span
                  className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[10px] font-semibold tracking-[0.2em] uppercase"
                  style={{
                    color: theme.accent,
                    backgroundColor: `color-mix(in srgb, ${theme.accent} 14%, transparent)`,
                    boxShadow: `inset 0 0 0 1px color-mix(in srgb, ${theme.accent} 35%, transparent)`,
                  }}
                >
                  <span
                    className="h-1.5 w-1.5 rounded-full"
                    style={{ backgroundColor: theme.accent }}
                  />
                  {theme.label}
                </span>
                <span className="text-foreground/85 text-sm font-medium">{sourcePresetLabel}</span>
                {readOnly ? (
                  <span className="text-muted-foreground border-border/70 inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-medium tracking-[0.16em] uppercase">
                    <Lock className="h-3 w-3" /> Read-only
                  </span>
                ) : null}
              </div>
              <p className="text-muted-foreground max-w-md text-sm leading-relaxed">
                Synced from {theme.tagline} — items, posters, and ordering are managed by the
                source.
              </p>
              <SyncPulse status={collection.last_sync_status} text={last} accent={theme.accent} />
            </div>
          </div>

          <div className="flex items-center gap-2 self-start md:self-center">
            {sourceUrl ? (
              <Button asChild variant="outline" size="sm" className="gap-1.5">
                <a href={sourceUrl} target="_blank" rel="noopener noreferrer">
                  <ExternalLink className="h-3.5 w-3.5" />
                  Open source
                </a>
              </Button>
            ) : null}
            <Button
              type="button"
              size="sm"
              onClick={onSyncNow}
              disabled={isSyncing || readOnly}
              className="gap-1.5 font-semibold"
              style={{
                backgroundColor: theme.accent,
                color: "#0b0b0c",
              }}
            >
              <RefreshCw className={`h-3.5 w-3.5 ${isSyncing ? "animate-spin" : ""}`} />
              {isSyncing ? "Syncing..." : "Sync now"}
            </Button>
          </div>
        </div>
      </div>
    </section>
  );
}

function BrandMark({ theme }: { theme: SourceTheme }) {
  return (
    <div className="relative h-16 w-16 shrink-0 sm:h-[72px] sm:w-[72px]">
      <div
        aria-hidden
        className="absolute -inset-2 rounded-2xl opacity-70 blur-xl"
        style={{
          backgroundImage: `radial-gradient(closest-side, ${theme.haloFrom}, transparent 70%)`,
        }}
      />
      <div
        className="bg-background/60 relative flex h-full w-full items-center justify-center rounded-2xl text-[1.35rem] font-bold tracking-tight backdrop-blur"
        style={{
          color: theme.accent,
          boxShadow: `inset 0 0 0 1px color-mix(in srgb, ${theme.accent} 40%, transparent), inset 0 0 28px color-mix(in srgb, ${theme.accent} 10%, transparent)`,
        }}
      >
        {theme.initials}
      </div>
    </div>
  );
}

function SyncPulse({
  status,
  text,
  accent,
}: {
  status: Collection["last_sync_status"];
  text: string;
  accent: string;
}) {
  const dotColor = statusDotColor(status, accent);
  const isRunning = status === "running";
  return (
    <div className="text-muted-foreground inline-flex items-center gap-2 text-xs font-medium">
      <span className="relative flex h-2 w-2" aria-hidden>
        <span className="absolute inset-0 rounded-full" style={{ backgroundColor: dotColor }} />
        {isRunning ? (
          <span
            className="absolute inset-0 animate-ping rounded-full opacity-60"
            style={{ backgroundColor: dotColor }}
          />
        ) : null}
      </span>
      <span>{text}</span>
    </div>
  );
}

function statusDotColor(status: Collection["last_sync_status"], themeAccent: string): string {
  switch (status) {
    case "running":
      return themeAccent;
    case "success":
      return "#34d399";
    case "warning":
      return "#fbbf24";
    case "failed":
      return "#fb7185";
    default:
      return "color-mix(in srgb, currentColor 35%, transparent)";
  }
}

function FormSection({
  number,
  title,
  description,
  children,
}: {
  number: string;
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <section className="grid gap-6 px-6 py-7 sm:px-8 sm:py-8 lg:grid-cols-[14rem_minmax(0,1fr)] lg:gap-10">
      <div className="space-y-1.5">
        <div className="text-muted-foreground/70 font-mono text-[10px] tracking-[0.3em] tabular-nums">
          {number}
        </div>
        <h3 className="text-foreground text-base font-semibold tracking-tight">{title}</h3>
        <p className="text-muted-foreground max-w-[18rem] text-xs leading-relaxed">{description}</p>
      </div>
      <div className="min-w-0 space-y-5">{children}</div>
    </section>
  );
}

function FieldLabel({ children, htmlFor }: { children: ReactNode; htmlFor?: string }) {
  return (
    <Label
      htmlFor={htmlFor}
      className="text-muted-foreground text-[11px] font-semibold tracking-[0.14em] uppercase"
    >
      {children}
    </Label>
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
  onCheckedChange: (next: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <label
      className={`group bg-background/40 hover:border-border/80 flex items-center justify-between gap-4 rounded-xl border border-[color-mix(in_srgb,var(--border)_55%,transparent)] px-4 py-3.5 transition-colors ${
        disabled ? "opacity-60" : "cursor-pointer"
      }`}
    >
      <div className="min-w-0 pr-2">
        <p className="text-sm font-medium">{title}</p>
        <p className="text-muted-foreground mt-0.5 text-xs leading-relaxed">{description}</p>
      </div>
      <Switch checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} />
    </label>
  );
}

function SourceSpecSheet({
  theme,
  collection,
  sourceUrl,
  sourcePresetLabel,
}: {
  theme: SourceTheme;
  collection: Collection;
  sourceUrl: string | null;
  sourcePresetLabel: string;
}) {
  const itemCount = collection.item_count != null ? collection.item_count.toLocaleString() : "—";
  return (
    <div
      className="surface-panel relative overflow-hidden rounded-[1.4rem]"
      style={{
        backgroundImage: `linear-gradient(180deg, color-mix(in srgb, ${theme.accent} 5%, transparent), transparent 35%)`,
      }}
    >
      <div
        aria-hidden
        className="absolute top-0 bottom-0 left-0 w-[3px]"
        style={{
          backgroundImage: `linear-gradient(180deg, ${theme.accent}, transparent)`,
        }}
      />
      <div className="relative px-5 py-5">
        <div className="flex items-center justify-between gap-3">
          <h4 className="flex items-center gap-2 text-[11px] font-semibold tracking-[0.2em] uppercase">
            <span
              className="inline-block h-1.5 w-1.5 rounded-full"
              style={{ backgroundColor: theme.accent }}
            />
            Source details
          </h4>
        </div>
        <p className="text-muted-foreground mt-1 text-xs">
          Captured when the collection was imported.
        </p>

        <div className="mt-4 divide-y divide-[color-mix(in_srgb,var(--border)_45%,transparent)]">
          <SpecRow icon={Hash} label={`${theme.label} preset`} value={sourcePresetLabel} />
          {sourceUrl ? (
            <SpecRow
              icon={Link2}
              label="URL"
              value={
                <a
                  href={sourceUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="hover:text-foreground inline-flex items-center gap-1 underline-offset-2 hover:underline"
                >
                  <span className="max-w-[12rem] truncate">{sourceUrl}</span>
                  <ExternalLink className="h-3 w-3 shrink-0" />
                </a>
              }
            />
          ) : null}
          <SpecRow icon={Hash} label="Items" value={itemCount} />
          {collection.last_sync_message ? (
            <SpecRow
              icon={CalendarClock}
              label={collection.last_sync_status === "failed" ? "Last error" : "Last note"}
              value={
                <span
                  className={
                    collection.last_sync_status === "failed"
                      ? "text-destructive"
                      : "text-foreground/85"
                  }
                >
                  {collection.last_sync_message}
                </span>
              }
            />
          ) : null}
          <SpecRow
            icon={Clock}
            label="Created"
            value={formatAbsoluteDate(collection.created_at) ?? "—"}
          />
        </div>
      </div>
    </div>
  );
}

function PresetReadout({
  label,
  url,
  themeLabel,
}: {
  label: string;
  url: string | null;
  themeLabel: string;
}) {
  return (
    <div className="border-border/60 bg-muted/20 flex items-center justify-between gap-3 rounded-md border px-3 py-2.5">
      <div className="min-w-0">
        <p className="truncate text-sm font-medium">{label}</p>
        {url ? (
          <p className="text-muted-foreground mt-0.5 truncate font-mono text-[11px]">{url}</p>
        ) : null}
      </div>
      <span className="text-muted-foreground inline-flex shrink-0 items-center gap-1 text-[10px] font-semibold tracking-[0.16em] uppercase">
        <Lock className="h-3 w-3" />
        {themeLabel}
      </span>
    </div>
  );
}

function ScheduleReadout({ schedule }: { schedule?: string }) {
  return (
    <div className="border-border/60 bg-muted/20 flex items-center justify-between gap-3 rounded-md border px-3 py-2.5">
      <div className="inline-flex items-center gap-2">
        <CalendarClock className="text-muted-foreground h-3.5 w-3.5" />
        <span className="text-sm font-medium">{prettySchedule(schedule)}</span>
      </div>
      <span className="text-muted-foreground inline-flex items-center gap-1 text-[10px] font-semibold tracking-[0.16em] uppercase">
        <Lock className="h-3 w-3" />
        Locked
      </span>
    </div>
  );
}

function prettySchedule(schedule?: string): string {
  if (!schedule) return "Manual";
  switch (schedule) {
    case "daily":
      return "Every day";
    case "weekly":
      return "Every week";
    case "monthly":
      return "Every month";
    default:
      return schedule.charAt(0).toUpperCase() + schedule.slice(1);
  }
}

function SpecRow({
  icon: Icon,
  label,
  value,
}: {
  icon: LucideIcon;
  label: string;
  value: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-3 py-2.5">
      <span className="text-muted-foreground inline-flex items-center gap-2 text-[11px] font-medium tracking-wide">
        <Icon className="h-3 w-3 opacity-70" />
        {label}
      </span>
      <span className="text-foreground/90 text-right text-xs font-medium">{value}</span>
    </div>
  );
}

function SaveDock({
  visible,
  dirtyCount,
  isSaving,
  saveBlocked,
  onDiscard,
  onCancel,
}: {
  visible: boolean;
  dirtyCount: number;
  isSaving: boolean;
  saveBlocked: boolean;
  onDiscard: () => void;
  onCancel: () => void;
}) {
  return (
    <div
      aria-hidden={!visible}
      className={`pointer-events-none fixed inset-x-0 bottom-5 z-40 flex justify-center px-4 transition-all duration-300 ${
        visible ? "translate-y-0 opacity-100" : "pointer-events-none translate-y-6 opacity-0"
      }`}
    >
      <div className="bg-background/85 border-border/80 pointer-events-auto flex items-center gap-3 rounded-full border px-3.5 py-2 shadow-[0_18px_40px_-18px_rgba(0,0,0,0.55)] backdrop-blur-xl">
        <div className="flex items-center gap-2 pr-1 pl-1.5">
          <span className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-amber-300/90" />
          <span className="text-foreground/85 text-xs font-medium">
            {saveBlocked
              ? "Fix errors above"
              : `${dirtyCount} unsaved ${dirtyCount === 1 ? "change" : "changes"}`}
          </span>
        </div>
        <div className="bg-border/70 h-5 w-px" aria-hidden />
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-8 px-3 text-xs"
          onClick={onCancel}
          disabled={isSaving}
        >
          Cancel
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-8 px-3 text-xs"
          onClick={onDiscard}
          disabled={isSaving}
        >
          Discard
        </Button>
        <Button
          type="submit"
          size="sm"
          className="h-8 px-4 text-xs font-semibold"
          disabled={isSaving || saveBlocked}
        >
          {isSaving ? "Saving..." : "Save changes"}
        </Button>
      </div>
    </div>
  );
}

function arraysEqual<T>(a: T[], b: T[]): boolean {
  if (a.length !== b.length) return false;
  const sa = [...a].sort();
  const sb = [...b].sort();
  for (let i = 0; i < sa.length; i++) {
    if (sa[i] !== sb[i]) return false;
  }
  return true;
}

function formatLastSync(collection: Collection): string {
  if (collection.last_sync_status === "running") {
    return "Sync in progress…";
  }
  if (!collection.last_sync_at) {
    return "Not yet synced";
  }
  const ago = formatRelativeTime(collection.last_sync_at);
  switch (collection.last_sync_status) {
    case "success":
      return `Synced ${ago}`;
    case "warning":
      return `Synced ${ago} with warnings`;
    case "failed":
      return `Sync failed ${ago}`;
    default:
      return `Last synced ${ago}`;
  }
}

function formatRelativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "recently";
  const delta = Date.now() - t;
  const s = Math.round(delta / 1000);
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min${m === 1 ? "" : "s"} ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} hour${h === 1 ? "" : "s"} ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d} day${d === 1 ? "" : "s"} ago`;
  const months = Math.round(d / 30);
  if (months < 12) return `${months} month${months === 1 ? "" : "s"} ago`;
  const years = Math.round(months / 12);
  return `${years} year${years === 1 ? "" : "s"} ago`;
}

function formatAbsoluteDate(iso: string | undefined): string | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return null;
  return formatPreferredDate(t, "medium") || null;
}

function readSourceConfigLimit(collection: Collection): number | null {
  const cfg = collection.source_config;
  if (!cfg || typeof cfg !== "object") return null;
  const limit = (cfg as Record<string, unknown>).limit;
  if (typeof limit === "number" && Number.isFinite(limit) && limit > 0) {
    return Math.trunc(limit);
  }
  return null;
}

function sanitizeLibraryIDs(raw: unknown): number[] {
  if (!Array.isArray(raw)) return [];
  const ids = raw
    .filter((id): id is number => typeof id === "number" && Number.isFinite(id) && id > 0)
    .map((id) => Math.trunc(id));
  return Array.from(new Set(ids));
}

function readSourceConfigLibraryIDs(collection: Collection): number[] {
  const cfg = collection.source_config;
  if (cfg && typeof cfg === "object") {
    const raw = (cfg as Record<string, unknown>).library_ids;
    if (Array.isArray(raw)) {
      return sanitizeLibraryIDs(raw);
    }
  }
  return sanitizeLibraryIDs(collection.query_definition.library_ids);
}

function parseMaxItemsInput(value: string): number | null {
  const trimmed = value.trim();
  if (trimmed === "") return null;
  if (!/^\d+$/.test(trimmed)) return null;
  const n = parseInt(trimmed, 10);
  if (!Number.isFinite(n) || n < 0) return null;
  return n === 0 ? null : n;
}

function readableSourceURL(collection: Collection): string | null {
  const url = collection.source_url;
  if (!url) return null;
  if (url.startsWith("http://") || url.startsWith("https://")) return url;
  return null;
}

function sourcePresetSummary(collection: Collection, fallback: string): string {
  const cfg = collection.source_config;
  if (cfg && typeof cfg === "object") {
    const preset = (cfg as Record<string, unknown>).preset;
    const mediaType = (cfg as Record<string, unknown>).media_type;
    const window = (cfg as Record<string, unknown>).time_window;
    const parts: string[] = [];
    if (typeof preset === "string" && preset) parts.push(prettyPreset(preset));
    if (typeof mediaType === "string" && mediaType) parts.push(prettyMediaType(mediaType));
    if (typeof window === "string" && window) parts.push(`this ${window}`);
    if (parts.length > 0) return parts.join(" · ");
  }
  return fallback;
}

function prettyPreset(preset: string): string {
  return preset
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function prettyMediaType(type: string): string {
  switch (type) {
    case "movie":
      return "Movies";
    case "tv":
      return "TV";
    case "all":
      return "Movies + TV";
    default:
      return type;
  }
}
