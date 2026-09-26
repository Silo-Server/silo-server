import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "@/api/v2/adminSubtitles";
import AdminSubtitlesTable from "./AdminSubtitlesTable";
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
it("actual download gesture saves via v2 and keeps the opaque subtitle ID", async () => {
  setAccessToken("admin");
  setProfileId("owner");
  setProfileToken(null);
  const row = {
    id: "9007199254740993",
    media_file_id: "42",
    language: "en",
    format: "srt",
    provider: "upload",
    release_name: "Fixture",
    media_title: "Fixture",
    created_at: "2026-09-06T00:00:00Z",
  } as AdminStoredSubtitle;
  const fetch = vi
    .fn()
    .mockResolvedValue(
      new Response("subtitle", { headers: { "Content-Type": "application/x-subrip" } }),
    );
  vi.stubGlobal("fetch", fetch);
  URL.createObjectURL = vi.fn(() => "blob:subtitle");
  URL.revokeObjectURL = vi.fn();
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  render(
    <MemoryRouter>
      <QueryClientProvider client={new QueryClient()}>
        <AdminSubtitlesTable
          subtitles={[row]}
          authorityScope={adminSubtitleListScope()}
          hasActiveFilters={false}
          onResetFilters={() => {}}
        />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole("button", { name: `Download subtitle ${row.id}` }));
  await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]![0]).toBe(`/api/v2/admin/subtitles/${row.id}/download`);
});
