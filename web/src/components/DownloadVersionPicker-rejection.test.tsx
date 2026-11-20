import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { FileVersion } from "@/api/types";
import DownloadVersionPicker from "./DownloadVersionPicker";

const mocks = vi.hoisted(() => ({ error: vi.fn() }));
vi.mock("sonner", () => ({ toast: { error: mocks.error } }));
vi.mock("@/hooks/queries/downloads", () => ({
  useDownloadCapability: () => ({
    data: { enabled: true, allowed: true, download_allowed: true },
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  }),
}));
// Use the real directDownloads helper and real client authority state.
const LINK_URL = "/api/v2/direct-download?file_id=42&dl=link-token";
/** The profile-bound link the server mints for the preflight. */
function mintResponse(): Response {
  return new Response(
    JSON.stringify({
      url: LINK_URL,
      proxy_url: `${LINK_URL}-proxy`,
      expires_at: "2026-01-01T00:30:00Z",
    }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}
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
  "fences a rejected actual preflight for %s authority without replacing the picker",
  async (authority) => {
    let reject!: (error: Error) => void;
    const fetch = vi.fn((input: RequestInfo | URL) =>
      String(input).includes("/direct-download/links")
        ? Promise.resolve(mintResponse())
        : new Promise<Response>((_, fail) => {
            reject = fail;
          }),
    );
    vi.stubGlobal("fetch", fetch);
    const close = vi.fn();
    render(<DownloadVersionPicker open onOpenChange={close} versions={versions} />);
    fireEvent.click(screen.getByRole("button", { name: /1080p/ }));
    await vi.waitFor(() => expect(reject).toBeTypeOf("function"));
    // The link is minted first; only the link's own URL is probed.
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls[0]?.[0]).toBe("/api/v2/direct-download/links");
    expect(fetch).toHaveBeenCalledWith(
      LINK_URL,
      expect.objectContaining({ method: "GET", cache: "no-store" }),
    );
    // No close/rerender/versions replacement: only the client authority changes.
    if (authority === "account") setAccessToken("replacement-account");
    if (authority === "profile") setProfileId("replacement-profile");
    if (authority === "pin") setProfileToken("replacement-pin");
    await act(async () => {
      reject(new TypeError("connection lost"));
    });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("link", { name: /Save file/i })).toBeNull();
    expect(close).not.toHaveBeenCalled();
    expect(mocks.error).toHaveBeenCalledTimes(authority === "unchanged" ? 1 : 0);
  },
);

it("fences a late preflight after an authority change with no save link", async () => {
  let resolveProbe!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) =>
      String(input).includes("/direct-download/links")
        ? Promise.resolve(mintResponse())
        : new Promise<Response>((r) => {
            resolveProbe = r;
          }),
    ),
  );
  const close = vi.fn();
  render(<DownloadVersionPicker open onOpenChange={close} versions={versions} />);
  fireEvent.click(screen.getByRole("button", { name: /1080p/ }));
  await vi.waitFor(() => expect(resolveProbe).toBeDefined());
  // The probe is still in flight; the profile changes before it lands.
  setProfileId("replacement-profile");
  await act(async () => {
    resolveProbe(new Response(null, { status: 206 }));
  });
  expect(screen.queryByRole("link", { name: /Save file/i })).toBeNull();
  expect(close).not.toHaveBeenCalled();
  expect(mocks.error).not.toHaveBeenCalled();
});
