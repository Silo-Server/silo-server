import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import SectionEditorDrawer from "./SectionEditorDrawer";
import type { RecipeCatalogResponse } from "@/lib/recipes";

vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({ collections: [], isLoading: false }),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useAvailableUserLibraries: () => ({ data: [{ id: 1, name: "Profile Movies" }] }),
}));

const recipeCatalog = {
  categories: {
    library_staples: ["recently_added", "recently_released"].map((type) => ({
      type,
      category: "library_staples",
      avoid_duplicates: false,
      supports_rotation: false,
      admin_only: false,
      presets: [
        {
          key: "ra",
          display_name: "Recently Added",
          icon: "🆕",
          description_short: "Latest",
          default_params: {},
        },
      ],
    })),
  },
} as unknown as RecipeCatalogResponse;

const adminSection = {
  id: "7",
  scope: "home",
  title: "Recently Added in TV",
  section_type: "recently_added",
  item_limit: 20,
  featured: false,
  enabled: true,
  position: 0,
  config: { generated_source: "home_library_recent", filter_library_id: 2 },
};

function renderAdmin(scope: string, onSave = vi.fn(), sectionType = "recently_added") {
  return render(
    <SectionEditorDrawer
      mode="admin"
      open
      onOpenChange={() => {}}
      section={{ ...adminSection, scope, section_type: sectionType } as never}
      scope={scope}
      currentLibraryId={scope === "library" ? 2 : null}
      libraries={[
        { id: 1, name: "Movies" },
        { id: 2, name: "TV" },
      ]}
      recipeCatalog={recipeCatalog}
      onSave={onSave}
    />,
  );
}

describe("SectionEditorDrawer library picker", () => {
  it.each(["recently_added", "recently_released"])(
    "preserves %s ownership when a library selection is reverted before saving",
    async (sectionType) => {
      const onSave = vi.fn();
      renderAdmin("home", onSave, sectionType);
      expect(screen.getByRole("button", { name: "Libraries" }).textContent).toContain("TV");
      await userEvent.click(screen.getByRole("button", { name: "Libraries" }));
      await userEvent.click(await screen.findByRole("menuitemcheckbox", { name: "Movies" }));
      await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "TV" }));
      await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "TV" }));
      await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Movies" }));
      await userEvent.keyboard("{Escape}");
      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({
          config: {
            generated_source: "home_library_recent",
            generated_library_id: 2,
            filter_library_ids: [2],
          },
        }),
      );
    },
  );

  it("hides the picker on an admin library page", () => {
    renderAdmin("library");
    expect(screen.queryByRole("button", { name: "Libraries" })).toBeNull();
  });

  it("hides the picker when a profile edits a library page", () => {
    render(
      <SectionEditorDrawer
        mode="profile"
        open
        onOpenChange={() => {}}
        section={{
          id: "s1",
          section_type: "recently_added",
          title: "Recently Added",
          featured: false,
          item_limit: 20,
          hidden: false,
          is_custom: true,
          customized: false,
          position: 0,
          config: {},
        }}
        libraries={[]}
        recipeCatalog={recipeCatalog}
        libraryScoped
        onSave={() => {}}
      />,
    );
    expect(screen.queryByRole("button", { name: "Libraries" })).toBeNull();
  });
});
