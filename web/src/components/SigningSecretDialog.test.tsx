import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SigningSecretDialog } from "./SigningSecretDialog";

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

const SECRET = "whsec_test_secret";
const execCommand = vi.fn();

// jsdom has no navigator.clipboard, like a browser on a plain-HTTP origin, so
// the copy goes through copyTextToClipboard's execCommand fallback.
beforeEach(() => {
  Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
});
afterEach(() => {
  execCommand.mockReset();
  toastError.mockReset();
});

function clickCopy() {
  render(<SigningSecretDialog secret={SECRET} onClose={() => {}} />);
  fireEvent.click(screen.getByRole("button", { name: "Copy signing secret" }));
}

it("copies the secret without the async Clipboard API and shows it was copied", async () => {
  execCommand.mockReturnValue(true);
  clickCopy();
  expect(await screen.findByRole("button", { name: "Signing secret copied" })).toBeInTheDocument();
  expect(execCommand).toHaveBeenCalledWith("copy");
  expect(toastError).not.toHaveBeenCalled();
});

it("reports a failed copy instead of showing it was copied", async () => {
  // The secret is shown once; a false "copied" would let the user close it unsaved.
  execCommand.mockReturnValue(false);
  clickCopy();
  await waitFor(() => expect(toastError).toHaveBeenCalledWith("Failed to copy to clipboard"));
  expect(screen.getByRole("button", { name: "Copy signing secret" })).toBeInTheDocument();
});
