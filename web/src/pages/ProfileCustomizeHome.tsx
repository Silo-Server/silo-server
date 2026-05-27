import { useEffect, useState } from "react";
import PageBack from "@/components/PageBack";
import ProfileSectionRow from "@/components/ProfileSectionRow";
import RecipeGalleryModal from "@/components/RecipeGallery/RecipeGalleryModal";
import RecipeConfigDrawer from "@/components/RecipeGallery/RecipeConfigDrawer";
import { api } from "@/api/client";
import type { GalleryPreset, RecipeDefinition } from "@/lib/recipes";

interface ProfileSection {
  id: string;
  is_custom: boolean;
  section_type: string;
  title: string;
  hidden: boolean;
}

interface SectionsSettingsResponse {
  sections?: Array<{
    id: string;
    is_custom?: boolean;
    section_type: string;
    title: string;
    hidden?: boolean;
  }>;
}

interface ProfileSectionsFlagResponse {
  allow_profile_custom_sections?: boolean;
}

// RawOverride mirrors the JSON shape returned by GET /profile/sections —
// userstore.SectionOverride marshalled with no JSON tags, so fields appear in
// Go-PascalCase. We round-trip these through the page state so admin-section
// customizations (hide/title/etc.) and user-added rows (recipe + config + title)
// survive every save. The legacy approach of rebuilding the payload from the
// resolved `sections` view dropped overrides for any section the user hadn't
// just touched, and lost user_config / item_limit / featured on user-added rows.
interface RawOverride {
  ID: string;
  SectionID: string;
  Position: number | null;
  Hidden: boolean;
  Removed: boolean;
  SectionType: string;
  Title: string;
  Featured: boolean | null;
  ItemLimit: number | null;
  Config: string;
  IsUserAdded: boolean;
  UserSectionType: string;
  UserConfig: string;
  UserTitle: string;
}

interface RawOverridesResponse {
  overrides?: RawOverride[];
}

function safeParseJSON(s: string): unknown {
  if (!s) return undefined;
  try {
    return JSON.parse(s);
  } catch {
    return undefined;
  }
}

// rawToWire converts a stored override into the snake_case shape expected by
// PUT /profile/sections (profileOverrideRequest). config / user_config are
// json.RawMessage on the server, so they must be sent as parsed JSON values.
function rawToWire(o: RawOverride) {
  return {
    id: o.ID || undefined,
    section_id: o.SectionID,
    position: o.Position ?? undefined,
    hidden: o.Hidden,
    removed: o.Removed,
    section_type: o.SectionType,
    title: o.Title,
    featured: o.Featured ?? undefined,
    item_limit: o.ItemLimit ?? undefined,
    config: safeParseJSON(o.Config),
    is_user_added: o.IsUserAdded,
    user_section_type: o.UserSectionType,
    user_config: safeParseJSON(o.UserConfig),
    user_title: o.UserTitle,
  };
}

