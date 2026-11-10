import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SigningSecretDialog } from "./SigningSecretDialog";

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

const SECRET = "whsec_test_secret";
const execCommand = vi.fn();
let copied = "";

// Like a browser, execCommand("copy") copies the focused field's selection.
// The dialog traps focus, so a fallback textarea mounted outside it copies nothing.
function copySelection() {
  const el = document.activeElement;
  copied =
    el instanceof HTMLTextAreaElement ? el.value.slice(el.selectionStart, el.selectionEnd) : "";
  return true;
}

// jsdom has no navigator.clipboard, like a browser on a plain-HTTP origin, so
// the copy goes through copyTextToClipboard's execCommand fallback.
beforeEach(() => {
  Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
});
afterEach(() => {
  execCommand.mockReset();
  toastError.mockReset();
  copied = "";
});

function clickCopy({ blurFirst = false } = {}) {
  render(<SigningSecretDialog secret={SECRET} onClose={() => {}} />);
  // Safari doesn't focus a clicked button, so focus can sit on the body.
  if (blurFirst) (document.activeElement as HTMLElement | null)?.blur();
  fireEvent.click(screen.getByRole("button", { name: "Copy signing secret" }));
}

it.each([
  ["focus inside the dialog", false],
  ["focus on the body", true],
])("copies the secret without the async Clipboard API (%s)", async (_, blurFirst) => {
  execCommand.mockImplementation(copySelection);
  clickCopy({ blurFirst });
  expect(await screen.findByRole("button", { name: "Signing secret copied" })).toBeInTheDocument();
  expect(execCommand).toHaveBeenCalledWith("copy");
  expect(copied).toBe(SECRET);
  expect(toastError).not.toHaveBeenCalled();
});

it("reports a failed copy instead of showing it was copied", async () => {
  // The secret is shown once; a false "copied" would let the user close it unsaved.
  execCommand.mockReturnValue(false);
  clickCopy();
  await waitFor(() => expect(toastError).toHaveBeenCalledWith("Failed to copy to clipboard"));
  expect(screen.getByRole("button", { name: "Copy signing secret" })).toBeInTheDocument();
});
