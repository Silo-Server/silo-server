import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import { createOnboardingWriter } from "./onboarding";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("profile");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
function state(etag: string) {
  return new Response(JSON.stringify({ tour_id: "tour", done: false }), {
    headers: { "Content-Type": "application/json", ETag: etag },
  });
}
it("advances only acknowledged validators and blocks overlapping intent", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(state('"one"'))
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValueOnce(state('"three"'));
  vi.stubGlobal("fetch", fetch);
  const write = createOnboardingWriter(captureProfileRequestContext()!);
  const first = write({ tour_id: "tour", last_step: "first" });
  await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  await expect(write({ tour_id: "tour", last_step: "second" })).rejects.toThrow(
    "still being saved",
  );
  finish(state('"two"'));
  await first;
  await write({ tour_id: "tour", last_step: "second" });
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("If-Match")).toBe('"one"');
  expect(new Headers(fetch.mock.calls[2]![1]?.headers).get("If-Match")).toBe('"two"');
  expect(fetch.mock.calls[2]![1]?.method).toBe("PUT");
});
for (const status of [401, 412])
  it(`stops after ${status} without refresh replay or silent rebase`, async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(state('"one"'))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ status, title: "Rejected" }), {
          status,
          headers: { "Content-Type": "application/problem+json" },
        }),
      );
    vi.stubGlobal("fetch", fetch);
    const write = createOnboardingWriter(captureProfileRequestContext()!);
    await expect(write({ tour_id: "tour", last_step: "first" })).rejects.toMatchObject({ status });
    await expect(write({ tour_id: "tour", last_step: "second" })).rejects.toThrow("Reload");
    expect(fetch).toHaveBeenCalledTimes(2);
  });
it("stops on an uncertain network result", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(state('"one"'))
    .mockRejectedValueOnce(new Error("connection lost"));
  vi.stubGlobal("fetch", fetch);
  const write = createOnboardingWriter(captureProfileRequestContext()!);
  await expect(write({ tour_id: "tour", completed: true })).rejects.toThrow("connection lost");
  await expect(write({ tour_id: "tour", completed: true })).rejects.toThrow("Reload");
  expect(fetch).toHaveBeenCalledTimes(2);
});
it("rejects completion after a profile change", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(state('"one"'))
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
  vi.stubGlobal("fetch", fetch);
  const write = createOnboardingWriter(captureProfileRequestContext()!);
  const pending = write({ tour_id: "tour", completed: true });
  await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  setProfileId("other");
  finish(state('"two"'));
  await expect(pending).rejects.toThrow();
});
