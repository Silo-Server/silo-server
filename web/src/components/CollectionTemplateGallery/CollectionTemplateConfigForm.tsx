import { useState } from "react";
import type { FormEvent } from "react";

import {
  useImportMDBListCollection,
  useImportTMDBCollection,
  useImportTraktCollection,
} from "@/hooks/queries/admin/collections";
import { useProfiles } from "@/hooks/queries/profiles";
import type { Library } from "@/api/types";
import { libraryEligibilityForMediaKind, mediaKindLabel } from "@/lib/collectionTemplates";
import type { CollectionTemplate, LibraryEligibility } from "@/lib/collectionTemplates";
import {
  CollectionLibraryPicker,
  parseOptionalPositiveInteger,
} from "@/pages/adminCollectionsShared";

import { Badge } from "@/components/ui/badge";
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
import { SyncScheduleField } from "@/components/collections/SyncScheduleField";

import { MDBListBrowser } from "./MDBListBrowser";
import { TemplatePosterField, type TemplatePosterMode } from "./TemplatePosterField";

interface Props {
  template: CollectionTemplate;
  libraries: Library[];
  initialLibraryId: number | null;
  onCancel: () => void;
  onCreated: () => void;
}

function isEligibleTemplateLibrary(
  library: Pick<Library, "type">,
  eligibility: LibraryEligibility,
): boolean {
  const kinds = eligibility.kinds;
  if (!kinds || kinds.length === 0) return true;
  if (!library.type) return true;
  if (library.type === "mixed") return true;
  return kinds.includes(library.type);
}

function initialTemplateLibraryIds(
  libraries: Library[],
  initialLibraryId: number | null,
  eligibility: LibraryEligibility,
): number[] {
  const eligibleLibraries = libraries.filter((library) =>
    isEligibleTemplateLibrary(library, eligibility),
  );
  if (eligibleLibraries.length === 0) return [];

  if (initialLibraryId) {
    const initialLibrary = eligibleLibraries.find((library) => library.id === initialLibraryId);
    if (initialLibrary) return [initialLibrary.id];
  }

  const [firstEligibleLibrary] = eligibleLibraries;
  return firstEligibleLibrary ? [firstEligibleLibrary.id] : [];
}

