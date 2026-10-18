import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { LearnedLimitRow, PlaybackConnection } from "./PlaybackConnection";

const request = vi.hoisted(() => vi.fn());
const speedTest = vi.hoisted(() => vi.fn());
vi.mock("@/player/bandwidth-probe", () => ({
  networkInformation: () => undefined,
  onMeteredConnection: () => false,
  testConnectionSpeed: speedTest,
  ProbeRateLimited: class extends Error {},
}));
vi.mock("@/api/v2/request", () => ({ v2: request }));
beforeEach(() => {
  request.mockReset();
});
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LearnedLimitRow />
      <PlaybackConnection />
    </QueryClientProvider>,
  );
}
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
    await screen.findByText(/^Limited to 44.1 Mbps after buffering on [A-Z][a-z]{2} 17$/),
  ).toBeInTheDocument();
  fireEvent.click(await screen.findByText("Reset"));
  await waitFor(() => expect(screen.queryByText("Reset")).not.toBeInTheDocument());
  expect(request).toHaveBeenCalledWith("DELETE /api/v2/playback/connection/learned-limit");
});

it("shows server speed and budget and refreshes after a speed test", async () => {
  request.mockImplementation(async (operation: string) =>
    operation.endsWith("capabilities")
      ? { features: ["buffering_quality_v1", "connection_speed_v1"] }
      : {
          connection: "remote",
          learned_limit: null,
          probe: { kbps: 48000, measured_at: new Date().toISOString(), transport: "wifi" },
          auto_budget_kbps: 35280,
        },
  );
  speedTest.mockResolvedValue(48000);
  mount();
  expect(await screen.findByText(/48 Mbps.*Wi-Fi/)).toBeInTheDocument();
  expect(screen.getByText(/35.3 Mbps without converting/)).toBeInTheDocument();
  fireEvent.click(screen.getByText("Test speed"));
  await waitFor(() => expect(speedTest).toHaveBeenCalledOnce());
  await waitFor(() =>
    expect(
      request.mock.calls.filter(([op]) => op === "GET /api/v2/playback/connection"),
    ).toHaveLength(2),
  );
});

it("says when a speed test fails rather than blaming playback", async () => {
  request.mockImplementation(async (operation: string) =>
    operation.endsWith("capabilities")
      ? { features: ["buffering_quality_v1", "connection_speed_v1"] }
      : {
          connection: "remote",
          learned_limit: null,
          probe: {
            kbps: 48000,
            measured_at: new Date(Date.now() - 3 * 60 * 60_000 - 60_000).toISOString(),
            transport: "ethernet",
          },
        },
  );
  speedTest.mockRejectedValueOnce(new Error("no measurement")).mockResolvedValueOnce(null);
  mount();
  expect(await screen.findByText("48 Mbps · measured 3 hours ago on Ethernet")).toBeInTheDocument();
  fireEvent.click(screen.getByText("Test speed"));
  expect(
    await screen.findByText("Could not measure connection speed. Try again."),
  ).toBeInTheDocument();
  fireEvent.click(await screen.findByText("Test speed"));
  expect(
    await screen.findByText("Stop playback before testing, then try again."),
  ).toBeInTheDocument();
});
