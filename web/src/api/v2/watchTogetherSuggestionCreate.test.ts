import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureSuggestionDraft, createRoomSuggestion } from "./watchTogetherSuggestionCreate";
import { useWatchTogetherRoomConnection } from "@/player/hooks/useWatchTogetherRoomConnection";
const input = { content_id: "movie", content_type: "movie" as const, title: "Title" };
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("host");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("retains copied draft identity/body/proof across uncertain failure and explicit retry", async () => {
  const original = { ...input };
  const draft = captureSuggestionDraft("room", "original-proof", original);
  original.title = "edited";
  const fetch = vi
    .fn()
    .mockRejectedValueOnce(new Error("lost response"))
    .mockResolvedValueOnce(json({ suggestion_id: draft.body.suggestion_id }, 201))
    .mockResolvedValueOnce(json({ items: [], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetch);
  await expect(createRoomSuggestion(draft)).rejects.toThrow("lost response");
  await createRoomSuggestion(draft);
  const calls = fetch.mock.calls;
  expect(calls.map((c) => c[1].method)).toEqual(["POST", "POST", "GET"]);
  expect(calls[0]![1].body).toBe(calls[1]![1].body);
  expect(JSON.parse(calls[1]![1].body)).toEqual({
    ...input,
    suggestion_id: draft.body.suggestion_id,
  });
  for (const c of calls) {
    expect(new Headers(c[1].headers).get("X-Room-Token")).toBe("original-proof");
    expect(c[0]).not.toContain("original-proof");
  }
  expect(captureSuggestionDraft("room", "original-proof", input).body.suggestion_id).not.toBe(
    draft.body.suggestion_id,
  );
});
it.each([401, 403, 409, 422, 500])(
  "does not replay %s or refresh after refusal",
  async (status) => {
    const draft = captureSuggestionDraft("room", "proof", input);
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await expect(createRoomSuggestion(draft)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
  },
);
it("old draft authority refuses dispatch and late success refuses list refresh", async () => {
  const draft = captureSuggestionDraft("room", "proof", input);
  setProfileToken("new");
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(createRoomSuggestion(draft)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
  const current = captureSuggestionDraft("room", "proof", input);
  fetch.mockImplementation(async () => {
    setProfileToken("next");
    return json({ suggestion_id: current.body.suggestion_id }, 201);
  });
  await expect(createRoomSuggestion(current)).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it.each([201, 500])(
  "mounted replacement room rejects prior draft %s completion",
  async (status) => {
    const draft = captureSuggestionDraft("room", "proof", input);
    let resolve!: (r: Response) => void;
    const fetch = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((r) => {
            resolve = r;
          }),
      )
      .mockResolvedValue(json({ items: [], page: { has_more: false } }));
    vi.stubGlobal("fetch", fetch);
    const { result, rerender } = renderHook(
      ({ id }) => useWatchTogetherRoomConnection({ roomId: id, roomToken: null }),
      { initialProps: { id: "room" } },
    );
    let done!: Promise<void>;
    act(() => {
      done = result.current.createSuggestion(draft);
    });
    rerender({ id: "other" });
    await act(async () => {
      resolve(
        status === 201
          ? json({ suggestion_id: draft.body.suggestion_id }, 201)
          : new Response(null, { status }),
      );
      await expect(done).rejects.toThrow();
    });
    expect(result.current.suggestions).toEqual([]);
  },
);
it("creates and retries a stable draft when secure-context randomUUID is absent", async () => {
  const getRandomValues = vi.fn((bytes: Uint8Array) => {
    for (let i = 0; i < bytes.length; i++) bytes[i] = i;
    return bytes;
  });
  vi.stubGlobal("crypto", { getRandomValues });
  const original = { ...input };
  const draft = captureSuggestionDraft("room", "proof", original);
  expect(draft.body.suggestion_id).toBe("00010203-0405-4607-8809-0a0b0c0d0e0f");
  expect(getRandomValues).toHaveBeenCalledTimes(1);
  original.title = "Changed after capture";
  const fetch = vi
    .fn()
    .mockRejectedValueOnce(new Error("uncertain"))
    .mockResolvedValueOnce(json({ suggestion_id: draft.body.suggestion_id }, 201))
    .mockResolvedValueOnce(json({ items: [], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetch);
  await expect(createRoomSuggestion(draft)).rejects.toThrow("uncertain");
  await createRoomSuggestion(draft);
  expect(fetch.mock.calls[0]![1].body).toBe(fetch.mock.calls[1]![1].body);
  expect(JSON.parse(fetch.mock.calls[1]![1].body)).toEqual({
    ...input,
    suggestion_id: draft.body.suggestion_id,
  });
});