export default function ProfileCustomizeHome() {
  const [sections, setSections] = useState<ProfileSection[]>([]);
  const [rawOverrides, setRawOverrides] = useState<RawOverride[]>([]);
  const [galleryOpen, setGalleryOpen] = useState(false);
  const [picked, setPicked] = useState<{ def: RecipeDefinition; preset: GalleryPreset } | null>(
    null,
  );
  const [allowCustom, setAllowCustom] = useState(false);

  async function load() {
    // Fetch both:
    //   - /profile/sections/settings → resolved view (admin sections + user-added,
    //     including hidden) for the row list
    //   - /profile/sections → raw stored overrides, which we round-trip on save
    //     so unrelated overrides aren't dropped by full-replacement PUTs
    try {
      const [data, raw] = await Promise.all([
        api<SectionsSettingsResponse>("/profile/sections/settings?scope=home"),
        api<RawOverridesResponse>("/profile/sections?scope=home"),
      ]);
      setSections(
        (data?.sections ?? []).map((s) => ({
          id: s.id,
          is_custom: !!s.is_custom,
          section_type: s.section_type,
          title: s.title,
          hidden: !!s.hidden,
        })),
      );
      setRawOverrides(raw?.overrides ?? []);
    } catch (err) {
      console.error("load sections failed:", err);
      setSections([]);
      setRawOverrides([]);
    }
  }

  async function loadSetting() {
    try {
      const j = await api<ProfileSectionsFlagResponse>("/profile/sections/flags");
      setAllowCustom(!!j.allow_profile_custom_sections);
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
    // SectionID for admin customizations and ID for user-added rows.
    const matches = (o: RawOverride, sectionID: string) =>
      o.SectionID === sectionID || (o.SectionID === "" && o.ID === sectionID);

    const merged: RawOverride[] = rawOverrides.map((o) => {
      const u = updates.find((up) => matches(o, up.id));
      if (!u) return o;
      return {
        ...o,
        Hidden: u.hidden ?? o.Hidden,
        Removed: u.removed ?? o.Removed,
      };
    });

    // For sections that have no existing override yet (admin sections still at
    // their server defaults), synthesize a fresh admin-customization row.
    for (const u of updates) {
      if (merged.some((o) => matches(o, u.id))) continue;
      const section = sections.find((s) => s.id === u.id);
      if (!section || section.is_custom) continue; // user-added sections always have an override
      merged.push({
        ID: "",
        SectionID: u.id,
        Position: null,
        Hidden: u.hidden ?? false,
        Removed: u.removed ?? false,
        SectionType: "",
        Title: "",
        Featured: null,
        ItemLimit: null,
        Config: "",
        IsUserAdded: false,
        UserSectionType: "",
        UserConfig: "",
        UserTitle: "",
      });
    }

    try {
      await api("/profile/sections", {
        method: "PUT",
        body: JSON.stringify({
          scope: "home",
          library_id: "",
          overrides: merged.map(rawToWire),
        }),
      });
    } catch (err) {
      console.error("save overrides failed:", err);
    }
    void load();
  }

  async function reset() {
    try {
      await api("/profile/sections/reset?scope=home", { method: "DELETE" });
    } catch (err) {
      console.error("reset overrides failed:", err);
    }
    void load();
  }

  return (
    <div className="relative mx-auto max-w-3xl p-6">
      <PageBack />
      <div className="flex items-center justify-between border-b border-white/10 pb-3">
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
            <button type="button" className="rounded border border-white/15 px-3 py-1.5 text-sm">
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
            onEdit={() => {
              // TODO: open the edit drawer for user-added recipes; out of scope here.
            }}
            onDelete={() => void saveOverrides([{ id: s.id, removed: true }])}
          />
        ))}
      </div>

      <RecipeGalleryModal
        open={galleryOpen}
        onClose={() => setGalleryOpen(false)}
        onPick={(def, preset) => {
          setGalleryOpen(false);
          setPicked({ def, preset });
        }}
      />

      {picked && (
        <RecipeConfigDrawer
          def={picked.def}
          preset={picked.preset}
          onCancel={() => setPicked(null)}
          onBackToGallery={() => {
            setPicked(null);
            setGalleryOpen(true);
          }}
          onAdd={async (payload) => {
            // Append a new user-added row to the existing override set so admin
            // customizations and prior custom sections aren't dropped by the
            // full-replacement PUT.
            const newRow: RawOverride = {
              ID: "",
              SectionID: "",
              Position: null,
              Hidden: false,
              Removed: false,
              SectionType: "",
              Title: "",
              Featured: payload.featured,
              ItemLimit: payload.item_limit,
              Config: "",
              IsUserAdded: true,
              UserSectionType: payload.section_type,
              UserConfig: JSON.stringify(payload.config ?? {}),
              UserTitle: payload.title ?? "",
            };
            try {
              await api("/profile/sections", {
                method: "PUT",
                body: JSON.stringify({
                  scope: "home",
                  library_id: "",
                  overrides: [...rawOverrides, newRow].map(rawToWire),
                }),
              });
            } catch (err) {
              console.error("add section failed:", err);
            }
            setPicked(null);
            void load();
          }}
        />
      )}
    </div>
  );
}
