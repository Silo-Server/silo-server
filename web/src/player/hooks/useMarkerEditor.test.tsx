import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import type { MarkerDraft } from "../types";
import { useMarkerEditor } from "./useMarkerEditor";

const config: PlayerConfig = {
  apiBaseUrl: "https://player.example/api/v1",
  getAccessToken: () => "player-token",
  getProfileId: () => "profile",
  getProfileToken: () => "profile-token",
  getDeviceId: () => "device",
};
const wrapper = ({ children }: { children: ReactNode }) =>
  createElement(PlayerConfigProvider, { config, children });
afterEach(() => vi.unstubAllGlobals());

it("saves changed ranges through the host-owned v2 player transport", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response("{}", { status: 200 }));
  vi.stubGlobal("fetch", fetchMock);
  const onSaved = vi.fn();
  const { result } = renderHook(
    () =>
      useMarkerEditor({
        fileId: 42,
        duration: 600,
        markers: {
          intro: { start: 0, end: 60 },
          credits: { start: 500, end: 600 },
          recap: null,
          preview: null,
        },
        onSaved,
      }),
    { wrapper },
  );
  act(() => result.current.begin());
  act(() => {
    result.current.setEdge("intro", "end", 70);
    result.current.clearKind("credits");
  });
  await act(() => result.current.save());
  expect(fetchMock.mock.calls[0]?.[0]).toBe("https://player.example/api/v2/markers/files/42");
  const options = fetchMock.mock.calls[0]?.[1];
  expect(JSON.parse(String(options?.body))).toEqual({
    intro: { start_seconds: 0, end_seconds: 70 },
    credits: null,
  });
  expect(options?.headers).toMatchObject({
    Authorization: "Bearer player-token",
    "X-Profile-Id": "profile",
    "X-Profile-Token": "profile-token",
    "X-Silo-Device-Id": "device",
  });
  // Only the kinds this save sent: the draft's copy of the others can be older
  // than what another session has saved since the panel opened.
  expect(onSaved).toHaveBeenCalledExactlyOnceWith({ intro: { start: 0, end: 70 }, credits: null });
  expect(result.current.editing).toBe(false);
});

it("keeps the draft open when v2 refuses a save", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(
      async () =>
        new Response(
          JSON.stringify({
            type: "https://example.test/problems/forbidden",
            detail: "Marker editing is disabled",
          }),
          { status: 403 },
        ),
    ),
  );
  const onSaved = vi.fn();
  const { result } = renderHook(
    () =>
      useMarkerEditor({
        fileId: 42,
        duration: 600,
        markers: { intro: null, credits: null, recap: null, preview: null },
        onSaved,
      }),
    { wrapper },
  );
  act(() => result.current.begin());
  act(() => result.current.setEdge("intro", "start", 0));
  await act(() => result.current.save());
  expect(result.current.error).toBe("Marker editing is disabled");
  expect(result.current.editing).toBe(true);
  expect(result.current.saving).toBe(false);
  expect(onSaved).not.toHaveBeenCalled();
});

it("reopens with the host's markers after a save whose realtime echo beat its response", async () => {
  const initial: MarkerDraft = {
    intro: { start: 0, end: 60 },
    recap: null,
    credits: null,
    preview: null,
  };
  const afterSave: MarkerDraft = { ...initial, recap: { start: 0, end: 30 } };
  const fromAnotherSession: MarkerDraft = { ...initial, intro: { start: 10, end: 70 } };
  let respond: (response: Response) => void = () => {};
  const fetchMock = vi.fn<typeof fetch>(
    () => new Promise<Response>((resolve) => (respond = resolve)),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result, rerender } = renderHook(
    ({ markers }) => useMarkerEditor({ fileId: 42, duration: 600, markers }),
    { wrapper, initialProps: { markers: initial } },
  );
  act(() => result.current.begin());
  act(() => result.current.setEdge("recap", "end", 30));
  let saving = Promise.resolve();
  act(() => {
    saving = result.current.save();
  });
  await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
  // The server pushes markers_updated before it answers the PUT, so the host
  // already holds the saved values when the save resolves.
  rerender({ markers: afterSave });
  await act(async () => {
    respond(new Response("{}", { status: 200 }));
    await saving;
  });
  expect(result.current.editing).toBe(false);

  rerender({ markers: fromAnotherSession });
  act(() => result.current.begin());
  expect(result.current.draft).toEqual(fromAnotherSession);
});

it("closes the draft when the player switches to another file version", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response("{}", { status: 200 }));
  vi.stubGlobal("fetch", fetchMock);
  const markers: MarkerDraft = {
    intro: null,
    recap: null,
    credits: null,
    preview: { start: 1440, end: 1461 },
  };
  const { result, rerender } = renderHook(
    ({ fileId }) => useMarkerEditor({ fileId, duration: 1500, markers }),
    { wrapper, initialProps: { fileId: 142 } },
  );
  act(() => result.current.begin());
  act(() => result.current.setEdge("preview", "start", 0));

  // A version switch keeps the player mounted and hands the hook the new file.
  rerender({ fileId: 73 });
  expect(result.current.editing).toBe(false);

  await act(() => result.current.save());
  expect(fetchMock).not.toHaveBeenCalled();

  rerender({ fileId: 142 });
  expect(result.current.editing).toBe(false);
});

