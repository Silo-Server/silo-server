import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { TourHost } from "./TourHost";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import type { OnboardingFlow } from "@/api/v2/onboarding";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("profile");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
const flow = {
  version: 1,
  tour_id: "tour",
  steps: [
    { id: "welcome", kind: "welcome", title: "Welcome" },
    { id: "last", kind: "feature_card", title: "Final stop" },
  ],
} as OnboardingFlow;
function response(tag: string) {
  return new Response(JSON.stringify({ tour_id: "tour", done: false }), {
    headers: { "Content-Type": "application/json", ETag: tag },
  });
}
it("waits for acknowledged saves before advancing and dismissing", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(response('"one"'))
    .mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
  vi.stubGlobal("fetch", fetch);
  const onDone = vi.fn();
  const client = new QueryClient();
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <TourHost flow={flow} onDone={onDone} />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Show me" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  expect(screen.getByText("Welcome")).toBeTruthy();
  expect(screen.queryByText("Final stop")).toBeNull();
  await act(async () => finish(response('"two"')));
  await screen.findByText("Final stop");
  fireEvent.click(screen.getByRole("button", { name: "Done" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
  expect(onDone).not.toHaveBeenCalled();
  await act(async () => finish(response('"three"')));
  await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
  view.unmount();
  client.clear();
});
it("keeps failed completion visible and requires reload", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(response('"one"'))
    .mockRejectedValueOnce(new Error("uncertain"));
  vi.stubGlobal("fetch", fetch);
  const onDone = vi.fn();
  const client = new QueryClient();
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <TourHost flow={flow} onDone={onDone} />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Skip tour" }));
  await screen.findByRole("alert");
  expect(onDone).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Reload tour" })).toBeTruthy();
  view.unmount();
  client.clear();
});
