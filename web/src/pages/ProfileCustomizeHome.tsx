import { useEffect, useState } from "react";
import PageBack from "@/components/PageBack";
import ProfileSectionRow from "@/components/ProfileSectionRow";
import RecipeGalleryModal from "@/components/RecipeGallery/RecipeGalleryModal";
import RecipeConfigDrawer from "@/components/RecipeGallery/RecipeConfigDrawer";
import type { components } from "@/api/v2/schema";
import { v2 } from "@/api/v2/request";
import { fetchRecipeCatalog, type GalleryPreset, type RecipeDefinition } from "@/lib/recipes";
import { useQuery } from "@tanstack/react-query";
import { useUserLibraries } from "@/hooks/queries/libraries";
import SectionEditorDrawer from "@/components/sections/SectionEditorDrawer";
import type { SettingsSectionEntry } from "@/api/types";
import { toast } from "sonner";

// A stored override as GET /api/v2/profile/sections returns it. We round-trip
// these through the page state so admin-section customizations (hide/title/etc.)
// and user-added rows (recipe + config + title) survive every save. The legacy
// approach of rebuilding the payload from the resolved `sections` view dropped
// overrides for any section the user hadn't just touched, and lost user_config
// / item_limit / featured on user-added rows.
type StoredOverride = components["schemas"]["SectionOverride"];
type OverrideWrite = components["schemas"]["SectionOverrideWrite"];

// toWrite narrows a stored override to the members PUT /api/v2/profile/sections
// accepts; the store timestamps are read-only.
function toWrite(o: StoredOverride): OverrideWrite {
  return {
    id: o.id || undefined,
    section_id: o.section_id,
    position: o.position,
    hidden: o.hidden,
    removed: o.removed,
    section_type: o.section_type,
    title: o.title,
    featured: o.featured,
    item_limit: o.item_limit,
    config: o.config,
    is_user_added: o.is_user_added,
    user_section_type: o.user_section_type,
    user_config: o.user_config,
    user_title: o.user_title,
  };
}

function emptyOverride(): StoredOverride {
  return {
    id: "",
    section_id: "",
    position: null,
    hidden: false,
    removed: false,
    section_type: "",
    title: "",
    featured: null,
    item_limit: null,
    is_user_added: false,
    user_section_type: "",
    user_title: "",
    created_at: null,
    updated_at: null,
  };
}

