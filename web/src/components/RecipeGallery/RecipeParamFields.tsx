import { CollectionSearchableSelect } from "@/components/CollectionSearchableSelect";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { useAllUserCollections } from "@/hooks/queries/useAllUserCollections";
import { useAvailableUserLibraries } from "@/hooks/queries/libraries";
import type { RecipeDefinition } from "@/lib/recipes";

export interface RecipeParamFieldsProps {
  def: RecipeDefinition;
  params: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

export default function RecipeParamFields({ def, params, onChange }: RecipeParamFieldsProps) {
  if (def.type === "collection") {
    return <CollectionParamField params={params} onChange={onChange} />;
  }
  if (def.type === "continue_watching") {
    return <ContinueTypeParamField params={params} onChange={onChange} />;
  }
  if (def.type === "watchlist" || def.type === "favorites") {
    return <PersonalListFilterFields params={params} onChange={onChange} />;
  }
  if (def.type === "seasonal_themed") {
    return <SeasonalParamField params={params} onChange={onChange} />;
  }
  if (def.type === "editorial_spotlight") {
    const subjectType = (params.subject_type as string) ?? "director";
    const autoRotate = (params.auto_rotate as boolean) ?? true;
    const cadence = (params.rotation_cadence as string) ?? "weekly";
    const subject = (params.subject as string) ?? "";
    return (
      <div className="space-y-3">
        <label className="block">
          <span className="mb-1 block text-xs text-white/70">Subject type</span>
          <select
            value={subjectType}
            onChange={(e) => onChange({ ...params, subject_type: e.target.value })}
            className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
          >
            <option value="director">Director</option>
            <option value="studio">Studio</option>
            <option value="actor">Actor</option>
            <option value="era">Era</option>
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={autoRotate}
            onChange={(e) => onChange({ ...params, auto_rotate: e.target.checked })}
          />
          Auto-rotate
        </label>
        {autoRotate ? (
          <label className="block">
            <span className="mb-1 block text-xs text-white/70">Rotation cadence</span>
            <select
              value={cadence}
              onChange={(e) => onChange({ ...params, rotation_cadence: e.target.value })}
              className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
            >
              <option value="daily">Daily</option>
              <option value="weekly">Weekly (default)</option>
              <option value="monthly">Monthly</option>
            </select>
          </label>
        ) : (
          <label className="block">
            <span className="mb-1 block text-xs text-white/70">Subject</span>
            <input
              value={subject}
              onChange={(e) => onChange({ ...params, subject: e.target.value })}
              className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
              placeholder="e.g. Christopher Nolan"
            />
          </label>
        )}
      </div>
    );
  }
  if (def.type === "because_you_watched") {
    const anchor = (params.anchor_item_id as string) ?? "";
    return (
      <div>
        <label className="mb-1 block text-xs text-white/70">Anchor item</label>
        <input
          className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
          placeholder="Auto-pick latest watched (leave blank)"
          value={anchor}
          onChange={(e) => onChange({ ...params, anchor_item_id: e.target.value })}
        />
        <div className="mt-1 text-[11px] text-white/50">
          Leave blank to auto-pick the most recent watch.
        </div>
      </div>
    );
  }
  if (def.type === "taste_match") {
    const genre = (params.genre as string) ?? "";
    return (
      <div>
        <label className="mb-1 block text-xs text-white/70">Genre (optional)</label>
        <input
          className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
          placeholder="e.g. Sci-Fi"
          value={genre}
          onChange={(e) => onChange({ ...params, genre: e.target.value })}
        />
      </div>
    );
  }
  return null;
}

interface ParamFieldProps {
  params: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

// PersonalListFilterFields edits the optional filter_type / filter_library_ids
// filters for watchlist and favorites sections, e.g. a "Movies watchlist" rail.
function PersonalListFilterFields({ params, onChange }: ParamFieldProps) {
  const { data: libraries } = useAvailableUserLibraries();
  const filterType = typeof params.filter_type === "string" ? params.filter_type : "";
  const libraryIds = Array.isArray(params.filter_library_ids)
    ? params.filter_library_ids.filter((id): id is number => typeof id === "number")
    : [];

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <label className="block">
        <span className="mb-1 block text-xs text-white/70">Media type</span>
        <select
          value={filterType || "all"}
          onChange={(e) =>
            onChange({
              ...params,
              filter_type: e.target.value === "all" ? undefined : e.target.value,
            })
          }
          className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
        >
          <option value="all">All Media</option>
          <option value="movie">Movies</option>
          <option value="series">TV Shows</option>
          <option value="audiobook">Audiobooks</option>
        </select>
      </label>
      <label className="block">
        <span className="mb-1 block text-xs text-white/70">Libraries</span>
        <LibraryMultiSelect
          libraries={libraries ?? []}
          value={libraryIds}
          onChange={(next) =>
            onChange({ ...params, filter_library_ids: next.length > 0 ? next : undefined })
          }
        />
      </label>
    </div>
  );
}

function ContinueTypeParamField({ params, onChange }: ParamFieldProps) {
  const continueType = params.continue_type === "listening" ? "listening" : "watching";

  return (
    <label className="block">
      <span className="mb-1 block text-xs text-white/70">Continue type</span>
      <select
        value={continueType}
        onChange={(event) => onChange({ ...params, continue_type: event.target.value })}
        className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
      >
        <option value="watching">Watching</option>
        <option value="listening">Listening</option>
      </select>
    </label>
  );
}

// Order matches SeasonalThemeOrder in the backend. Higher entries take
// priority when multiple enabled themes match the current date.
const SEASONAL_THEMES: Array<{ key: string; label: string; window: string; icon: string }> = [
  { key: "valentines", label: "Valentine's Day", window: "Feb 7–14", icon: "💝" },
  { key: "st_patricks", label: "St. Patrick's Day", window: "Mar 15–17", icon: "🍀" },
  { key: "thanksgiving", label: "Thanksgiving", window: "Nov 22–30", icon: "🦃" },
  { key: "christmas", label: "Christmas", window: "Dec 1–31", icon: "🎄" },
  { key: "halloween", label: "Halloween", window: "All October", icon: "🎃" },
  {
    key: "saturday_morning",
    label: "Saturday Morning Cartoons",
    window: "Saturday before 1pm",
    icon: "📺",
  },
  {
    key: "summer_blockbuster",
    label: "Summer Blockbusters",
    window: "June – August",
    icon: "🌴",
  },
];

function SeasonalParamField({ params, onChange }: ParamFieldProps) {
  // Resolve the current enabled set, falling back to the legacy single-theme
  // shape when the section was saved before EnabledThemes existed.
  const rawEnabled = params.enabled_themes;
  const enabled = new Set<string>(
    Array.isArray(rawEnabled)
      ? rawEnabled.filter((v): v is string => typeof v === "string")
      : typeof params.theme === "string" && params.theme
        ? [params.theme]
        : [],
  );

  // theme_titles is an optional map of theme key → custom display name.
  const rawTitles = params.theme_titles;
  const themeTitles: Record<string, string> = {};
  if (rawTitles && typeof rawTitles === "object" && !Array.isArray(rawTitles)) {
    for (const [k, v] of Object.entries(rawTitles)) {
      if (typeof v === "string") themeTitles[k] = v;
    }
  }

  function commit(nextEnabled: Set<string>, nextTitles: Record<string, string>) {
    // Drop empty/whitespace-only titles and titles for disabled themes so the
    // saved config stays tidy.
    const cleanedTitles: Record<string, string> = {};
    for (const [k, v] of Object.entries(nextTitles)) {
      if (!nextEnabled.has(k)) continue;
      const trimmed = v.trim();
      if (trimmed) cleanedTitles[k] = trimmed;
    }
    onChange({
      ...params,
      enabled_themes: Array.from(nextEnabled),
      theme_titles: Object.keys(cleanedTitles).length > 0 ? cleanedTitles : undefined,
      // Clear legacy fields so they don't shadow multi-theme resolution.
      theme: "",
      mode: "",
    });
  }

  function toggle(key: string, on: boolean) {
    const next = new Set(enabled);
    if (on) next.add(key);
    else next.delete(key);
    commit(next, themeTitles);
  }

  function setTitle(key: string, value: string) {
    commit(enabled, { ...themeTitles, [key]: value });
  }

  return (
    <div className="space-y-2">
      <span className="block text-xs text-white/70">Holidays to celebrate</span>
      <div className="space-y-2 rounded border border-white/10 bg-white/5 px-3 py-2">
        {SEASONAL_THEMES.map((t) => {
          const isOn = enabled.has(t.key);
          return (
            <div key={t.key} className="space-y-1">
              <label className="flex items-center justify-between gap-3 text-sm">
                <span className="flex items-center gap-2">
                  <span aria-hidden>{t.icon}</span>
                  <span>{t.label}</span>
                  <span className="text-[10px] text-white/40">{t.window}</span>
                </span>
                <input
                  type="checkbox"
                  checked={isOn}
                  onChange={(e) => toggle(t.key, e.target.checked)}
                />
              </label>
              {isOn && (
                <input
                  type="text"
                  value={themeTitles[t.key] ?? ""}
                  onChange={(e) => setTitle(t.key, e.target.value)}
                  placeholder={`Section title in season — defaults to "${t.label}"`}
                  className="ml-7 w-[calc(100%-1.75rem)] rounded border border-white/10 bg-white/5 px-2 py-1 text-xs"
                />
              )}
            </div>
          );
        })}
      </div>
      <p className="text-[11px] text-white/50">
        The section auto-cycles: it shows whichever enabled holiday is currently in season, and
        hides itself when none match. Per-holiday titles override the section name only while that
        holiday is active.
      </p>
    </div>
  );
}

function CollectionParamField({ params, onChange }: ParamFieldProps) {
  const { collections, isLoading } = useAllUserCollections();
  const libraryID = (params.library_collection_id as string) ?? "";
  const userID = (params.user_collection_id as string) ?? "";
  const value = userID || libraryID;
  const sourceProvider = typeof params.source_provider === "string" ? params.source_provider : "";
  const sourcePreset = typeof params.source_preset === "string" ? params.source_preset : "";
  const mediaType = typeof params.media_type === "string" ? params.media_type : "";
  const isTraktPreset = sourceProvider === "trakt";
  const isAutoBackedTraktPreset =
    isTraktPreset && (sourcePreset === "trending" || sourcePreset === "popular");
  const collectionOptions = isTraktPreset
    ? collections.filter((collection) => {
        if (collection.source !== "library" || collection.collection_type !== "trakt") {
          return false;
        }
        const sourceConfig = collection.source_config;
        if (!sourceConfig || typeof sourceConfig !== "object" || Array.isArray(sourceConfig)) {
          return false;
        }
        return sourceConfig.preset === sourcePreset && sourceConfig.media_type === mediaType;
      })
    : collections;

  if (isAutoBackedTraktPreset && !value) {
    return (
      <p className="text-xs text-white/50">
        A synced Trakt {sourcePreset} {mediaType === "tv" ? "shows" : "movies"} collection will be
        created automatically.
      </p>
    );
  }

  return (
    <div className="space-y-1">
      <span className="block text-xs text-white/70">Collection</span>
      <CollectionSearchableSelect
        options={collectionOptions}
        value={value}
        onChange={(next) => {
          // Pick the right param key based on the chosen collection's source.
          // Clearing the selection wipes both fields.
          if (!next) {
            onChange({ ...params, library_collection_id: "", user_collection_id: "" });
            return;
          }
          const picked = collectionOptions.find((c) => c.id === next);
          if (picked?.source === "user") {
            onChange({ ...params, library_collection_id: "", user_collection_id: next });
          } else {
            onChange({ ...params, library_collection_id: next, user_collection_id: "" });
          }
        }}
        disabled={isLoading}
        isLoading={isLoading}
      />
      {isTraktPreset && !isLoading && collectionOptions.length === 0 ? (
        <p className="text-xs text-amber-300">
          No synced Trakt {sourcePreset} {mediaType === "tv" ? "shows" : "movies"} collection was
          found. Create and sync one from Admin Collections first.
        </p>
      ) : null}
    </div>
  );
}
