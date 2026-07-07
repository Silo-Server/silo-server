import { useState } from "react";
import type { FormEvent } from "react";

import type {
  ImportUserMDBListCollectionRequest,
  ImportUserTMDBCollectionRequest,
  ImportUserTraktCollectionRequest,
  UserCollectionMediaFilter,
  UserCollectionSyncSchedule,
  UserCollectionWatchFilter,
} from "@/api/types";
import {
  useImportUserMDBListCollection,
  useImportUserTMDBCollection,
  useImportUserTraktCollection,
} from "@/hooks/queries/userCollectionImports";
import { useUserLibraries } from "@/hooks/queries/libraries";
import {
  COLLECTION_MEDIA_FILTER_OPTIONS,
  COLLECTION_WATCH_FILTER_OPTIONS,
  displayFiltersToQueryDefinition,
} from "@/lib/collectionDisplayFilters";
import {
  COLLECTION_MAX_ITEMS,
  libraryEligibilityForMediaKind,
  mediaKindLabel,
} from "@/lib/collectionTemplates";
import type { CollectionTemplate } from "@/lib/collectionTemplates";
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

import { MDBListBrowser } from "./MDBListBrowser";
import { TemplatePosterField, type TemplatePosterMode } from "./TemplatePosterField";

interface Props {
  template: CollectionTemplate;
  onCancel: () => void;
  onCreated: () => void;
}

// MANUAL_SCHEDULE is the in-form sentinel for "no automatic refresh". Radix
// Select disallows empty-string SelectItem values, so we map this to "" only
// at submit time when populating the request body.
const MANUAL_SCHEDULE = "none" as const;
type ScheduleChoice = typeof MANUAL_SCHEDULE | Exclude<UserCollectionSyncSchedule, "">;

// SCHEDULE_OPTIONS is the user-allowed cadence set. It mirrors the
// usercollections.AllowedSyncSchedules map on the backend, which caps user
// schedules to >= 24h to keep TMDB/Trakt API quota usage bounded.
const SCHEDULE_OPTIONS: Array<{ value: ScheduleChoice; label: string }> = [
  { value: MANUAL_SCHEDULE, label: "Manual only" },
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
  { value: "monthly", label: "Monthly" },
];

function scheduleChoiceToRequest(choice: ScheduleChoice): UserCollectionSyncSchedule {
  return choice === MANUAL_SCHEDULE ? "" : choice;
}

// templateDefaultSchedule maps the template's built-in cron into the closest
// user-allowed cadence. Templates that prescribe a sub-daily cadence get
// downgraded to "daily" — users cannot opt into shorter intervals here.
function templateDefaultSchedule(cron: string | undefined): ScheduleChoice {
  if (!cron) return MANUAL_SCHEDULE;
  const fields = cron.trim().split(/\s+/);
  if (fields.length !== 5) return "daily";
  const [, , dom, month, dow] = fields;
  if (dom === "1" && month === "*") return "monthly";
  if (dom === "*" && month === "*" && dow !== "*") return "weekly";
  return "daily";
}

