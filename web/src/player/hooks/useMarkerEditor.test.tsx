import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
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
  expect(onSaved).toHaveBeenCalledOnce();
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
