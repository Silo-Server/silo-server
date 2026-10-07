import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { FileVersion } from "@/api/types";
import DownloadVersionPicker from "./DownloadVersionPicker";

const mocks = vi.hoisted(() => ({ error: vi.fn() }));
vi.mock("sonner", () => ({ toast: { error: mocks.error } }));
// Use the real directDownloads helper and real client authority state.
const versions = [{ file_id: 42, resolution: "1080p", file_size: 1024 } as FileVersion];
beforeEach(() => {
  mocks.error.mockClear();
  setAccessToken("original-account");
  setProfileId("original-profile");
  setProfileToken("original-pin");
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  setAccessToken(null);
  setProfileId(null);
  setProfileToken(null);
});

it.each(["account", "profile", "pin", "unchanged"])(
  "fences a rejected actual HEAD for %s authority without replacing the picker",
  async (authority) => {
    let reject!: (error: Error) => void;
    const link = "/api/v2/direct-download?file_id=42&dl=synthetic-link";
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ url: link, proxy_url: link, expires_at: "2026-01-01T00:05:00Z" }),
          { headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockImplementationOnce(
        () =>
          new Promise<Response>((_, fail) => {
            reject = fail;
          }),
      );
    vi.stubGlobal("fetch", fetch);
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const close = vi.fn();
    render(<DownloadVersionPicker open onOpenChange={close} versions={versions} />);
    fireEvent.click(screen.getByRole("button", { name: /1080p/ }));
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect(fetch).toHaveBeenLastCalledWith(link, { method: "HEAD", cache: "no-store" });
    // No close/rerender/versions replacement: only the client authority changes.
    if (authority === "account") setAccessToken("replacement-account");
    if (authority === "profile") setProfileId("replacement-profile");
    if (authority === "pin") setProfileToken("replacement-pin");
    await act(async () => {
      reject(new TypeError("connection lost"));
    });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(click).not.toHaveBeenCalled();
    expect(close).not.toHaveBeenCalled();
    expect(mocks.error).toHaveBeenCalledTimes(authority === "unchanged" ? 1 : 0);
  },
);
