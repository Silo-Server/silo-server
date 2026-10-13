// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { Profile } from "@/api/types";
import { v2Problem } from "@/api/v2/problems.test-support";

import { ProfilePinDialog } from "./ProfilePinDialog";
import { pinLockoutMessage } from "./pinLockout";

afterEach(() => {
  cleanup();
});

const profile = { id: "profile-1", name: "Kid", has_pin: true } as Profile;

function renderDialog(verifyPin: (id: string, pin: string) => Promise<{ valid: boolean }>) {
  const onVerified = vi.fn();
  render(
    <ProfilePinDialog
      profile={profile}
      onClose={() => {}}
      onVerified={onVerified}
      verifyPin={verifyPin}
    />,
  );
  return { onVerified };
}

function submitPin(pin: string) {
  fireEvent.change(screen.getByLabelText("PIN"), { target: { value: pin } });
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
}

describe("ProfilePinDialog", () => {
  it("says the PIN was wrong when the server answers valid false", async () => {
    renderDialog(vi.fn().mockResolvedValue({ valid: false }));
    submitPin("0000");
    expect(await screen.findByText("Incorrect PIN")).toBeTruthy();
  });

  it("shows the lockout wait instead of a wrong-PIN message on 429", async () => {
    const verifyPin = vi.fn().mockRejectedValue(
      v2Problem(429, "rate_limited", "Too many incorrect PINs. Try again later.", {
        retryAfterSeconds: 241,
      }),
    );
    const { onVerified } = renderDialog(verifyPin);
    submitPin("1234");
    expect(
      await screen.findByText("Too many incorrect PINs. Try again in 5 minutes."),
    ).toBeTruthy();
    expect(screen.queryByText("Incorrect PIN")).toBeNull();
    expect(onVerified).not.toHaveBeenCalled();
    await waitFor(() => expect((screen.getByLabelText("PIN") as HTMLInputElement).value).toBe(""));
  });

  it("keeps the generic failure for other errors", async () => {
    renderDialog(vi.fn().mockRejectedValue(new Error("offline")));
    submitPin("1234");
    expect(await screen.findByText("Verification failed")).toBeTruthy();
  });
});

describe("pinLockoutMessage", () => {
  it("rounds the wait up to whole minutes", () => {
    const locked = (seconds?: number) =>
      v2Problem(429, "rate_limited", "locked", { retryAfterSeconds: seconds });
    expect(pinLockoutMessage(locked(300))).toBe("Too many incorrect PINs. Try again in 5 minutes.");
    expect(pinLockoutMessage(locked(30))).toBe("Too many incorrect PINs. Try again in 1 minute.");
    expect(pinLockoutMessage(locked())).toBe("Too many incorrect PINs. Try again later.");
  });

  it("ignores everything but a 429", () => {
    expect(pinLockoutMessage(v2Problem(404, "not_found", "gone"))).toBeNull();
    expect(pinLockoutMessage(new Error("boom"))).toBeNull();
  });
});
