import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { PlaybackConnection } from "./PlaybackConnection";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", () => ({ v2: request }));
beforeEach(() => {
  request.mockReset();
});
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <PlaybackConnection />
    </QueryClientProvider>,
  );
}
it("does not request connection state without the advertised feature", async () => {
  request.mockResolvedValue({ features: [] });
  mount();
  await waitFor(() => expect(request).toHaveBeenCalledTimes(1));
  expect(request).toHaveBeenCalledWith("GET /api/v2/playback/capabilities");
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
  fireEvent.click(await screen.findByText("Reset"));
  await waitFor(() => expect(screen.queryByText("Reset")).not.toBeInTheDocument());
  expect(request).toHaveBeenCalledWith("DELETE /api/v2/playback/connection/learned-limit");
});
