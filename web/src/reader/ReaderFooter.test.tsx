// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ReaderFooter from "./ReaderFooter";

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function render(props: Partial<React.ComponentProps<typeof ReaderFooter>> = {}) {
  const defaults = {
    fraction: 0.34,
    extent: { start: 0.28, end: 0.42, index: 6 },
    chapterLabel: "The Duke",
    onScrub: vi.fn(),
    onShowShortcuts: vi.fn(),
  };
  const merged = { ...defaults, ...props };
  act(() => root.render(<ReaderFooter {...merged} />));
  return merged;
}

describe("ReaderFooter", () => {
  it("shows chapter label, percentage, and the chapter band", () => {
    render();
    expect(container.textContent).toContain("The Duke");
    expect(container.textContent).toContain("34%");
    const band = container.querySelector("[data-chapter-band]") as HTMLElement;
    expect(band).not.toBeNull();
    expect(parseFloat(band.style.left)).toBeCloseTo(28);
    expect(parseFloat(band.style.width)).toBeCloseTo(14);
  });

  it("hides band and label without an extent", () => {
    render({ extent: null, chapterLabel: "The Duke" });
    expect(container.querySelector("[data-chapter-band]")).toBeNull();
    expect(container.textContent).not.toContain("The Duke");
  });

  it("scrubs the whole book from the slider", () => {
    const { onScrub } = render();
    const slider = container.querySelector('input[type="range"]') as HTMLInputElement;
    slider.value = "62";
    act(() => {
      slider.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(onScrub).toHaveBeenCalledWith(0.62);
  });

  it("opens the shortcuts overlay from the ? affordance", () => {
    const { onShowShortcuts } = render();
    const btn = container.querySelector('[aria-label="Keyboard shortcuts"]') as HTMLButtonElement;
    act(() => btn.click());
    expect(onShowShortcuts).toHaveBeenCalled();
  });
});