export function CollectionTemplateConfigForm({
  template,
  libraries,
  initialLibraryId,
  onCancel,
  onCreated,
}: Props) {
  const tmdbMutation = useImportTMDBCollection();
  const traktMutation = useImportTraktCollection();
  const mdblistMutation = useImportMDBListCollection();
  const { data: profiles = [] } = useProfiles();
  const eligibility = libraryEligibilityForMediaKind(template.media_kind);

  const [libraryIds, setLibraryIds] = useState<number[]>(() =>
    initialTemplateLibraryIds(libraries, initialLibraryId, eligibility),
  );

  const [title, setTitle] = useState(template.title);
  const [description, setDescription] = useState(template.description);
  const [limit, setLimit] = useState(template.default_limit ? String(template.default_limit) : "");
  const [syncSchedule, setSyncSchedule] = useState(template.default_sync_schedule ?? "");
  const [featured, setFeatured] = useState(template.featured ?? true);
  const defaultProfileId = template.requires_profile ? (profiles[0]?.id ?? "") : "";
  const [profileId, setProfileId] = useState(defaultProfileId);
  const [mdblistUrl, setMdblistUrl] = useState(template.mdblist?.url ?? "");
  const [posterMode, setPosterMode] = useState<TemplatePosterMode>(() =>
    template.poster_path ? "default" : "custom",
  );
  const [customPosterUrl, setCustomPosterUrl] = useState("");

  // Discover- and Collection-source templates are bundle-only: the spec is
  // backend-driven and can't be edited inline. Render a read-only summary so
  // admins know to apply them via Template Bundles. The early returns MUST
  // sit AFTER every useState above so React's rules-of-hooks lint stays
  // green — the unused state slots are cheap and isolate this branch from
  // the editable-form path.
  if (template.source === "tmdb_collection") {
    return <TMDBCollectionTemplateSummary template={template} onCancel={onCancel} />;
  }
  if (template.source === "tmdb_discover") {
    return <TMDBDiscoverTemplateSummary template={template} onCancel={onCancel} />;
  }

  // When profiles load after initial render and the template needs one but
  // no value has been chosen yet, settle on the first profile.
  if (template.requires_profile && profileId === "" && profiles[0]?.id) {
    setProfileId(profiles[0].id);
  }

  const parsedLimit = parseOptionalPositiveInteger(limit);
  const limitInvalid = limit.trim().length > 0 && parsedLimit === undefined;
  const isPending = tmdbMutation.isPending || traktMutation.isPending || mdblistMutation.isPending;
  const missingLibrary = libraryIds.length === 0;
  const missingProfile = template.requires_profile && profileId === "";
  const missingMDBListURL = template.source === "mdblist" && mdblistUrl.trim().length === 0;

  const submitDisabled =
    isPending || missingLibrary || missingProfile || missingMDBListURL || limitInvalid;

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (submitDisabled) return;

    const sharedFields = {
      library_ids: libraryIds,
      title: title.trim() || template.title,
      description: description.trim() || undefined,
      featured,
      sync_schedule: syncSchedule.trim() || undefined,
      limit: parsedLimit,
      ...(posterMode === "custom"
        ? { poster_source_url: customPosterUrl.trim() || undefined }
        : { poster_url: template.poster_path || undefined }),
    };

    if (template.source === "tmdb" && template.tmdb) {
      tmdbMutation.mutate(
        {
          body: {
            ...sharedFields,
            preset: template.tmdb.preset,
            media_type: template.tmdb.media_type,
            time_window: template.tmdb.time_window,
          },
        },
        { onSuccess: onCreated },
      );
      return;
    }

    if (template.source === "trakt" && template.trakt) {
      traktMutation.mutate(
        {
          body: {
            ...sharedFields,
            preset: template.trakt.preset,
            media_type: template.trakt.media_type,
            profile_id: template.requires_profile ? profileId : undefined,
          },
        },
        { onSuccess: onCreated },
      );
      return;
    }

    if (template.source === "mdblist" && template.mdblist) {
      mdblistMutation.mutate(
        {
          body: {
            ...sharedFields,
            url: mdblistUrl.trim(),
          },
        },
        { onSuccess: onCreated },
      );
      return;
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="border-border bg-muted/40 flex items-start gap-3 rounded-lg border p-3">
        <div
          className="bg-background text-primary flex h-10 w-10 shrink-0 items-center justify-center rounded-lg text-xl"
          aria-hidden
        >
          {template.icon}
        </div>
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-semibold">{template.title}</span>
            <Badge variant="outline" className="text-[10px] uppercase">
              {template.source}
            </Badge>
            <Badge variant="secondary" className="text-[10px]">
              {mediaKindLabel(template.media_kind)}
            </Badge>
          </div>
          <p className="text-muted-foreground text-xs">{template.description}</p>
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
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
          <Label htmlFor="template-title">Collection Title</Label>
          <Input
            id="template-title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            required
          />
        </div>
      </div>

      <div className="space-y-2">
        <Label htmlFor="template-description">Description</Label>
        <Input
          id="template-description"
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          placeholder="Optional summary shown to viewers"
        />
      </div>

      {template.source === "mdblist" ? (
        <div className="space-y-3">
          <MDBListBrowser
            onPick={(list, jsonURL) => {
              setMdblistUrl(jsonURL);
              if (!title.trim() || title.trim() === template.title) {
                setTitle(list.name);
              }
            }}
          />
          <div className="space-y-2">
            <Label htmlFor="template-mdblist-url">MDBList URL</Label>
            <Input
              id="template-mdblist-url"
              value={mdblistUrl}
              onChange={(event) => setMdblistUrl(event.target.value)}
              placeholder="https://mdblist.com/lists/user/slug"
              required
            />
            <p className="text-muted-foreground text-xs">
              Pick a list above or paste any public MDBList list URL (with or without{" "}
              <code>/json</code>). Items resolve via TMDB/IMDb/TVDB IDs.
            </p>
          </div>
        </div>
      ) : null}

      {template.requires_profile ? (
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
            This template uses the chosen profile's connected Trakt account from Watch Providers
            settings.
          </p>
        </div>
      ) : null}

      <TemplatePosterField
        template={template}
        mode={posterMode}
        onModeChange={setPosterMode}
        customUrl={customPosterUrl}
        onCustomUrlChange={setCustomPosterUrl}
        inputId="template-poster-url"
      />

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="template-limit">Max Items</Label>
          <Input
            id="template-limit"
            type="number"
            min={1}
            max={200}
            step={1}
            inputMode="numeric"
            value={limit}
            onChange={(event) => setLimit(event.target.value)}
            placeholder={template.default_limit ? String(template.default_limit) : "No limit"}
          />
        </div>
        <div className="space-y-2">
          <Label>Featured</Label>
          <div className="border-border flex h-9 items-center justify-between rounded-md border px-3">
            <span className="text-muted-foreground text-xs">Surface in hero shelves</span>
            <Switch checked={featured} onCheckedChange={setFeatured} />
          </div>
        </div>
      </div>

      <SyncScheduleField value={syncSchedule} onChange={setSyncSchedule} />

      <div className="border-border flex justify-end gap-2 border-t pt-4">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={isPending}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitDisabled}>
          {isPending ? "Importing..." : "Create Collection"}
        </Button>
      </div>
    </form>
  );
}

