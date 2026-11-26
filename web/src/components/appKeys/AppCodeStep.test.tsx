// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { SessionAppCode } from "@/api/v2/appCodes";
import vectors from "@/lib/scanCode.vectors.json";
import { SessionCodeStep } from "./AppCodeStep";
import { expectNoLinkCarrying } from "./codeLinkGuard.test-support";

vi.mock("react-qr-code", async () => (await import("./codeLinkGuard.test-support")).qrCodeMock());
const copyTextToClipboard = vi.hoisted(() => vi.fn());
vi.mock("@/lib/clipboard", () => ({ copyTextToClipboard }));

const known = vectors.cases.find((c) => c.valid)!;

function step(server_address: string) {
  const appCode = {
    code: known.app_code!,
    expires_in: 600,
    server_address,
    link_allowed: false,
  } as SessionAppCode;
  return render(<SessionCodeStep appCode={appCode} />);
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// The QR code must hold exactly the text the app's scanner reads; anything
// else is a picture that does nothing (or, worse, a link).
it("draws the scan code for the code and address", () => {
  const { container } = step(known.address!);
  const qr = screen.getByRole("img", { name: "Scan code for the Silo app" });
  expect(qr.getAttribute("data-qr-value")).toBe(known.string);
  expectNoLinkCarrying(container, known.app_code!);
});

// Rule: an address over 128 bytes has no scan code; the address and the code
// are shown to type, and Copy code stays for a device that can't scan.
it("draws no scan code for an address over 128 bytes", () => {
  const long = `https://example.com/${"a".repeat(109)}`;
  const { container } = step(long);
  expect(container.querySelector("svg[data-qr-value]")).toBeNull();
  expect(screen.getByText(long)).toBeTruthy();
  expect(screen.getByText(known.app_code!)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Copy code" })).toBeTruthy();
  expectNoLinkCarrying(container, known.app_code!);
});

// Copy code puts the code alone on the clipboard (the app's paste field takes
// the code, and a copied link could be opened by another app).
it("copies the code alone", async () => {
  copyTextToClipboard.mockResolvedValueOnce(undefined);
  step(known.address!);
  fireEvent.click(screen.getByRole("button", { name: "Copy code" }));
  expect(await screen.findByText("Code copied")).toBeTruthy();
  expect(copyTextToClipboard).toHaveBeenCalledWith(known.app_code);
});

// A browser that refuses the clipboard: say so, and the code stays on screen
// to select by hand.
it("says when the code can't be copied", async () => {
  copyTextToClipboard.mockRejectedValueOnce(new Error("denied"));
  step(known.address!);
  fireEvent.click(screen.getByRole("button", { name: "Copy code" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Couldn’t copy the code");
  expect(screen.getByText(known.app_code!)).toBeTruthy();
});
