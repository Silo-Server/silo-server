import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, onTestFinished, vi } from "vitest";
import { LearnedLimitRow, PlaybackConnection } from "./PlaybackConnection";

const request = vi.hoisted(() => vi.fn());
const speedTest = vi.hoisted(() => vi.fn());
const probe = vi.hoisted(() => ({
  network: undefined as { type?: string; saveData?: boolean } | undefined,
  RateLimited: class extends Error {
    constructor(readonly retryAfter: number) {
      super(`Try again in ${retryAfter} ${retryAfter === 1 ? "second" : "seconds"}`);
    }
  },
  NotSaved: class extends Error {},
}));
vi.mock("@/player/bandwidth-probe", () => ({
  CONNECTION_QUERY_KEY: ["playback", "connection"],
  networkInformation: () => probe.network,
  testConnectionSpeed: speedTest,
  ProbeRateLimited: probe.RateLimited,
  ProbeNotSaved: probe.NotSaved,
}));
vi.mock("@/api/v2/request", () => ({ v2: request }));
beforeEach(() => {
  request.mockReset();
  speedTest.mockReset();
  probe.network = undefined;
});
function mount(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return render(
    <QueryClientProvider client={client}>
      <LearnedLimitRow />
      <PlaybackConnection />
    </QueryClientProvider>,
  );
}
const SPEED_FEATURES = { features: ["buffering_quality_v1", "connection_speed_v1"] };
function serve(fields: Record<string, unknown> | (() => Record<string, unknown>)) {
  request.mockImplementation(async (operation: string) =>
    operation.endsWith("capabilities")
      ? SPEED_FEATURES
      : {
          connection: "remote",
          learned_limit: null,
          ...(typeof fields === "function" ? fields() : fields),
        },
  );
}

const connectionReads = () =>
  request.mock.calls.filter(([op]) => op === "GET /api/v2/playback/connection");

it("does not request connection state without the advertised feature", async () => {
  request.mockResolvedValue({ features: [] });
  mount();
  await waitFor(() => expect(request).toHaveBeenCalled());
  expect(request.mock.calls.every(([op]) => op === "GET /api/v2/playback/capabilities")).toBe(true);
  expect(screen.queryByText("Reset")).not.toBeInTheDocument();
});
it("resets the server limit and refreshes the row", async () => {
  let cleared = false;
  request.mockImplementation(async (operation: string) => {
    if (operation.endsWith("capabilities")) return { features: ["buffering_quality_v1"] };
    if (operation.startsWith("DELETE")) {
      cleared = true;
      return;
    }
    return {
      connection: "remote",
      learned_limit: cleared
        ? null
        : {
            kbps: 44100,
            observed_at: "2026-10-17T12:00:00Z",
            expires_at: "2026-10-24T12:00:00Z",
          },
    };
  });
  mount();
  expect(
    await screen.findByText(/^Limited to 44 Mbps after buffering on [A-Z][a-z]{2} 17$/),
  ).toBeInTheDocument();
  fireEvent.click(await screen.findByText("Reset"));
  await waitFor(() => expect(screen.queryByText("Reset")).not.toBeInTheDocument());
  expect(request).toHaveBeenCalledWith("DELETE /api/v2/playback/connection/learned-limit");
});

it("says in the row when a reset fails and keeps the limit", async () => {
  request.mockImplementation(async (operation: string) => {
    if (operation.endsWith("capabilities")) return { features: ["buffering_quality_v1"] };
    if (operation.startsWith("DELETE")) throw new Error("offline");
    return {
      connection: "remote",
      learned_limit: {
        kbps: 44100,
        observed_at: "2026-10-17T12:00:00Z",
        expires_at: "2026-10-24T12:00:00Z",
      },
    };
  });
  mount();
  fireEvent.click(await screen.findByText("Reset"));
  expect(await screen.findByText("Couldn't reset the limit. Try again.")).toBeInTheDocument();
  expect(screen.getByText(/^Limited to 44 Mbps after buffering/)).toBeInTheDocument();
});

it("shows server speed and budget and refreshes after a speed test", async () => {
  serve(() => ({
    probe: { kbps: 48000, measured_at: new Date().toISOString(), transport: "wifi" },
    auto_budget_kbps: 35280,
  }));
  speedTest.mockResolvedValue(48000);
  mount();
  expect(await screen.findByText(/48 Mbps.*Wi-Fi/)).toBeInTheDocument();
  expect(
    screen.getByText("Auto plays videos up to 35 Mbps without converting them."),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByText("Test speed"));
  await waitFor(() => expect(speedTest).toHaveBeenCalledOnce());
  await waitFor(() => expect(connectionReads()).toHaveLength(2));
});