it("keeps the new version's draft when a save from the old version finishes late", async () => {
  let finishSave!: (response: Response) => void;
  const fetchMock = vi.fn<typeof fetch>(
    () => new Promise<Response>((resolve) => (finishSave = resolve)),
  );
  vi.stubGlobal("fetch", fetchMock);
  const onSaved = vi.fn();
  const markers: MarkerDraft = {
    intro: null,
    recap: null,
    credits: null,
    preview: { start: 1440, end: 1461 },
  };
  const { result, rerender } = renderHook(
    ({ fileId }) => useMarkerEditor({ fileId, duration: 1500, markers, onSaved }),
    { wrapper, initialProps: { fileId: 142 } },
  );
  act(() => result.current.begin());
  act(() => result.current.setEdge("preview", "start", 0));
  let saving!: Promise<void>;
  act(() => {
    saving = result.current.save();
  });

  rerender({ fileId: 73 });
  act(() => result.current.begin());
  act(() => result.current.setEdge("intro", "start", 10));
  const newDraft = result.current.draft;

  await act(async () => {
    finishSave(new Response("{}", { status: 200 }));
    await saving;
  });
  expect(result.current.editing).toBe(true);
  expect(result.current.draft).toEqual(newDraft);
  expect(result.current.dirty).toBe(true);
  expect(onSaved).toHaveBeenCalledWith({ preview: { start: 0, end: 1461 } });
});

// The panel closed and opened again on the same file while a save was in
// flight. When the save finishes, the new opening's edits stay (#4108).
it("keeps a reopened panel's edits when an earlier save finishes late", async () => {
  let finishSave!: (response: Response) => void;
  const fetchMock = vi.fn<typeof fetch>(
    () => new Promise<Response>((resolve) => (finishSave = resolve)),
  );
  vi.stubGlobal("fetch", fetchMock);
  const onSaved = vi.fn();
  const markers: MarkerDraft = {
    intro: null,
    recap: null,
    credits: null,
    preview: { start: 1440, end: 1461 },
  };
  const { result } = renderHook(
    () => useMarkerEditor({ fileId: 142, duration: 1500, markers, onSaved }),
    { wrapper },
  );
  act(() => result.current.begin());
  act(() => result.current.setEdge("preview", "start", 0));
  let saving!: Promise<void>;
  act(() => {
    saving = result.current.save();
  });

  act(() => result.current.cancel());
  act(() => result.current.begin());
  act(() => result.current.setEdge("intro", "start", 10));
  const newDraft = result.current.draft;

  await act(async () => {
    finishSave(new Response("{}", { status: 200 }));
    await saving;
  });
  expect(result.current.editing).toBe(true);
  // The new opening's edit stays; the kind the earlier save wrote, which this
  // opening didn't touch, shows the saved value and is its baseline.
  expect(result.current.draft).toEqual({ ...newDraft, preview: { start: 0, end: 1461 } });
  expect(result.current.isKindDirty("intro")).toBe(true);
  expect(result.current.isKindDirty("preview")).toBe(false);
  expect(onSaved).toHaveBeenCalledWith({ preview: { start: 0, end: 1461 } });

  // Its next save sends only what it changed against the saved state.
  fetchMock.mockImplementation(async () => new Response("{}", { status: 200 }));
  await act(() => result.current.save());
  expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body))).toEqual({
    intro: { start_seconds: 10, end_seconds: 70 },
  });
});

// A save that fails after the panel was closed and reopened still says so on
// the reopened panel, and leaves its draft alone.
it("shows an earlier save's failure on a reopened panel", async () => {
  let finishSave!: (response: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(() => new Promise<Response>((resolve) => (finishSave = resolve))),
  );
  const markers: MarkerDraft = { intro: null, recap: null, credits: null, preview: null };
  const { result } = renderHook(
    () => useMarkerEditor({ fileId: 142, duration: 1500, markers, onSaved: vi.fn() }),
    { wrapper },
  );
  act(() => result.current.begin());
  act(() => result.current.setEdge("recap", "start", 0));
  let saving!: Promise<void>;
  act(() => {
    saving = result.current.save();
  });

  act(() => result.current.cancel());
  act(() => result.current.begin());
  act(() => result.current.setEdge("intro", "start", 10));
  const newDraft = result.current.draft;

  await act(async () => {
    finishSave(
      new Response(JSON.stringify({ detail: "Marker editing is disabled" }), { status: 403 }),
    );
    await saving;
  });
  expect(result.current.error).toBe("Marker editing is disabled");
  expect(result.current.editing).toBe(true);
  expect(result.current.draft).toEqual(newDraft);
});