export default function ProfileCustomizeHome() {
  const [sections, setSections] = useState<SettingsSectionEntry[]>([]);
  const [rawOverrides, setRawOverrides] = useState<StoredOverride[]>([]);
  const [galleryOpen, setGalleryOpen] = useState(false);
  const [picked, setPicked] = useState<{ def: RecipeDefinition; preset: GalleryPreset } | null>(
    null,
  );
  const [allowCustom, setAllowCustom] = useState(false);
  const [editor, setEditor] = useState<{ section: SettingsSectionEntry | null } | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [hasLoaded, setHasLoaded] = useState(false);
  const { data: libraries = [] } = useUserLibraries();
  const recipes = useQuery({ queryKey: ["sectionRecipeCatalog"], queryFn: fetchRecipeCatalog });

  async function load() {
    // Fetch both:
    //   - /profile/sections/settings → resolved view (admin sections + user-added,
    //     including hidden) for the row list
    //   - /profile/sections → stored overrides, which we round-trip on save
    //     so unrelated overrides aren't dropped by full-replacement PUTs
    try {
      const [data, raw] = await Promise.all([
        v2("GET /api/v2/profile/sections/settings", { query: { scope: "home" } }),
        v2("GET /api/v2/profile/sections", { query: { scope: "home" } }),
      ]);
      setSections(
        data.items.map((s) => ({
          id: s.id,
          is_custom: s.is_custom,
          section_type: s.section_type,
          title: s.title,
          hidden: s.hidden,
          featured: s.featured,
          item_limit: s.item_limit,
          position: s.position,
          customized: s.customized,
          config: s.config,
        })),
      );
      setRawOverrides(raw.items);
      setHasLoaded(true);
      setLoadError(false);
    } catch (err) {
      console.error("load sections failed:", err);
      setLoadError(true);
    }
  }

  async function loadSetting() {
    try {
      const j = await v2("GET /api/v2/profile/sections/flags");
      setAllowCustom(j.allow_profile_custom_sections);
    } catch {
      // Setting just defaults to false on failure.
    }
  }

  useEffect(() => {
    void load();
    void loadSetting();
  }, []);

  async function saveOverrides(
    updates: Array<{ id: string; hidden?: boolean; removed?: boolean }>,
  ) {
    // PUT /profile/sections is a full-replacement save for this profile+scope.
    // Start from the existing stored overrides so unrelated customizations
    // (admin-section hides, user-added recipes' config/title) are preserved,
    // then mutate by id. The resolved section id matches an override's
    // section_id for admin customizations and id for user-added rows.
    const matches = (o: StoredOverride, sectionID: string) =>
      o.section_id === sectionID || (o.section_id === "" && o.id === sectionID);

    const merged: StoredOverride[] = rawOverrides.map((o) => {
      const u = updates.find((up) => matches(o, up.id));
      if (!u) return o;
      return {
        ...o,
        hidden: u.hidden ?? o.hidden,
        removed: u.removed ?? o.removed,
      };
    });

    // For sections that have no existing override yet (admin sections still at
    // their server defaults), synthesize a fresh admin-customization row.
    for (const u of updates) {
      if (merged.some((o) => matches(o, u.id))) continue;
      const section = sections.find((s) => s.id === u.id);
      if (!section || section.is_custom) continue; // user-added sections always have an override
      merged.push({
        ...emptyOverride(),
        section_id: u.id,
        hidden: u.hidden ?? false,
        removed: u.removed ?? false,
      });
    }

    try {
      await v2("PUT /api/v2/profile/sections", {
        query: { scope: "home" },
        body: { overrides: merged.map(toWrite) },
      });
    } catch {
      toast.error("Could not save section changes. Please retry.");
      return;
    }
    void load();
  }

  async function reset() {
    try {
      await v2("DELETE /api/v2/profile/sections", { query: { scope: "home" } });
    } catch {
      toast.error("Could not reset sections. Please retry.");
      return;
    }
    void load();
  }

  async function saveSection(section: SettingsSectionEntry) {
    const previous = rawOverrides.find((o) => o.is_user_added && o.id === section.id);
    const changed: StoredOverride = {
      ...(previous ?? emptyOverride()),
      featured: section.featured,
      item_limit: section.item_limit,
      hidden: section.hidden,
      is_user_added: true,
      user_section_type: section.section_type,
      user_config: section.config ?? {},
      user_title: section.title,
    };
    try {
      await v2("PUT /api/v2/profile/sections", {
        query: { scope: "home" },
        body: {
          overrides: (previous
            ? rawOverrides.map((o) => (o.id === previous.id ? changed : o))
            : [...rawOverrides, changed]
          ).map(toWrite),
        },
      });
    } catch (err) {
      toast.error("Could not save section changes. Please retry.");
      throw err;
    }
    setEditor(null);
    await load();
  }

  if (loadError)
    return (
      <div role="alert" className="p-6">
        Could not load sections. <button onClick={() => void load()}>Retry</button>
      </div>
    );

  if (!hasLoaded) return <div className="p-6">Loading sections...</div>;

  return (
    <div className="relative mx-auto max-w-3xl p-6">
      <PageBack />
      <div className="mt-10 flex items-center justify-between border-b border-white/10 pb-3 sm:mt-12">
        <h1 className="text-base font-semibold">Customize home</h1>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => setGalleryOpen(true)}
            className="rounded bg-indigo-600 px-3 py-1.5 text-sm text-white"
          >
            + Add from Gallery
          </button>
          {allowCustom && (
            <button
              type="button"
              onClick={() => setEditor({ section: null })}
              className="rounded border border-white/15 px-3 py-1.5 text-sm"
            >
              + Build Custom
            </button>
          )}
        </div>
      </div>

      <div className="my-3 flex justify-end">
        <button type="button" onClick={reset} className="text-xs underline opacity-65">
          ↻ Reset to server defaults
        </button>
      </div>

      <div className="rounded-lg border border-white/10">
        {sections.map((s) => (
          <ProfileSectionRow
            key={s.id}
            kind={s.is_custom ? "yours" : "server-default"}
            title={s.title}
            sectionType={s.section_type}
            hidden={s.hidden}
            onHide={() => void saveOverrides([{ id: s.id, hidden: true }])}
            onShow={() => void saveOverrides([{ id: s.id, hidden: false }])}
            onEdit={() => setEditor({ section: s })}
            onDelete={() => void saveOverrides([{ id: s.id, removed: true }])}
          />
        ))}
      </div>

      {editor && (
        <SectionEditorDrawer
          mode="profile"
          open
          section={editor.section}
          libraries={libraries}
          recipeCatalog={recipes.data}
          onOpenChange={(open) => {
            if (!open) setEditor(null);
          }}
          onSave={saveSection}
        />
      )}

      <RecipeGalleryModal
        open={galleryOpen}
        onClose={() => setGalleryOpen(false)}
        hideAdminOnly
        onPick={(def, preset) => {
          setGalleryOpen(false);
          setPicked({ def, preset });
        }}
      />

      {picked && (
        <RecipeConfigDrawer
          def={picked.def}
          preset={picked.preset}
          showBulkApply={false}
          onCancel={() => setPicked(null)}
          onBackToGallery={() => {
            setPicked(null);
            setGalleryOpen(true);
          }}
          onAdd={async (payload) => {
            // Append a new user-added row to the existing override set so admin
            // customizations and prior custom sections aren't dropped by the
            // full-replacement PUT.
            const newRow: StoredOverride = {
              ...emptyOverride(),
              hidden: !payload.enabled,
              featured: payload.featured,
              item_limit: payload.item_limit,
              is_user_added: true,
              user_section_type: payload.section_type,
              user_config: payload.config ?? {},
              user_title: payload.title ?? "",
            };
            try {
              await v2("PUT /api/v2/profile/sections", {
                query: { scope: "home" },
                body: { overrides: [...rawOverrides, newRow].map(toWrite) },
              });
            } catch (err) {
              toast.error("Could not add section. Please retry.");
              throw err;
            }
            setPicked(null);
            void load();
          }}
        />
      )}
    </div>
  );
}
