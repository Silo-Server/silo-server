import { useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { CollectionSearchableSelect } from "@/components/CollectionSearchableSelect";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { useAllUserCollections } from "@/hooks/queries/useAllUserCollections";
import { useAvailableUserLibraries } from "@/hooks/queries/libraries";
import { createCatalogSearchState, fetchCatalogPage } from "@/hooks/queries/catalog";
import { fetchWatchDetail } from "@/hooks/queries/items";
import { catalogKeys, itemKeys } from "@/hooks/queries/keys";
import { useDebounce } from "@/hooks/useDebounce";
import type { BrowseItem } from "@/api/types";
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
  if (def.type === "admin_curated_list") {
    return <CuratedItemsParamField params={params} onChange={onChange} />;
  }
  if (def.type === "returning_shows") {
    return (
      <NumberParamField
        params={params}
        onChange={onChange}
        paramKey="lookback_days"
        label="Lookback window (days)"
        placeholder="30"
        hint="How far back a new season counts as “just arrived”."
      />
    );
  }
  if (def.type === "short_watches") {
    return (
      <NumberParamField
        params={params}
        onChange={onChange}
        paramKey="max_minutes"
        label="Maximum runtime (minutes)"
        placeholder="95"
        hint="Movies at or under this runtime qualify."
      />
    );
  }
  if (def.type === "anniversaries") {
    return (
      <NumberParamField
        params={params}
        onChange={onChange}
        paramKey="milestone_years"
        label="Milestone (years)"
        placeholder="5"
        hint="Only anniversaries that are a multiple of this many years. Set 1 for every anniversary."
      />
    );
  }
  if (def.type === "editorial_spotlight") {
    const subjectType = (params.subject_type as string) ?? "director";
    // A preset that ships a pinned subject (e.g. "The 80s") without an
    // explicit auto_rotate is a pinned config — mirroring the backend's Go
    // zero-value. Defaulting the toggle to ON here would hide the subject
    // field and misrepresent what gets saved.
    const autoRotate = (params.auto_rotate as boolean) ?? !params.subject;
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
          placeholder="Auto-pick your strongest genre (leave blank)"
          value={genre}
          onChange={(e) => onChange({ ...params, genre: e.target.value })}
        />
        <div className="mt-1 text-[11px] text-white/50">
          Leave blank to follow the profile&apos;s strongest taste automatically.
        </div>
      </div>
    );
  }
  return null;
}

