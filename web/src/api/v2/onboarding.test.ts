import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import { createOnboardingWriter } from "./onboarding";
import { pendingResponse } from "./pendingFetch.test-support";
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
  const second = pendingResponse();
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(state('"one"'))
    .mockReturnValueOnce(second.promise)
    .mockResolvedValueOnce(state('"three"'));
  vi.stubGlobal("fetch", fetch);
  const write = createOnboardingWriter(captureProfileRequestContext()!);
  const first = write({ tour_id: "tour", last_step: "first" });
  await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  await expect(write({ tour_id: "tour", last_step: "second" })).rejects.toThrow(
    "still being saved",
  );
  second.finish(state('"two"'));
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
it("rejects completion after a profile change", async () => {
  const completion = pendingResponse();
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(state('"one"'))
    .mockReturnValueOnce(completion.promise);
  vi.stubGlobal("fetch", fetch);
  const write = createOnboardingWriter(captureProfileRequestContext()!);
  const pending = write({ tour_id: "tour", completed: true });
  await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  setProfileId("other");
  completion.finish(state('"two"'));
  await expect(pending).rejects.toThrow();
});
