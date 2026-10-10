import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { OnboardingGate } from "./OnboardingGate";

const onboarding = vi.hoisted(() => ({
  done: false,
  mutate: vi.fn(),
  mutateAsync: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ profile: { id: "profile" } }),
}));

vi.mock("@/hooks/queries/onboarding", () => ({
  useOnboardingState: () => ({ data: { tour_id: "tour", done: onboarding.done } }),
  useOnboardingFlow: ({ enabled }: { enabled: boolean }) => ({
    data: enabled
      ? {
          tour_id: "tour",
          steps: [{ id: "welcome", kind: "welcome", title: "Welcome" }],
        }
      : undefined,
  }),
  useOnboardingProgress: () => ({
    mutate: onboarding.mutate,
    mutateAsync: onboarding.mutateAsync,
    isPending: false,
    isError: false,
  }),
}));

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("profile");
  onboarding.done = false;
  onboarding.mutate.mockReset();
  onboarding.mutateAsync.mockReset().mockResolvedValue(undefined);
});

afterEach(cleanup);

function renderGate() {
  return render(
    <MemoryRouter>
      <OnboardingGate>
        <h1>Home</h1>
      </OnboardingGate>
    </MemoryRouter>,
  );
}

it("keeps Home mounted while loading the tour and after dismissal", async () => {
  renderGate();
  const home = screen.getByRole("heading", { name: "Home" });
  await screen.findByRole("dialog", { name: "Feature tour" });
  expect(screen.getByRole("heading", { name: "Home" })).toBe(home);

  fireEvent.click(screen.getByRole("button", { name: "Skip tour" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(onboarding.mutateAsync).toHaveBeenCalledWith({
    tour_id: "tour",
    last_step: "welcome",
    completed: false,
    skipped: true,
  });
  expect(screen.getByRole("heading", { name: "Home" })).toBe(home);
});

it("does not show a tour for a profile that already finished it", () => {
  onboarding.done = true;
  renderGate();
  expect(screen.getByRole("heading", { name: "Home" })).toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(onboarding.mutateAsync).not.toHaveBeenCalled();
});
