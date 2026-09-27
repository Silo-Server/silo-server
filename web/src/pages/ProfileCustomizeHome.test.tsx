import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import ProfileCustomizeHome from "./ProfileCustomizeHome";
import type { SettingsSectionEntry } from "@/api/types";

const mocks = vi.hoisted(() => ({ request: vi.fn(), error: vi.fn() }));
vi.mock("@/api/v2/request", () => ({ v2: mocks.request }));
vi.mock("@/components/PageBack", () => ({ default: () => null }));
vi.mock("@/hooks/queries/libraries", () => ({ useUserLibraries: () => ({ data: [] }) }));
vi.mock("@/lib/recipes", () => ({ fetchRecipeCatalog: async () => ({ categories: {} }) }));
vi.mock("@/components/RecipeGallery/RecipeGalleryModal", () => ({ default: () => null }));
vi.mock("sonner", () => ({ toast: { error: mocks.error } }));
vi.mock("@/components/sections/SectionEditorDrawer", () => ({
  default: ({
    section,
    onSave,
  }: {
    section: SettingsSectionEntry | null;
    onSave: (s: SettingsSectionEntry) => Promise<void>;
  }) => (
    <div role="dialog">
      <span>{section?.title ?? "New section"}</span>
      <button
        onClick={() =>
          void onSave({
            ...section!,
            title: "Edited",
            item_limit: 12,
            featured: true,
            config: { genre: "Drama" },
          }).catch(() => {})
        }
      >
        Save changes
      </button>
    </div>
  ),
}));
const own = {
  id: "own",
  section_id: "",
  is_user_added: true,
  user_title: "Original",
  user_section_type: "recently_added",
  user_config: { genre: "Comedy" },
  position: 4,
  item_limit: 7,
  featured: false,
  hidden: false,
  removed: false,
};
const other = { id: "hide", section_id: "admin", hidden: true, removed: false, position: 2 };
beforeEach(() => {
  vi.clearAllMocks();
  mocks.request.mockImplementation(async (op: string) => {
    if (op === "GET /api/v2/profile/sections/settings")
      return {
        items: [
          {
            id: "own",
            is_custom: true,
            title: "Original",
            section_type: "recently_added",
            position: 4,
            item_limit: 7,
            featured: false,
            hidden: false,
            customized: false,
            config: { genre: "Comedy" },
          },
        ],
      };
    if (op === "GET /api/v2/profile/sections") return { items: [other, own] };
    if (op === "GET /api/v2/profile/sections/flags") return { allow_profile_custom_sections: true };
  });
});
function show() {
  return render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <ProfileCustomizeHome />
    </QueryClientProvider>,
  );
}
it("opens a saved section and preserves unrelated overrides and identity on edit", async () => {
  show();
  fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
  expect(screen.getByRole("dialog").textContent).toContain("Original");
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() =>
    expect(mocks.request).toHaveBeenCalledWith(
      "PUT /api/v2/profile/sections",
      expect.objectContaining({
        body: {
          overrides: [
            expect.objectContaining(other),
            expect.objectContaining({
              ...own,
              user_title: "Edited",
              user_config: { genre: "Drama" },
              featured: true,
              item_limit: 12,
            }),
          ],
        },
      }),
    ),
  );
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
it("keeps the edit draft open and reports a rejected save", async () => {
  mocks.request.mockImplementationOnce(async () => ({
    items: [
      {
        id: "own",
        is_custom: true,
        title: "Original",
        section_type: "recently_added",
        config: {},
        item_limit: 7,
      },
    ],
  }));
  show();
  fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
  const normal = mocks.request.getMockImplementation()!;
  mocks.request.mockImplementation((op: string, ...args: unknown[]) =>
    op.startsWith("PUT") ? Promise.reject(new Error("offline")) : normal(op, ...args),
  );
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(mocks.error).toHaveBeenCalled());
  expect(screen.getByRole("dialog")).toBeTruthy();
});
it("opens Build Custom instead of rendering an inert action", async () => {
  show();
  fireEvent.click(await screen.findByRole("button", { name: "+ Build Custom" }));
  expect(screen.getByRole("dialog").textContent).toContain("New section");
});