interface TMDBCollectionTemplateSummaryProps {
  template: CollectionTemplate;
  onCancel: () => void;
}

// TMDBCollectionTemplateSummary is the read-only view shown when an admin
// opens a `tmdb_collection` template directly from the gallery. The spec
// (TMDB collection ID, sort order, sync schedule) ships from the backend
// catalog and isn't user-editable at apply-time — the official flow is to
// apply via Template Bundles, where bulk creation, dedupe by management key,
// and featured-section wiring are all handled in one shot.
function TMDBCollectionTemplateSummary({ template, onCancel }: TMDBCollectionTemplateSummaryProps) {
  const collectionId = template.tmdb_collection?.collection_id ?? 0;
  const isPlaceholder = collectionId === 0;

  return (
    <div className="space-y-4">
      <div className="border-border bg-muted/40 flex items-start gap-3 rounded-lg border p-3">
        <div
          className="bg-background text-primary flex h-10 w-10 shrink-0 items-center justify-center rounded-lg text-xl"
          aria-hidden
        >
          {template.icon}
        </div>
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-semibold">{template.title}</span>
            <Badge variant="outline" className="text-[10px] uppercase">
              {template.source}
            </Badge>
            <Badge variant="secondary" className="text-[10px]">
              {mediaKindLabel(template.media_kind)}
            </Badge>
          </div>
          <p className="text-muted-foreground text-xs">{template.description}</p>
        </div>
      </div>

      <dl className="grid gap-2 text-sm sm:grid-cols-2">
        <div className="space-y-0.5">
          <dt className="text-muted-foreground text-xs uppercase">TMDB Collection ID</dt>
          <dd className="font-mono text-sm">
            {isPlaceholder ? "(unset — admin fills in at apply)" : collectionId}
          </dd>
        </div>
        {typeof template.default_sort_order === "number" ? (
          <div className="space-y-0.5">
            <dt className="text-muted-foreground text-xs uppercase">Default Sort Order</dt>
            <dd className="font-mono text-sm">{template.default_sort_order}</dd>
          </div>
        ) : null}
      </dl>

      <p className="text-muted-foreground border-border bg-muted/30 rounded-md border p-3 text-xs">
        TMDB franchise templates are bundle-driven. Apply them through Template Bundles so the
        management key, library scoping, and sync schedule are wired up consistently across
        libraries.
        {isPlaceholder ? (
          <>
            {" "}
            This template is a placeholder; after applying, edit the collection's source_config and
            replace <code>collection_id: 0</code> with the real TMDB collection ID before the first
            sync.
          </>
        ) : null}
      </p>

      <div className="border-border flex justify-end gap-2 border-t pt-4">
        <Button type="button" variant="ghost" onClick={onCancel}>
          Close
        </Button>
        <Button type="button" disabled>
          Use Template Bundles
        </Button>
      </div>
    </div>
  );
}

