import { expect } from "vitest";

/**
 * Fails if anything in container could hand `code` to a device: an attribute
 * holding it (href, src, data), a link, image, svg or canvas (a QR code), or
 * text with a URL that carries it. A code whose answer said link_allowed:
 * false is shown only as text to type: a link can be received by an app
 * other than Silo, which would redeem the code first.
 */
export function expectNoLinkCarrying(container: HTMLElement, code: string) {
  for (const element of container.querySelectorAll("*")) {
    for (const attribute of element.attributes) {
      expect(attribute.value, `${element.tagName} ${attribute.name}`).not.toContain(code);
    }
  }
  const urlWithCode = new RegExp(`(silo|https?):[^\\s"']*${code}`);
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    expect(node.textContent ?? "").not.toMatch(urlWithCode);
  }
  expect(container.querySelector("a[href*='join'], img, canvas, svg[data-qr]")).toBeNull();
}
