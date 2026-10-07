// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SectionOverride, SettingsSectionEntry } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";

import { ProfileHomeSections } from "./ProfileHomeSections";

const mocks = vi.hoisted(() => ({
  sections: [] as SettingsSectionEntry[],
  overrides: [] as SectionOverride[],
  settingsError: false,
  saveMutate: vi.fn(),
  resetMutate: vi.fn(),
  calls: [] as { hook: string; userId: number; profileId: string }[],
}));

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

vi.mock("@/hooks/queries/admin/profileSections", () => ({
  useAdminProfileSectionSettings: (userId: number, profileId: string) => {
    mocks.calls.push({ hook: "settings", userId, profileId });
    return mocks.settingsError
      ? { data: undefined, isLoading: false, isError: true, isSuccess: false }
      : { data: mocks.sections, isLoading: false, isError: false, isSuccess: true };
  },
  useAdminProfileSectionOverrides: () => ({
    data: mocks.overrides,
    isLoading: false,
    isError: false,
    isSuccess: true,
  }),
  useSaveAdminProfileSections: () => ({ mutate: mocks.saveMutate, isPending: false }),
  useResetAdminProfileSections: () => ({ mutate: mocks.resetMutate, isPending: false }),
}));
// The recipe catalog only labels rows; leave it unloaded.
vi.mock("@/lib/recipes", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/recipes")>()),
  fetchRecipeCatalog: () => new Promise(() => {}),
}));

function section(overrides: Partial<SettingsSectionEntry>): SettingsSectionEntry {
  return {
    id: "s",
    section_type: "recently_added",
    title: "Section",
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position: 0,
    ...overrides,
  };
}

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <ProfileHomeSections userId={7} profileId="p2" profileName="Kids" />
    </QueryClientProvider>,
  );
  return screen
    .getByRole("heading", { name: "Home sections · Kids" })
    .closest("section") as HTMLElement;
}

beforeEach(() => {
  mocks.sections = [
    section({ id: "s-continue", section_type: "continue_watching", title: "Continue Watching" }),
    section({
      id: "s-recent",
      title: "Recently Added",
      hidden: true,
      customized: true,
      position: 1,
    }),
    section({
      id: "u-gems",
      section_type: "hidden_gems",
      title: "Hidden gems",
      is_custom: true,
      position: 2,
    }),
  ];
  mocks.overrides = [
    { id: "o-recent", section_id: "s-recent", position: 1, hidden: true },
    { id: "o-trending", section_id: "s-trending", removed: true },
  ];
  mocks.settingsError = false;
  mocks.calls = [];
  mocks.saveMutate.mockReset();
  mocks.resetMutate.mockReset();
  toastError.mockReset();
});

afterEach(() => {
  cleanup();
});

describe("ProfileHomeSections", () => {
  it("lists the profile's Home rows in order, without edit or delete", () => {
    const card = renderCard();
    expect(
      within(card)
        .getAllByRole("button", { name: /^Drag / })
        .map((b) => b.getAttribute("aria-label")),
    ).toEqual(["Drag Continue Watching", "Drag Recently Added", "Drag Hidden gems"]);
    expect(within(card).getByRole("button", { name: "Show Recently Added" })).toBeEnabled();
    expect(within(card).queryByRole("button", { name: /^Edit / })).not.toBeInTheDocument();
    expect(within(card).queryByRole("button", { name: /^Delete / })).not.toBeInTheDocument();
    expect(mocks.calls).toContainEqual({ hook: "settings", userId: 7, profileId: "p2" });
  });

  it("saves the whole layout when a row is hidden, keeping saved IDs and removed rows", async () => {
    const u = userEvent.setup();
    const card = renderCard();
    await u.click(within(card).getByRole("button", { name: "Hide Continue Watching" }));

    expect(mocks.saveMutate).toHaveBeenCalledTimes(1);
    const saved = mocks.saveMutate.mock.calls[0]?.[0] as SectionOverride[];
    expect(
      saved.map(({ section_id, position, hidden, removed }) => ({
        section_id,
        position,
        hidden,
        removed,
      })),
    ).toEqual([
      { section_id: "s-continue", position: 0, hidden: true, removed: undefined },
      { section_id: "s-recent", position: 1, hidden: true, removed: undefined },
      { section_id: undefined, position: 2, hidden: false, removed: undefined },
      { section_id: "s-trending", position: undefined, hidden: undefined, removed: true },
    ]);
    expect(saved.map((o) => o.id)).toEqual([
      expect.any(String),
      "o-recent",
      "u-gems",
      "o-trending",
    ]);
    expect(
      within(card).getByRole("button", { name: "Show Continue Watching" }),
    ).toBeInTheDocument();
  });

  it("resets the profile to the default layout after confirming", async () => {
    const u = userEvent.setup();
    const card = renderCard();
    await u.click(within(card).getByRole("button", { name: "Reset to default" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Other profiles keep theirs.");
    await u.click(within(dialog).getByRole("button", { name: "Reset" }));
    await waitFor(() => expect(mocks.resetMutate).toHaveBeenCalledTimes(1));
    expect(mocks.saveMutate).not.toHaveBeenCalled();
  });

  it("says so when the layout can't load, and offers no edits", () => {
    mocks.settingsError = true;
    const card = renderCard();
    expect(within(card).getByRole("alert")).toHaveTextContent(
      "Couldn't load Kids's Home sections.",
    );
    expect(within(card).getByRole("button", { name: "Reset to default" })).toBeDisabled();
  });

  it("names the server's reason when a save is refused", async () => {
    mocks.saveMutate.mockImplementation((_body, options: { onError: (e: unknown) => void }) =>
      options.onError(
        new V2ProblemError("replaceAdminUserProfileSectionOverrides", {
          type: "https://silo.dev/problems/validation_failed",
          title: "Validation failed",
          status: 400,
          detail: "The request did not pass validation; see errors.",
          instance: "urn:silo:request:1",
          errors: [
            {
              location: "body.overrides",
              code: "invalid",
              detail: "legacy Trakt section sources cannot be changed or reactivated",
            },
          ],
        }),
      ),
    );
    const u = userEvent.setup();
    const card = renderCard();
    await u.click(within(card).getByRole("button", { name: "Hide Continue Watching" }));

    expect(toastError).toHaveBeenCalledWith(
      "Couldn't save Home sections: legacy Trakt section sources cannot be changed or reactivated",
    );
  });
});