export function UserCollectionTemplateConfigForm({ template, onCancel, onCreated }: Props) {
  const tmdbMutation = useImportUserTMDBCollection();
  const traktMutation = useImportUserTraktCollection();
  const mdblistMutation = useImportUserMDBListCollection();
  const { data: libraries = [] } = useUserLibraries();

  const [title, setTitle] = useState(template.title);
  const [description, setDescription] = useState(template.description);
  const [limit, setLimit] = useState(template.default_limit ? String(template.default_limit) : "");
  const [schedule, setSchedule] = useState<ScheduleChoice>(
    templateDefaultSchedule(template.default_sync_schedule),
  );
  const [isShared, setIsShared] = useState(false);
  const [mdblistUrl, setMdblistUrl] = useState(template.mdblist?.url ?? "");
  const [libraryIds, setLibraryIds] = useState<number[]>([]);
  const [watchFilter, setWatchFilter] = useState<UserCollectionWatchFilter>("all");
  const [mediaFilter, setMediaFilter] = useState<UserCollectionMediaFilter>("all");
  const [posterMode, setPosterMode] = useState<TemplatePosterMode>(() =>
    template.poster_path ? "default" : "custom",
  );
  const [customPosterUrl, setCustomPosterUrl] = useState("");

  const eligibility = libraryEligibilityForMediaKind(template.media_kind);
  const pickerLibraries = libraries.map((lib) => ({
    id: lib.id,
    name: lib.name,
    type: lib.type,
  }));

  const parsedLimit = parseOptionalPositiveInteger(limit);
  const limitInvalid = limit.trim().length > 0 && parsedLimit === undefined;
  const isPending = tmdbMutation.isPending || traktMutation.isPending || mdblistMutation.isPending;
  const missingMDBListURL = template.source === "mdblist" && mdblistUrl.trim().length === 0;
  const submitDisabled = isPending || limitInvalid || missingMDBListURL;

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (submitDisabled) return;

    const sharedFields = {
      title: title.trim() || template.title,
      description: description.trim() || undefined,
      sync_schedule: scheduleChoiceToRequest(schedule),
      limit: parsedLimit,
      is_shared: isShared,
      poster_url:
        posterMode === "custom"
          ? customPosterUrl.trim() || undefined
          : template.poster_path || undefined,
      library_ids: libraryIds.length > 0 ? libraryIds : undefined,
      display_query_definition: displayFiltersToQueryDefinition(watchFilter, mediaFilter),
    };

    if (template.source === "tmdb" && template.tmdb) {
      const body: ImportUserTMDBCollectionRequest = {
        ...sharedFields,
        preset: template.tmdb.preset,
        media_type: template.tmdb.media_type,
        time_window: template.tmdb.time_window,
      };
      tmdbMutation.mutate(body, { onSuccess: onCreated });
      return;
    }

    if (template.source === "trakt" && template.trakt) {
      const body: ImportUserTraktCollectionRequest = {
        ...sharedFields,
        preset: template.trakt.preset,
        media_type: template.trakt.media_type,
      };
      traktMutation.mutate(body, { onSuccess: onCreated });
      return;
    }

    if (template.source === "mdblist") {
      const body: ImportUserMDBListCollectionRequest = {
        ...sharedFields,
        url: mdblistUrl.trim(),
      };
      mdblistMutation.mutate(body, { onSuccess: onCreated });
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

      <div className="space-y-2">
        <Label htmlFor="user-template-title">Collection Title</Label>
        <Input
          id="user-template-title"
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          required
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="user-template-description">Description</Label>
        <Input
          id="user-template-description"
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          placeholder="Optional summary"
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
            <Label htmlFor="user-template-mdblist-url">MDBList URL</Label>
            <Input
              id="user-template-mdblist-url"
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

      <div className="space-y-2">
        <Label>Libraries</Label>
        <CollectionLibraryPicker
          libraries={pickerLibraries}
          value={libraryIds}
          onChange={setLibraryIds}
          eligibility={eligibility}
        />
        <p className="text-muted-foreground text-xs">
          Leave empty to span every library you can see.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="user-template-watch-filter">Watch state</Label>
          <Select
            value={watchFilter}
            onValueChange={(next) => setWatchFilter(next as UserCollectionWatchFilter)}
          >
            <SelectTrigger id="user-template-watch-filter">
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
          <Label htmlFor="user-template-media-filter">Content</Label>
          <Select
            value={mediaFilter}
            onValueChange={(next) => setMediaFilter(next as UserCollectionMediaFilter)}
          >
            <SelectTrigger id="user-template-media-filter">
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
      <p className="text-muted-foreground text-xs">
        Uses the active profile&rsquo;s watched state. Shared profiles may see different results.
      </p>

      {template.requires_profile ? (
        <p className="text-muted-foreground text-xs">
          This template uses your active profile&rsquo;s connected Trakt account.
        </p>
      ) : null}

      <TemplatePosterField
        template={template}
        mode={posterMode}
        onModeChange={setPosterMode}
        customUrl={customPosterUrl}
        onCustomUrlChange={setCustomPosterUrl}
        inputId="user-template-poster-url"
      />

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="user-template-limit">Max Items</Label>
          <Input
            id="user-template-limit"
            type="number"
            min={1}
            max={COLLECTION_MAX_ITEMS}
            step={1}
            inputMode="numeric"
            value={limit}
            onChange={(event) => setLimit(event.target.value)}
            placeholder={template.default_limit ? String(template.default_limit) : "No limit"}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="user-template-schedule">Auto Refresh</Label>
          <Select value={schedule} onValueChange={(next) => setSchedule(next as ScheduleChoice)}>
            <SelectTrigger id="user-template-schedule">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SCHEDULE_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
        <div className="space-y-0.5">
          <p className="text-sm font-medium">Share with other profiles</p>
          <p className="text-muted-foreground text-xs">
            When on, profiles you choose can browse this collection too.
          </p>
        </div>
        <Switch checked={isShared} onCheckedChange={setIsShared} />
      </div>

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
