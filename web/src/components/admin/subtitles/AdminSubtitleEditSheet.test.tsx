import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "@/api/v2/adminSubtitles";
import { captureAdminSubtitleEditIntent } from "@/api/v2/adminSubtitleMetadata";
import AdminSubtitleEditSheet from "./AdminSubtitleEditSheet";
const row = {
  id: "9",
  media_file_id: "42",
  language: "en",
  format: "srt",
  provider: "upload",
  release_name: "List value",
  hearing_impaired: false,
  media_title: "Fixture",
} as AdminStoredSubtitle;
const json = (value: unknown, status = 200, tag = '"original"') =>
  new Response(JSON.stringify(value), {
    status,
    headers: {
      "Content-Type": status === 200 ? "application/json" : "application/problem+json",
      ETag: tag,
    },
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
function mount() {
  const onClose = vi.fn();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <AdminSubtitleEditSheet
        intent={captureAdminSubtitleEditIntent(row, adminSubtitleListScope())}
        open
        onOpenChange={onClose}
      />
    </QueryClientProvider>,
  );
  return onClose;
}
it("loads canonical values before editing and sends only the changed field", async () => {
  let finish!: (response: Response) => void;
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValueOnce(json({ ...row, release_name: "Edited" }, 200, '"saved"'));
  vi.stubGlobal("fetch", fetch);
  const close = mount();
  expect(screen.queryByLabelText("Release name")).not.toBeInTheDocument();
  finish(json({ ...row, release_name: "Canonical value" }));
  const input = await screen.findByLabelText("Release name");
  expect(input).toHaveValue("Canonical value");
  fireEvent.change(input, { target: { value: "Edited" } });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(close).toHaveBeenCalledWith(false));
  expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({ release_name: "Edited" });
  expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("If-Match")).toBe('"original"');
});
it("retains the draft and blocks another save after a conflict without rereading or rebasing", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json({ ...row, release_name: "Canonical" }))
    .mockResolvedValueOnce(
      json({ status: 412, title: "Changed", code: "precondition_failed" }, 412, '"newer"'),
    );
  vi.stubGlobal("fetch", fetch);
  const close = mount();
  const input = await screen.findByLabelText("Release name");
  fireEvent.change(input, { target: { value: "My draft" } });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await screen.findByRole("alert");
  expect(input).toHaveValue("My draft");
  expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  expect(close).not.toHaveBeenCalled();
  expect(fetch).toHaveBeenCalledTimes(2);
});
it("does not close a replacement editor when an older save finishes", async () => {
  let finish!: (r: Response) => void;
  const next = { ...row, id: "10", release_name: "Next subtitle" };
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json(row))
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValueOnce(json(next));
  vi.stubGlobal("fetch", fetch);
  const onClose = vi.fn();
  const client = new QueryClient();
  const initialIntent = captureAdminSubtitleEditIntent(row, adminSubtitleListScope());
  const renderSheet = (intent: typeof initialIntent) => (
    <QueryClientProvider client={client}>
      <AdminSubtitleEditSheet intent={intent} open onOpenChange={onClose} />
    </QueryClientProvider>
  );
  const view = render(renderSheet(initialIntent));
  fireEvent.change(await screen.findByLabelText("Release name"), {
    target: { value: "Older draft" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  view.rerender(renderSheet(captureAdminSubtitleEditIntent(next, adminSubtitleListScope())));
  await waitFor(() => expect(screen.getByLabelText("Release name")).toHaveValue("Next subtitle"));
  finish(json({ ...row, release_name: "Older draft" }, 200, '"saved"'));
  await waitFor(() => expect(client.isMutating()).toBe(0));
  expect(onClose).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Release name")).toHaveValue("Next subtitle");
});
