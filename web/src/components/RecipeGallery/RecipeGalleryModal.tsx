import { useEffect, useMemo, useState } from "react";
import RecipeCard from "./RecipeCard";
import {
  fetchRecipeCatalog,
  type Category,
  type GalleryPreset,
  type RecipeDefinition,
} from "@/lib/recipes";

function score(query: string, preset: GalleryPreset): number {
  const q = query.toLowerCase();
  const name = preset.display_name.toLowerCase();
  const desc = preset.description_short.toLowerCase();
  if (name === q) return 100;
  if (name.startsWith(q)) return 80;
  if (name.includes(q)) return 60;
  if (desc.includes(q)) return 30;
  return 0;
}

const CATEGORY_LABELS: Record<Category, string> = {
  library_staples: "Library staples",
  personalized: "Personalized",
  discovery: "Discovery",
  editorial: "Editorial",
  seasonal: "Seasonal",
  mood: "Mood",
  hand_picked: "Hand-picked",
  social: "Social",
  custom: "Custom",
};

interface Props {
  open: boolean;
  onClose: () => void;
  onPick: (def: RecipeDefinition, preset: GalleryPreset) => void;
  /**
   * Hide recipes flagged admin_only by the backend. Set on profile-facing
   * surfaces (customize home, home settings); the admin sections page shows
   * everything.
   */
  hideAdminOnly?: boolean;
}

export default function RecipeGalleryModal({ open, onClose, onPick, hideAdminOnly }: Props) {
  const [catalog, setCatalog] = useState<Partial<Record<Category, RecipeDefinition[]>>>({});
  const [search, setSearch] = useState("");
  const [activeCategory, setActiveCategory] = useState<Category | "all">("all");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    fetchRecipeCatalog()
      .then((res) => setCatalog(res.categories))
      .catch((err) => setError(String(err)));
  }, [open]);

  const flat = useMemo(() => {
    const rows: { def: RecipeDefinition; preset: GalleryPreset; score: number }[] = [];
    for (const cat of Object.keys(catalog) as Category[]) {
      if (activeCategory !== "all" && activeCategory !== cat) continue;
      for (const def of catalog[cat] ?? []) {
        if (hideAdminOnly && def.admin_only) continue;
        for (const preset of def.presets ?? []) {
          const s = search.trim() ? score(search.trim(), preset) : 1;
          if (s === 0) continue;
          rows.push({ def, preset, score: s });
        }
      }
    }
    if (search.trim()) {
      rows.sort((a, b) => b.score - a.score);
    }
    return rows;
  }, [catalog, search, activeCategory, hideAdminOnly]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={onClose}
    >
      <div
        className="max-h-[80vh] w-[800px] overflow-y-auto rounded-xl border border-white/10 bg-zinc-900 p-6"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-white/10 pb-3">
          <h2 className="text-base font-semibold">Add a section</h2>
          <button type="button" onClick={onClose} className="text-white/60 hover:text-white">
            ✕
          </button>
        </div>

        {error && <div className="mt-3 text-sm text-red-400">Error loading recipes: {error}</div>}

        <input
          className="mt-4 w-full rounded border border-white/15 bg-white/5 px-3 py-2 text-sm"
          placeholder="🔍 Search recipes..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />

        <div className="mt-4 flex flex-wrap gap-2">
          <button
            type="button"
            onClick={() => setActiveCategory("all")}
            className={`rounded-full px-3 py-1 text-xs ${activeCategory === "all" ? "bg-indigo-500 text-white" : "bg-white/5 text-white/80"}`}
          >
            All
          </button>
          {(Object.keys(catalog) as Category[]).map((c) => (
            <button
              key={c}
              type="button"
              onClick={() => setActiveCategory(c)}
              className={`rounded-full px-3 py-1 text-xs ${activeCategory === c ? "bg-indigo-500 text-white" : "bg-white/5 text-white/80"}`}
            >
              {CATEGORY_LABELS[c]}
            </button>
          ))}
        </div>

        <div className="mt-4 grid grid-cols-3 gap-3">
          {flat.map(({ def, preset }) => (
            <RecipeCard
              key={`${def.type}:${preset.key}`}
              preset={preset}
              category={def.category}
              onPick={() => onPick(def, preset)}
            />
          ))}
          {flat.length === 0 && (
            <div className="col-span-3 flex flex-col items-center justify-center py-12 text-center text-sm text-white/50">
              <div className="mb-2 text-3xl">🔍</div>
              <div>No recipes match {search ? `"${search}"` : "this filter"}.</div>
              <button
                type="button"
                onClick={() => {
                  setSearch("");
                  setActiveCategory("all");
                }}
                className="mt-3 text-xs underline opacity-80"
              >
                Clear filters
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
