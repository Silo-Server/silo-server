import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, cleanup } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import AdminSubtitles from "./AdminSubtitles";

const mocks = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/hooks/queries/admin/subtitles", () => ({
  useAdminDownloadedSubtitles: mocks.list,
  useAdminDeleteDownloadedSubtitle: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({ useAdminUsers: () => ({ data: [] }) }));
vi.mock("@/components/admin/subtitles/AdminSubtitlesTable", () => ({
  default: () => <div>Stored rows</div>,
}));
vi.mock("@/components/admin/subtitles/AdminSubtitlesFilters", () => ({
  FILTER_ALL: "all",
  default: ({ onLanguageChange }: { onLanguageChange: (v: string) => void }) => (
    <button onClick={() => onLanguageChange("fr")}>French</button>
  ),
}));
beforeEach(() => {
  cleanup();
  mocks.list.mockReset();
  setAccessToken("admin");
  setProfileId("owner");
  setProfileToken(null);
  mocks.list.mockImplementation((query) => ({
    data: {
      items: [],
      total: 3,
      uploads: 1,
      provider_downloads: 2,
      page: query.cursor ? { has_more: false } : { has_more: true, next_cursor: "c1" },
    },
  }));
});
it("uses server cursors for Next and previous history for Previous", () => {
  render(
    <MemoryRouter>
      <AdminSubtitles />
    </MemoryRouter>,
  );
  expect(mocks.list.mock.lastCall?.[0].cursor).toBeUndefined();
  fireEvent.click(screen.getByRole("button", { name: "Next" }));
  expect(mocks.list.mock.lastCall?.[0].cursor).toBe("c1");
  expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Previous" }));
  expect(mocks.list.mock.lastCall?.[0].cursor).toBeUndefined();
  expect(mocks.list.mock.calls.every(([query]) => !("offset" in query))).toBe(true);
});
it("resets the cursor immediately when filters or PIN authority change", () => {
  const view = render(
    <MemoryRouter>
      <AdminSubtitles />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Next" }));
  fireEvent.click(screen.getByRole("button", { name: "French" }));
  expect(mocks.list.mock.lastCall?.[0]).toMatchObject({ language: "fr", cursor: undefined });
  fireEvent.click(screen.getByRole("button", { name: "Next" }));
  setProfileToken("new-pin");
  view.rerender(
    <MemoryRouter>
      <AdminSubtitles />
    </MemoryRouter>,
  );
  expect(mocks.list.mock.lastCall?.[0].cursor).toBeUndefined();
});