it("reads the connection again whenever Settings opens", async () => {
  serve(() => ({
    probe: { kbps: 9949, measured_at: new Date().toISOString(), transport: "wifi" },
  }));
  // A reading cached from an earlier visit, on what may be another network.
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(["playback", "connection"], {
    connection: "remote",
    learned_limit: null,
    probe: { kbps: 48000, measured_at: new Date().toISOString(), transport: "ethernet" },
  });
  mount(client);
  expect(await screen.findByText("9.9 Mbps · measured just now on Wi-Fi")).toBeInTheDocument();
  expect(connectionReads()).toHaveLength(1);
});

it("says when a speed test fails rather than blaming playback", async () => {
  serve(() => ({
    probe: {
      kbps: 48000,
      measured_at: new Date(Date.now() - 3 * 60 * 60_000 - 60_000).toISOString(),
      transport: "ethernet",
    },
  }));
  speedTest
    .mockRejectedValueOnce(new Error("no measurement"))
    .mockResolvedValueOnce("playing")
    .mockResolvedValueOnce("busy");
  mount();
  expect(await screen.findByText("48 Mbps · measured 3 hours ago on Ethernet")).toBeInTheDocument();
  fireEvent.click(screen.getByText("Test speed"));
  expect(
    await screen.findByText("Couldn't measure the connection speed. Try again."),
  ).toBeInTheDocument();
  fireEvent.click(await screen.findByText("Test speed"));
  expect(
    await screen.findByText("Stop playback before testing, then try again."),
  ).toBeInTheDocument();
  fireEvent.click(await screen.findByText("Test speed"));
  expect(
    await screen.findByText("A speed test is already running. Try again in a moment."),
  ).toBeInTheDocument();
});

it("says a result could not be saved and keeps the server's reading", async () => {
  serve(() => ({
    probe: { kbps: 48000, measured_at: new Date().toISOString(), transport: "wifi" },
  }));
  speedTest.mockRejectedValueOnce(new probe.NotSaved());
  mount();
  fireEvent.click(await screen.findByText("Test speed"));
  expect(await screen.findByText("Couldn't save the speed test. Try again.")).toBeInTheDocument();
  expect(screen.getByText("48 Mbps · measured just now on Wi-Fi")).toBeInTheDocument();
});

it("shows a 429's wait only while it lasts", async () => {
  serve({});
  speedTest.mockRejectedValueOnce(new probe.RateLimited(1));
  mount();
  const button = await screen.findByText("Test speed");
  vi.useFakeTimers();
  onTestFinished(() => {
    vi.useRealTimers();
  });
  await act(async () => fireEvent.click(button));
  expect(screen.getByText("Try again in 1 second")).toBeInTheDocument();
  act(() => vi.advanceTimersByTime(999));
  expect(screen.getByText("Try again in 1 second")).toBeInTheDocument();
  act(() => vi.advanceTimersByTime(1));
  expect(screen.queryByText("Try again in 1 second")).not.toBeInTheDocument();
});

it.each([
  [
    { type: "cellular" },
    "Speed tests are skipped on cellular, and the Cellular quality settings apply.",
  ],
  [{ type: "wifi", saveData: true }, "Speed tests are skipped on metered networks."],
])("replaces the speed, Test speed and the budget on %o", async (network, note) => {
  probe.network = network;
  serve(() => ({
    probe: { kbps: 48000, measured_at: new Date().toISOString(), transport: "wifi" },
    auto_budget_kbps: 35280,
  }));
  mount();
  expect(await screen.findByText(note)).toBeInTheDocument();
  await waitFor(() => expect(connectionReads()).toHaveLength(1));
  expect(screen.queryByText(/48 Mbps/)).not.toBeInTheDocument();
  expect(screen.queryByText("Test speed")).not.toBeInTheDocument();
  expect(screen.queryByText(/without converting/)).not.toBeInTheDocument();
});

it("words a lower bound and a playback sample as faster than, and shows a sample only without a probe", async () => {
  // The sample is higher, but a probe is the reading whenever there is one.
  let reading: Record<string, unknown> = {
    probe: {
      kbps: 48000,
      lower_bound: true,
      measured_at: new Date().toISOString(),
      transport: "ethernet",
    },
    playback_sample: {
      kbps: 51200,
      measured_at: new Date(Date.now() - 2 * 60 * 60_000).toISOString(),
    },
  };
  serve(() => reading);
  let finish: (value: unknown) => void = () => undefined;
  speedTest.mockReturnValue(new Promise((resolve) => (finish = resolve)));
  mount();
  expect(
    await screen.findByText("Faster than 48 Mbps · measured just now on Ethernet"),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByText("Test speed"));
  expect(await screen.findByText("Testing…")).toBeInTheDocument();

  // Without a probe, the sample is the reading.
  reading = { playback_sample: reading.playback_sample };
  finish({ kbps: 48000, lower_bound: false, bytes: 18_000_000, duration_ms: 3000 });
  expect(
    await screen.findByText("Faster than 51 Mbps · seen during playback 2 hours ago"),
  ).toBeInTheDocument();
  expect(screen.getByText("Test speed")).toBeInTheDocument();
});
