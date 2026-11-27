import { createElement } from "react";
import { expect } from "vitest";

/** A scan code: text for the Silo app's scanner, never a URL. */
const scanCode = /^SILOSCANA [A-P]{64} (?:[A-P]{2})+$/;

/**
 * Stands in for react-qr-code: an svg that names its content in
 * data-qr-value, so a test can read what the QR code holds. vi.mock is
 * hoisted above imports, so its factory imports this module:
 * `vi.mock("react-qr-code", async () => (await import("…/codeLinkGuard.test-support")).qrCodeMock())`.
 */
export function qrCodeMock() {
  return {
    default: ({ value, size: _size, ...props }: { value: string; size?: number }) =>
      createElement("svg", { ...props, "data-qr-value": value }),
  };
}

/**
 * Fails if anything in container could hand `code` to a device: an attribute
 * holding it (href, src, data), a link, image or canvas, an svg that is
 * neither an icon (aria-hidden) nor a QR code of a scan code, or text with a
 * URL that carries it. A code whose answer said link_allowed: false is shown
 * as text to type or copy and as a scan code: a link can be received by an
 * app other than Silo, which would redeem the code first. A QR code's content
 * is only readable where the test mocks react-qr-code with qrCodeMock, so an
 * unmocked QR code, of a scan code or of anything else, fails.
 */
export function expectNoLinkCarrying(container: HTMLElement, code: string) {
  for (const element of container.querySelectorAll("*")) {
    for (const attribute of element.attributes) {
      if (attribute.name === "data-qr-value") {
        expect(attribute.value, "QR code content").toMatch(scanCode);
        continue;
      }
      expect(attribute.value, `${element.tagName} ${attribute.name}`).not.toContain(code);
    }
  }
  const urlWithCode = new RegExp(`(silo|https?):[^\\s"']*${code}`);
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    expect(node.textContent ?? "").not.toMatch(urlWithCode);
  }
  for (const svg of container.querySelectorAll("svg")) {
    // An svg inside another (a nested group) is judged with its outer svg.
    if (svg.parentElement?.closest("svg")) continue;
    if (svg.getAttribute("aria-hidden") === "true") continue;
    expect(
      svg.getAttribute("data-qr-value") ?? "",
      "svg that is neither an icon nor a scan code",
    ).toMatch(scanCode);
  }
  expect(container.querySelector("a[href*='join'], img, canvas")).toBeNull();
}
