import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";
import { resetThemeAudioFormats } from "@/lib/themeMusic";
import { useThemeMusic } from "./useThemeMusic";
import TrailersSection from "./components/TrailersSection";

const state = vi.hoisted(() => ({
  profile: { id: "p1" },
  proof: "pin-1",
  enabled: true,
  requests: [] as string[],
  bodies: [] as unknown[],
  grant: undefined as Promise<{ url: string }> | undefined,
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { id: 1 }, profile: state.profile }),
}));
vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({ emblaRef: vi.fn(), canScrollPrev: false, canScrollNext: false }),
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
  v2: async (
    operation: string,
    options: { profileContext?: { profileToken: string }; body?: unknown },
  ) => {
    if (operation.startsWith("GET")) return { state: "available", allowed: true };
    state.requests.push(options.profileContext?.profileToken ?? "");
    state.bodies.push(options.body);
    return state.grant ?? { url: "/audio?token=test" };
  },
}));

let elements: HTMLAudioElement[];
beforeEach(() => {
  state.profile = { id: "p1" };
  state.proof = "pin-1";
  state.enabled = true;
  state.requests = [];
  state.bodies = [];
  state.grant = undefined;
  resetThemeAudioFormats();
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

function openTrailer() {
  render(
    <TrailersSection
      videos={[
        {
          kind: "trailer",
          site: "YouTube",
          site_key: "review-fixture",
          name: "Review trailer",
          is_official: true,
        },
      ]}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /Review trailer/ }));
  expect(screen.getByTitle("Review trailer")).toHaveAttribute(
    "src",
    "https://www.youtube-nocookie.com/embed/review-fixture?autoplay=1",
  );
}

it("stops theme music when an iframe trailer opens and keeps the owner suppressed", async () => {
  const { rerender, unmount } = renderHook(({ detail }) => useThemeMusic(detail, false), {
    wrapper,
    initialProps: { detail: item("movie") },
  });
  await waitFor(() => expect(elements).toHaveLength(1));
  openTrailer();
  expect(elements[0]!.paused).toBe(true);
  expect(elements[0]!.src).toBe("");
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  rerender({ detail: item("movie") });
  expect(elements).toHaveLength(1);
  expect(state.requests).toHaveLength(1);
  unmount();
});

it("discards an in-flight theme grant when an iframe trailer opens", async () => {
  let finish!: (grant: { url: string }) => void;
  state.grant = new Promise((resolve) => {
    finish = resolve;
  });
  const { unmount } = renderHook(() => useThemeMusic(item("movie"), false), { wrapper });
  await waitFor(() => expect(state.requests).toHaveLength(1));
  openTrailer();
  await act(async () => {
    finish({ url: "/audio?token=obsolete" });
  });
  expect(elements).toHaveLength(0);
  unmount();
});

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

it("tells the server which theme formats this browser decodes", async () => {
  const canPlay = vi
    .spyOn(HTMLMediaElement.prototype, "canPlayType")
    .mockImplementation((mime: string) => (mime === "audio/mpeg" ? "probably" : ""));
  const { unmount } = renderHook(() => useThemeMusic(item("movie"), false), { wrapper });
  await waitFor(() => expect(elements).toHaveLength(1));
  expect(state.bodies[0]).toEqual({ accepted_formats: [{ container: "mp3", audio_codec: "mp3" }] });
  unmount();
  canPlay.mockRestore();
});

it("omits the format list when the browser cannot describe itself", async () => {
  const canPlay = vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
  const { unmount } = renderHook(() => useThemeMusic(item("movie"), false), { wrapper });
  await waitFor(() => expect(elements).toHaveLength(1));
  expect(state.bodies[0]).toBeUndefined();
  unmount();
  canPlay.mockRestore();
});
