import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { OnboardingGate } from "./OnboardingGate";

const tourLoad = vi.hoisted(() => ({
  started: vi.fn(),
  reject: undefined as ((error: Error) => void) | undefined,
}));

vi.mock(
  "./TourHost",
  () =>
    new Promise((_, reject) => {
      tourLoad.reject = reject;
      tourLoad.started();
    }),
);
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ profile: { id: "profile" } }),
}));
vi.mock("@/hooks/queries/onboarding", () => ({
  useOnboardingState: () => ({ data: { tour_id: "tour", done: false } }),
  useOnboardingFlow: () => ({
    data: {
      tour_id: "tour",
      steps: [{ id: "welcome", kind: "welcome", title: "Welcome" }],
    },
  }),
  useOnboardingProgress: () => ({ isPending: false, isError: false }),
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("keeps Home mounted when the tour chunk fails to download", async () => {
  const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
  render(
    <MemoryRouter>
      <OnboardingGate>
        <h1>Home</h1>
      </OnboardingGate>
    </MemoryRouter>,
  );
  const home = screen.getByRole("heading", { name: "Home" });
  await waitFor(() => expect(tourLoad.started).toHaveBeenCalledOnce());
  await act(async () => {
    tourLoad.reject!(new Error("Failed to fetch dynamically imported module"));
  });
  await waitFor(() =>
    expect(consoleError).toHaveBeenCalledWith(
      "[LocalErrorBoundary]",
      expect.anything(),
      expect.any(String),
    ),
  );
  expect(screen.getByRole("heading", { name: "Home" })).toBe(home);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});
