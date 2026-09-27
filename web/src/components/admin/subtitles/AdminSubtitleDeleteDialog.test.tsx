import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "@/api/v2/adminSubtitles";
import AdminSubtitleDeleteDialog from "./AdminSubtitleDeleteDialog";
const row = {
  id: "9",
  media_file_id: "42",
  language: "en",
  release_name: "Original",
} as AdminStoredSubtitle;
const canonical = () =>
  new Response(JSON.stringify(row), {
    headers: { "Content-Type": "application/json", ETag: '"captured"' },
  });
beforeEach(() => {
  setAccessToken("admin");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("loads canonical state before enabling confirmation and retains an uncertain failure", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn()
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({ status: 500, title: "Uncertain deletion", code: "internal_error" }),
        { status: 500, headers: { "Content-Type": "application/problem+json" } },
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const close = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AdminSubtitleDeleteDialog
        target={{ subtitle: row, scope: adminSubtitleListScope() }}
        onClose={close}
      />
    </QueryClientProvider>,
  );
  expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
  finish(canonical());
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await screen.findByRole("alert");
  expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
  expect(close).not.toHaveBeenCalled();
  expect(fetch).toHaveBeenCalledTimes(2);
});
it("a prior confirmation cannot close a replacement target after late success", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(canonical())
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ ...row, id: "10" }), {
        headers: { "Content-Type": "application/json", ETag: '"next"' },
      }),
    );
  vi.stubGlobal("fetch", fetch);
  const close = vi.fn();
  const client = new QueryClient();
  const scope = adminSubtitleListScope();
  const view = (id: string) => (
    <QueryClientProvider client={client}>
      <AdminSubtitleDeleteDialog target={{ subtitle: { ...row, id }, scope }} onClose={close} />
    </QueryClientProvider>
  );
  const mounted = render(view("9"));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  mounted.rerender(view("10"));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
  finish(new Response(null, { status: 204 }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled());
  expect(close).not.toHaveBeenCalled();
});
