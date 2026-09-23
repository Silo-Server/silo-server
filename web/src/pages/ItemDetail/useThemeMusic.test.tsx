import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";
import { useThemeMusic } from "./useThemeMusic";

const state = vi.hoisted(() => ({
  profile: { id: "p1" },
  proof: "pin-1",
  enabled: true,
  requests: [] as string[],
  grant: undefined as Promise<{ url: string }> | undefined,
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { id: 1 }, profile: state.profile }),
}));
vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({
    data: {
      "ui.theme_music_enabled": { value: state.enabled },
      "ui.theme_music_loop": { value: false },
    },
  }),
}));
vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({
    accessToken: "token",
    authContextVersion: 1,
    serverOrigin: "http://localhost",
    profileId: state.profile.id,
    profileToken: state.proof,
  }),
  isCapturedProfileAuthorityActive: (context: { profileId: string; profileToken: string }) =>
    context.profileId === state.profile.id && context.profileToken === state.proof,
}));
vi.mock("@/api/v2/request", () => ({
  v2: async (operation: string, options: { profileContext?: { profileToken: string } }) => {
    if (operation.startsWith("GET")) return { state: "available", allowed: true };
    state.requests.push(options.profileContext?.profileToken ?? "");
    return state.grant ?? { url: "/audio?token=test" };
  },
}));

let elements: HTMLAudioElement[];
beforeEach(() => {
  state.profile = { id: "p1" };
  state.proof = "pin-1";
  state.enabled = true;
  state.requests = [];
  state.grant = undefined;
  elements = [];
  vi.stubGlobal(
    "Audio",
    vi.fn(function () {
      const element = {
        src: "",
        volume: 0,
        loop: false,
        paused: true,
        preload: "",
        onerror: null,
        play: vi.fn(async () => {
          element.paused = false;
        }),
        pause: vi.fn(() => {
          element.paused = true;
        }),
        removeAttribute: vi.fn(() => {
          element.src = "";
        }),
        load: vi.fn(),
      };
      elements.push(element as unknown as HTMLAudioElement);
      return element;
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const item = (owner: string) =>
  ({
    themes: {
      owner_id: owner,
      items: [{ id: "1", title: "Theme", duration_seconds: 3, container: "mp3" }],
    },
  }) as ItemDetail;
function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

it("pauses during unresolved navigation and resumes the same owner's element", async () => {
  const { rerender, unmount } = renderHook(
    ({ detail, loading }) => useThemeMusic(detail, loading),
    { wrapper, initialProps: { detail: item("series") as ItemDetail | undefined, loading: false } },
  );
  await waitFor(() => expect(elements).toHaveLength(1));
  vi.useFakeTimers();
  act(() => vi.advanceTimersByTime(300));
  rerender({ detail: undefined, loading: true });
  act(() => vi.advanceTimersByTime(300));
  expect(elements[0]!.paused).toBe(true);
  rerender({ detail: item("series"), loading: false });
  await act(async () => {
    await Promise.resolve();
  });
  expect(elements).toHaveLength(1);
  expect(elements[0]!.paused).toBe(false);
  unmount();
  act(() => vi.advanceTimersByTime(300));
  expect(elements[0]!.src).toBe("");
});

it("uses renewed PIN proof and stops immediately on a profile switch", async () => {
  const { rerender, unmount } = renderHook(({ detail }) => useThemeMusic(detail, false), {
    wrapper,
    initialProps: { detail: item("movie-1") },
  });
  await waitFor(() => expect(elements).toHaveLength(1));
  state.proof = "pin-2";
  rerender({ detail: item("movie-2") });
  await waitFor(() => expect(state.requests).toEqual(["pin-1", "pin-2"]));
  state.profile = { id: "p2" };
  rerender({ detail: item("movie-2") });
  expect(elements[1]!.src).toBe("");
  unmount();
});

it("remains opt-in and suppresses theme music after other media starts", async () => {
  state.enabled = false;
  const { rerender, unmount } = renderHook(({ detail }) => useThemeMusic(detail, false), {
    wrapper,
    initialProps: { detail: item("movie") },
  });
  expect(elements).toHaveLength(0);
  state.enabled = true;
  rerender({ detail: item("movie") });
  await waitFor(() => expect(elements).toHaveLength(1));
  act(() => document.dispatchEvent(new Event("play")));
  expect(elements[0]!.src).toBe("");
  rerender({ detail: item("movie") });
  expect(elements).toHaveLength(1);
  rerender({ detail: item("other") });
  await waitFor(() => expect(elements).toHaveLength(2));
  rerender({ detail: item("movie") });
  await waitFor(() => expect(elements).toHaveLength(3));
  unmount();
});

it("discards a grant if the profile changes before React rerenders", async () => {
  let finish!: (grant: { url: string }) => void;
  state.grant = new Promise((resolve) => {
    finish = resolve;
  });
  const { unmount } = renderHook(() => useThemeMusic(item("movie"), false), { wrapper });
  await waitFor(() => expect(state.requests).toHaveLength(1));
  state.profile = { id: "p2" };
  await act(async () => {
    finish({ url: "/audio?token=old-profile" });
  });
  expect(elements).toHaveLength(0);
  unmount();
});
