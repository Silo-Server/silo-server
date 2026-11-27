// @vitest-environment jsdom
// The real QR library, unmocked: what it draws, and what the link guard makes
// of a QR code whose content it can't read.
import { cleanup, render, screen } from "@testing-library/react";
import QRCode from "react-qr-code";
import { afterEach, expect, it } from "vitest";
import type { SessionAppCode } from "@/api/v2/appCodes";
import { MIN_MODULE_PIXELS, QUIET_ZONE_MODULES } from "@/lib/scanCode";
import vectors from "@/lib/scanCode.vectors.json";
import { SessionCodeStep } from "./AppCodeStep";
import { expectNoLinkCarrying } from "./codeLinkGuard.test-support";

const valid = vectors.cases.filter((c) => c.valid);
const typical = valid[0]!;
const longest = valid.reduce((a, b) => (b.string.length > a.string.length ? b : a));

afterEach(cleanup);

// A module under 3.5 px is hard for a phone camera to resolve off a screen;
// the longest scan code (a 128-byte address) is the densest the page draws.
it.each([
  ["a typical address", typical],
  ["a 128-byte address", longest],
])("draws %s with modules of at least 3.5 px and a four-module margin", (_, c) => {
  const appCode = {
    code: c.app_code!,
    expires_in: 600,
    server_address: c.address!,
    link_allowed: false,
  } as SessionAppCode;
  render(<SessionCodeStep appCode={appCode} />);
  const qr = screen.getByRole("img", { name: "Scan code for the Silo app" });
  const modules = Number(qr.getAttribute("viewBox")!.split(" ")[2]);
  const module = Number(qr.getAttribute("width")) / modules;
  expect(module).toBeGreaterThanOrEqual(MIN_MODULE_PIXELS);
  // The white box around the code is the quiet zone.
  const box = qr.parentElement!;
  expect(box.className).toContain("bg-white");
  expect(parseFloat(box.style.padding)).toBeGreaterThanOrEqual(QUIET_ZONE_MODULES * module);
});

// The guard can't read an unmocked QR code, so it must refuse one rather than
// let a QR code of a link through.
it("fails the link guard on a real QR code of a link", () => {
  const code = typical.app_code!;
  const { container } = render(
    <QRCode value={`silo://join#v1.aHR0cHM6Ly9tZWRpYS5leGFtcGxlLmNvbQ.${code}`} />,
  );
  expect(() => expectNoLinkCarrying(container, code)).toThrow(/neither an icon nor a scan code/);
});