interface TMDBDiscoverTemplateSummaryProps {
  template: CollectionTemplate;
  onCancel: () => void;
}

// summarizeDiscoverSpec produces a one-line human description of the discover
// filter set so admins can see at a glance what the template will fetch.
function summarizeDiscoverSpec(spec: NonNullable<CollectionTemplate["tmdb_discover"]>): string {
  const parts: string[] = [];
  parts.push(spec.media_type === "tv" ? "TV" : "movies");
  parts.push(`sorted by ${spec.sort_by}`);
  if (spec.with_genres?.length) {
    parts.push(`genres ${spec.with_genres.join(", ")}`);
  }
  if (spec.vote_count_gte) {
    parts.push(`votes ≥ ${spec.vote_count_gte}`);
  }
  if (spec.vote_average_gte) {
    parts.push(`rating ≥ ${spec.vote_average_gte}`);
  }
  if (spec.release_date_gte || spec.release_date_lte) {
    const lo = spec.release_date_gte ?? "…";
    const hi = spec.release_date_lte ?? "…";
    parts.push(`released ${lo}–${hi}`);
  }
  if (spec.certifications?.length) {
    parts.push(`certs ${spec.certifications.join("/")}`);
  }
  if (spec.certification_lte) {
    parts.push(`cert ≤ ${spec.certification_lte}`);
  }
  if (spec.original_language) {
    parts.push(`lang ${spec.original_language}`);
  }
  return parts.join(" · ");
}

// TMDBDiscoverTemplateSummary mirrors TMDBCollectionTemplateSummary: TMDB
// discover templates ship as backend-driven blueprints (genre matrices etc.)
// and apply through Template Bundles, not the per-template create form.
function TMDBDiscoverTemplateSummary({ template, onCancel }: TMDBDiscoverTemplateSummaryProps) {
  const spec = template.tmdb_discover;

  return (
    <div className="space-y-4">
      <div className="border-border bg-muted/40 flex items-start gap-3 rounded-lg border p-3">
        <div
          className="bg-background text-primary flex h-10 w-10 shrink-0 items-center justify-center rounded-lg text-xl"
          aria-hidden
        >
          {template.icon}
        </div>
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-semibold">{template.title}</span>
            <Badge variant="outline" className="text-[10px] uppercase">
              {template.source}
            </Badge>
            <Badge variant="secondary" className="text-[10px]">
              {mediaKindLabel(template.media_kind)}
            </Badge>
          </div>
          <p className="text-muted-foreground text-xs">{template.description}</p>
        </div>
      </div>

      {spec ? (
        <dl className="grid gap-2 text-sm sm:grid-cols-2">
          <div className="space-y-0.5">
            <dt className="text-muted-foreground text-xs uppercase">Filter Summary</dt>
            <dd className="text-sm">{summarizeDiscoverSpec(spec)}</dd>
          </div>
          {typeof template.default_sort_order === "number" ? (
            <div className="space-y-0.5">
              <dt className="text-muted-foreground text-xs uppercase">Default Sort Order</dt>
              <dd className="font-mono text-sm">{template.default_sort_order}</dd>
            </div>
          ) : null}
        </dl>
      ) : null}

      <p className="text-muted-foreground border-border bg-muted/30 rounded-md border p-3 text-xs">
        Discover templates are applied via Template Bundles. The filter set ships from the backend
        catalog; apply through a bundle so the management key, library scoping, and sync schedule
        are wired up consistently across libraries.
      </p>

      <div className="border-border flex justify-end gap-2 border-t pt-4">
        <Button type="button" variant="ghost" onClick={onCancel}>
          Close
        </Button>
        <Button type="button" disabled>
          Use Template Bundles
        </Button>
      </div>
    </div>
  );
}
