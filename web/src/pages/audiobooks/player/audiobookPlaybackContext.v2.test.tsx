import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { storage } from "@/utils/storage";
import { fixturePlanV3 } from "@/player/protocol-v3.fixtures";
import {
  AudiobookPlaybackProvider,
  useAudiobookPlaybackController,
} from "./audiobookPlaybackContext";

const identity = vi.hoisted(() => ({ account: 1, toast: vi.fn() }));
vi.mock("sonner", () => ({ toast: { error: identity.toast, dismiss: vi.fn() } }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: identity.account } }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "profile" } }),
}));
vi.mock("@/api/client", async (original) => ({
  ...(await original<object>()),
  getAccessToken: () => "synthetic",
  getProfileToken: () => null,
  getOrCreateDeviceId: () => "device",
  captureProfileRequestContext: () => ({
    profileId: "profile",
    serverOrigin: "http://localhost:3000",
    account: identity.account,
  }),
  isCapturedProfileAuthorityActive: (captured: { account: number }) =>
    captured.account === identity.account,
}));
vi.mock("@/player/hooks/usePlaybackRealtime", () => ({
  usePlaybackRealtime: () => ({ connectionState: "disconnected" }),
}));
// Keep the actual provider, lazy player, hook and durable HTTP helpers. Only
// presentation and media decoding are replaced by observable media events.
vi.mock("./MiniBar", () => ({
  MiniBar: ({ playback }: { playback: { currentTime: number; playing: boolean } }) => (
    <output data-testid="clock" data-playing={String(playback.playing)}>
      {playback.currentTime}
    </output>
  ),
}));
vi.mock("./NowListening", () => ({ NowListening: () => null }));
const files = [
  { id: 1, duration_seconds: 60 },
  { id: 2, duration_seconds: 36 },
];
function Controls() {
  const controller = useAudiobookPlaybackController()!;
  const input = { contentId: "book", title: "Book", files };
  return (
    <>
      <button onClick={() => controller.startPlayback(input)}>Play book</button>
      <button
        onClick={() =>
          controller.startPlayback({
            ...input,
            initialChapter: { fileId: "2", positionSeconds: 9 },
          })
        }
      >
        Detail Part2 chapter
      </button>
      <button
        onClick={() =>
          controller.startPlayback({
            ...input,
            initialChapter: { fileId: "2", positionSeconds: 19 },
          })
        }
      >
        Different chapter
      </button>
      <button onClick={controller.stopPlayback}>Close book</button>
    </>
  );
}
function App() {
  return (
    <QueryClientProvider client={new QueryClient()}>
      <AudiobookPlaybackProvider>
        <Controls />
      </AudiobookPlaybackProvider>
    </QueryClientProvider>
  );
}
const digest = "a".repeat(64);
const manifest = {
  installation_id: "installation",
  timeline_id: digest,
  media_item_id: "book",
  edition_id: "edition",
  duration_seconds: 84,
  parts: [
    { file_id: "1", offset_seconds: 0, duration_seconds: 48 },
    { file_id: "2", offset_seconds: 48, duration_seconds: 36 },
  ],
};
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
let unique = 0;
function server() {
  let release!: (status?: number) => void;
  let held = false;
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, options?: RequestInit): Promise<Response> => {
      const url = String(input);
      if (url.endsWith("/capabilities"))
        return json({
          installation_id: "installation",
          state: "available",
          allowed: true,
          protocol_versions: [3],
          features: ["sequenced_progress_v1", "bound_client_timeline"],
        });
      if (url.includes("/timelines/")) return json(manifest);
      const body = JSON.parse(String(options?.body ?? "{}"));
      if (url.endsWith("/start")) {
        const file = body.file_id;
        const session = `remount-${unique}-${file}`;
        const plan = fixturePlanV3({ session_id: session });
        return json(
          {
            protocol_version: 3,
            outcome: "playable",
            session_id: session,
            server_features: ["sequenced_progress_v1", "bound_client_timeline"],
            progress_timeline: {
              timeline_id: digest,
              media_item_id: "book",
              file_id: file,
              part_offset_seconds: file === "2" ? 48 : 0,
              part_duration_seconds: file === "2" ? 36 : 48,
              duration_seconds: 84,
            },
            playback_plan: {
              ...plan,
              requested_media_file_id: file,
              effective_media_file_id: file,
              source: { ...plan.source, media_file_id: file },
              timeline: {
                ...plan.timeline,
                source_start_seconds: body.start_position,
                player_start_seconds: body.start_position,
                timeline_offset_seconds: 0,
              },
              stream: { ...plan.stream, url: `/api/v2/media/files/${file}/original` },
            },
          },
          201,
        );
      }
      const second = url.endsWith("-2") || url.includes("-2/");
      const accepted = {
        sequence: body.sequence,
        position: body.position,
        is_paused: body.is_paused,
        timeline_id: digest,
        item_position: body.position + (second ? 48 : 0),
      };
      if (options?.method === "DELETE") {
        const receipt = { outcome: "stopped", stop_id: body.stop_id, accepted };
        if (!held) {
          held = true;
          return new Promise((resolve) => {
            release = (status = 200) =>
              resolve(json(status === 200 ? receipt : { detail: "stop unknown" }, status));
          });
        }
        return json(receipt);
      }
      if (url.endsWith("/progress")) return json({ outcome: "applied", accepted });
      if (url.endsWith("/route-events")) return new Response(null, { status: 202 });
      throw new Error(`Unexpected request ${url}`);
    },
  );
  vi.stubGlobal("fetch", fetcher);
  return {
    fetcher,
    release: (status?: number) => release(status),
    starts: () => fetcher.mock.calls.filter(([url]) => String(url).endsWith("/start")),
    stops: () => fetcher.mock.calls.filter(([, options]) => options?.method === "DELETE"),
  };
}
beforeEach(() => {
  unique++;
  identity.account = 1;
  identity.toast.mockClear();
  localStorage.clear();
  storage.set(storage.KEYS.PROFILE_ID, "profile");
  const tails = new Map<string, Promise<unknown>>();
  Object.defineProperty(navigator, "locks", {
    configurable: true,
    value: {
      request: (key: string, _options: unknown, action: () => Promise<unknown>) => {
        const next = (tails.get(key) ?? Promise.resolve()).catch(() => {}).then(action);
        tails.set(key, next);
        return next;
      },
    },
  });
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(function (
    this: HTMLMediaElement,
  ) {
    Object.defineProperty(this, "paused", { configurable: true, value: false });
    this.dispatchEvent(new Event("play"));
    return Promise.resolve();
  });
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(function (
    this: HTMLMediaElement,
  ) {
    if (this.paused) return;
    Object.defineProperty(this, "paused", { configurable: true, value: true });
    this.dispatchEvent(new Event("pause"));
  });
});
afterEach(async () => {
  await act(async () => cleanup());
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function playingBook() {
  const app = render(<App />);
  fireEvent.click(screen.getByText("Play book"));
  await waitFor(() =>
    expect(app.container.querySelector("audio")?.src).toContain("/files/1/original"),
  );
  const audio = app.container.querySelector("audio")!;
  fireEvent.loadedMetadata(audio);
  await waitFor(() => expect(audio.paused).toBe(false));
  audio.currentTime = 1;
  fireEvent.timeUpdate(audio);
  return app;
}
it("continues the original detail chapter through provider replacement only after the old terminal stop", async () => {
  const api = server();
  const app = await playingBook();
  fireEvent.click(screen.getByText("Detail Part2 chapter"));
  await waitFor(() => expect(api.stops()).toHaveLength(1));
  expect(api.starts()).toHaveLength(1);
  const original = api.stops()[0]![1]?.body;
  await act(async () => api.release());
  await waitFor(() => expect(api.starts()).toHaveLength(2));
  expect(JSON.parse(String(api.starts()[1]![1]?.body))).toMatchObject({
    file_id: "2",
    start_position: 9,
    timeline_id: digest,
  });
  expect(api.stops()).toHaveLength(1);
  expect(api.stops()[0]![1]?.body).toBe(original);
  const audio = app.container.querySelector("audio")!;
  await waitFor(() => expect(audio.src).toContain("/files/2/original"));
  fireEvent.loadedMetadata(audio);
  await waitFor(() => expect(audio.paused).toBe(false));
  expect(audio.currentTime).toBe(9);
  expect(screen.getByTestId("clock").textContent).toBe("57");
});

it("retains the original chapter and exact stop body through an unknown stop and explicit Retry", async () => {
  const api = server();
  await playingBook();
  fireEvent.click(screen.getByText("Detail Part2 chapter"));
  await waitFor(() => expect(api.stops()).toHaveLength(1));
  const original = api.stops()[0]![1]?.body;
  await act(async () => api.release(409));
  await waitFor(() =>
    expect(identity.toast).toHaveBeenCalledWith("Audiobook change pending", expect.anything()),
  );
  expect(api.starts()).toHaveLength(1);
  fireEvent.click(screen.getByText("Different chapter"));
  const options = identity.toast.mock.calls.find(
    ([message]) => message === "Audiobook change pending",
  )![1];
  await act(async () => options.action.onClick());
  await waitFor(() => expect(api.starts()).toHaveLength(2));
  expect(api.stops()).toHaveLength(2);
  expect(api.stops()[1]![1]?.body).toBe(original);
  expect(JSON.parse(String(api.starts()[1]![1]?.body))).toMatchObject({
    file_id: "2",
    start_position: 9,
    timeline_id: digest,
  });
});

it.each(["account switch", "close"])(
  "does not continue a held chapter after %s",
  async (cancel) => {
    const api = server();
    const app = await playingBook();
    fireEvent.click(screen.getByText("Detail Part2 chapter"));
    await waitFor(() => expect(api.stops()).toHaveLength(1));
    const original = api.stops()[0]![1]?.body;
    if (cancel === "account switch") {
      identity.account = 2;
      app.rerender(<App />);
    } else {
      fireEvent.click(screen.getByText("Close book"));
    }
    await act(async () => api.release());
    expect(api.starts()).toHaveLength(1);
    expect(api.stops()).toHaveLength(1);
    expect(api.stops()[0]![1]?.body).toBe(original);
    expect(app.container.querySelector("audio")).toBeNull();
  },
);

it("cannot revive a canceled chapter using a stale Retry callback", async () => {
  const api = server();
  await playingBook();
  fireEvent.click(screen.getByText("Detail Part2 chapter"));
  await waitFor(() => expect(api.stops()).toHaveLength(1));
  await act(async () => api.release(409));
  await waitFor(() =>
    expect(identity.toast).toHaveBeenCalledWith("Audiobook change pending", expect.anything()),
  );
  const options = identity.toast.mock.calls.find(
    ([message]) => message === "Audiobook change pending",
  )![1];
  fireEvent.click(screen.getByText("Close book"));
  await act(async () => options.action.onClick());
  expect(api.starts()).toHaveLength(1);
  expect(screen.queryByTestId("clock")).toBeNull();
});

it("cancels a captured chapter replacement after owner loss and accepts a new explicit click", async () => {
  const api = server();
  const normal = api.fetcher.getMockImplementation()!;
  api.fetcher.mockImplementation(async (input, options) => {
    if (options?.method !== "DELETE") return normal(input, options);
    const original = JSON.parse(String(api.starts()[0]![1]?.body));
    return json({
      outcome: "aborted",
      recovery: {
        recovery_id: "recovery",
        playback_attempt_id: original.playback_attempt_id,
        session_id: `remount-${unique}-1`,
        state: "aborted",
        reason: "owner_lost",
      },
    });
  });
  const app = await playingBook();
  fireEvent.click(screen.getByText("Detail Part2 chapter"));
  await waitFor(() => expect(app.container.querySelector("audio")).toBeNull());
  expect(api.starts()).toHaveLength(1);
  expect(identity.toast).toHaveBeenCalledWith("Playback ended", expect.any(Object));
  expect(identity.toast.mock.calls.some(([, options]) => options?.action)).toBe(false);
  unique++;
  fireEvent.click(screen.getByText("Different chapter"));
  await waitFor(() => expect(api.starts()).toHaveLength(2));
  expect(JSON.parse(String(api.starts()[1]![1]?.body))).toMatchObject({
    file_id: "2",
    start_position: 19,
  });
  expect(JSON.parse(String(api.starts()[1]![1]?.body)).playback_attempt_id).not.toBe(
    JSON.parse(String(api.starts()[0]![1]?.body)).playback_attempt_id,
  );
  api.fetcher.mockImplementation(normal);
});