interface ParamFieldProps {
  params: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

// Sort choices for watchlist/favorites sections. The empty value keeps the
// list's stored order (provider sync order, then newest-added first).
const PERSONAL_LIST_SORT_OPTIONS = [
  { value: "", label: "List order (default)" },
  { value: "added_at:desc", label: "Date added (newest first)" },
  { value: "added_at:asc", label: "Date added (oldest first)" },
  { value: "title:asc", label: "Title (A–Z)" },
  { value: "title:desc", label: "Title (Z–A)" },
  { value: "release_date:desc", label: "Release date (newest first)" },
  { value: "release_date:asc", label: "Release date (oldest first)" },
  { value: "rating_imdb:desc", label: "IMDb rating (highest first)" },
];

// PersonalListFilterFields edits the optional filter_type / filter_library_ids
// filters and sort for watchlist and favorites sections, e.g. a "Movies
// watchlist" rail sorted by release date.
function PersonalListFilterFields({ params, onChange }: ParamFieldProps) {
  const { data: libraries } = useAvailableUserLibraries();
  const filterType = typeof params.filter_type === "string" ? params.filter_type : "";
  const libraryIds = Array.isArray(params.filter_library_ids)
    ? params.filter_library_ids.filter((id): id is number => typeof id === "number")
    : [];
  const sortField = typeof params.sort === "string" ? params.sort : "";
  const sortOrder =
    typeof params.order === "string" && params.order
      ? params.order
      : sortField === "title"
        ? "asc" // mirror the backend's per-field default order
        : "desc";
  const sortValue = sortField ? `${sortField}:${sortOrder}` : "";

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
      <label className="block md:col-span-2">
        <span className="mb-1 block text-xs text-white/70">Sort</span>
        <select
          value={PERSONAL_LIST_SORT_OPTIONS.some((o) => o.value === sortValue) ? sortValue : ""}
          onChange={(e) => {
            const [sort, order] = e.target.value.split(":");
            onChange({ ...params, sort: sort || undefined, order: order || undefined });
          }}
          className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
        >
          {PERSONAL_LIST_SORT_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
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
    key: "family_movie_night",
    label: "Family Movie Night",
    window: "Fri & Sat from 5pm",
    icon: "🍿",
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

// NumberParamField edits a single optional integer param (e.g. lookback_days).
// Clearing the input removes the key so the backend default applies.
function NumberParamField({
  params,
  onChange,
  paramKey,
  label,
  placeholder,
  hint,
}: ParamFieldProps & {
  paramKey: string;
  label: string;
  placeholder: string;
  hint?: string;
}) {
  const raw = params[paramKey];
  const value = typeof raw === "number" && Number.isFinite(raw) ? String(raw) : "";
  return (
    <div>
      <label className="mb-1 block text-xs text-white/70">{label}</label>
      <input
        type="number"
        min={1}
        step={1}
        className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
        placeholder={placeholder}
        value={value}
        onChange={(e) => {
          const parsed = Number(e.target.value);
          // All numeric recipe params are Go ints server-side; storing a
          // fractional value would fail JSON unmarshalling at save time.
          onChange({
            ...params,
            [paramKey]:
              e.target.value && Number.isInteger(parsed) && parsed > 0 ? parsed : undefined,
          });
        }}
      />
      {hint ? <div className="mt-1 text-[11px] text-white/50">{hint}</div> : null}
    </div>
  );
}

const CURATED_SEARCH_LIMIT = 10;
const CURATED_SEARCH_DEBOUNCE_MS = 250;

function curatedItemLabel(title: string, year?: number): string {
  return year ? `${title} (${year})` : title;
}

// CuratedItemsParamField builds the ordered item_ids list for
// admin_curated_list sections: catalog search on top, the picked (ordered)
// list below. Titles for freshly added items come from the search result;
// items persisted before this drawer opened are hydrated from the item
// detail endpoint, falling back to the raw id while loading.
function CuratedItemsParamField({ params, onChange }: ParamFieldProps) {
  const [query, setQuery] = useState("");
  const [labels, setLabels] = useState<Record<string, string>>({});
  const debounced = useDebounce(query.trim(), CURATED_SEARCH_DEBOUNCE_MS);

  const itemIDs = Array.isArray(params.item_ids)
    ? params.item_ids.filter((id): id is string => typeof id === "string")
    : [];
  const picked = new Set(itemIDs);

  // Hydrate display titles for ids we have no label for (pre-existing config
  // being edited). Cached under the same key as the watch-detail hook.
  const unlabeled = itemIDs.filter((id) => !(id in labels));
  const detailQueries = useQueries({
    queries: unlabeled.map((id) => ({
      queryKey: itemKeys.watchDetail(id),
      queryFn: ({ signal }: { signal?: AbortSignal }) =>
        fetchWatchDetail(id, undefined, undefined, { signal }),
      staleTime: 5 * 60 * 1000,
      retry: false,
    })),
  });
  const hydratedLabels: Record<string, string> = {};
  unlabeled.forEach((id, i) => {
    const detail = detailQueries[i]?.data;
    if (detail) hydratedLabels[id] = curatedItemLabel(detail.title, detail.year);
  });

  const searchState = useMemo(
    () => createCatalogSearchState("query", { q: debounced || undefined }),
    [debounced],
  );
  const results = useQuery({
    queryKey: [
      "curatedListPicker",
      catalogKeys.list({
        source: searchState.source,
        q: searchState.q,
        limit: CURATED_SEARCH_LIMIT,
        offset: 0,
      }),
    ],
    queryFn: ({ signal }) => fetchCatalogPage(searchState, CURATED_SEARCH_LIMIT, 0, { signal }),
    enabled: debounced.length > 0,
    staleTime: 30 * 1000,
  });
  const found: BrowseItem[] = results.data?.items ?? [];

  function add(item: BrowseItem) {
    if (picked.has(item.content_id)) return;
    setLabels((prev) => ({
      ...prev,
      [item.content_id]: curatedItemLabel(item.title, item.year),
    }));
    onChange({ ...params, item_ids: [...itemIDs, item.content_id] });
  }

  function remove(id: string) {
    onChange({ ...params, item_ids: itemIDs.filter((existing) => existing !== id) });
  }

  function move(id: string, delta: number) {
    const idx = itemIDs.indexOf(id);
    const target = idx + delta;
    if (idx < 0 || target < 0 || target >= itemIDs.length) return;
    const next = [...itemIDs];
    next.splice(idx, 1);
    next.splice(target, 0, id);
    onChange({ ...params, item_ids: next });
  }

  return (
    <div className="space-y-3">
      <div>
        <label className="mb-1 block text-xs text-white/70">Add titles</label>
        <input
          className="w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
          placeholder="🔍 Search your catalog…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          autoComplete="off"
        />
        {debounced.length === 0 ? null : results.isLoading ? (
          <div className="mt-2 text-xs text-white/50">Searching…</div>
        ) : results.isError ? (
          <div className="mt-2 text-xs text-amber-300">Search failed — try again.</div>
        ) : found.length === 0 ? (
          <div className="mt-2 text-xs text-white/50">No matches.</div>
        ) : (
          <ul className="mt-2 max-h-52 divide-y divide-white/10 overflow-y-auto rounded border border-white/10">
            {found.map((item) => {
              const already = picked.has(item.content_id);
              return (
                <li key={item.content_id} className="flex items-center gap-2 px-3 py-2 text-sm">
                  <span className="min-w-0 flex-1 truncate">
                    {item.title}
                    <span className="ml-1 text-xs text-white/40">
                      {item.year ? `${item.year} · ` : ""}
                      {item.type}
                    </span>
                  </span>
                  <button
                    type="button"
                    disabled={already}
                    onClick={() => add(item)}
                    className="rounded border border-white/15 px-2 py-0.5 text-xs disabled:opacity-40"
                  >
                    {already ? "Added" : "Add"}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <div>
        <span className="mb-1 block text-xs text-white/70">
          Curated list ({itemIDs.length} {itemIDs.length === 1 ? "title" : "titles"}, shown in this
          order)
        </span>
        {itemIDs.length === 0 ? (
          <div className="rounded border border-dashed border-white/15 px-3 py-3 text-xs text-white/50">
            Search above and add at least one title.
          </div>
        ) : (
          <ul className="divide-y divide-white/10 rounded border border-white/10">
            {itemIDs.map((id, idx) => (
              <li key={id} className="flex items-center gap-2 px-3 py-2 text-sm">
                <span className="min-w-0 flex-1 truncate">
                  {labels[id] ?? hydratedLabels[id] ?? id}
                </span>
                <button
                  type="button"
                  onClick={() => move(id, -1)}
                  disabled={idx === 0}
                  aria-label="Move up"
                  className="rounded border border-white/15 px-2 py-0.5 text-xs disabled:opacity-40"
                >
                  ↑
                </button>
                <button
                  type="button"
                  onClick={() => move(id, 1)}
                  disabled={idx === itemIDs.length - 1}
                  aria-label="Move down"
                  className="rounded border border-white/15 px-2 py-0.5 text-xs disabled:opacity-40"
                >
                  ↓
                </button>
                <button
                  type="button"
                  onClick={() => remove(id)}
                  aria-label="Remove"
                  className="rounded border border-white/15 px-2 py-0.5 text-xs text-red-300"
                >
                  ✕
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
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
