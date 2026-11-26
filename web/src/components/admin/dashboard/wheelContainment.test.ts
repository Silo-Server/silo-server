// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";

import { containWheel } from "./wheelContainment";

function setup({ scrollTop, clientHeight, scrollHeight }: Record<string, number>) {
  document.body.innerHTML = `
    <div id="panel">
      <h2 id="header">Add widget</h2>
      <div id="list" data-wheel-scroller><button id="item">Item</button></div>
    </div>`;
  const panel = document.getElementById("panel")!;
  const list = document.getElementById("list")!;
  Object.defineProperties(list, {
    scrollTop: { value: scrollTop },
    clientHeight: { value: clientHeight },
    scrollHeight: { value: scrollHeight },
  });
  const release = containWheel(panel)!;
  return { release, el: (id: string) => document.getElementById(id)! };
}

function wheel(target: Element, init: WheelEventInit) {
  const event = new WheelEvent("wheel", { bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(event);
  return event.defaultPrevented;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("containWheel", () => {
  it("lets the list scroll while it has room in the wheel's direction", () => {
    const { el } = setup({ scrollTop: 50, clientHeight: 100, scrollHeight: 300 });
    expect(wheel(el("item"), { deltaY: 40 })).toBe(false);
    expect(wheel(el("item"), { deltaY: -40 })).toBe(false);
  });

  it("blocks wheel input the list can't take, so the page doesn't scroll", () => {
    const atBottom = setup({ scrollTop: 200, clientHeight: 100, scrollHeight: 300 });
    expect(wheel(atBottom.el("item"), { deltaY: 40 })).toBe(true);
    atBottom.release();

    const atTop = setup({ scrollTop: 0, clientHeight: 100, scrollHeight: 300 });
    expect(wheel(atTop.el("item"), { deltaY: -40 })).toBe(true);
    atTop.release();

    const short = setup({ scrollTop: 0, clientHeight: 300, scrollHeight: 300 });
    expect(wheel(short.el("item"), { deltaY: 40 })).toBe(true);
  });

  it("blocks wheel input over the panel outside the list", () => {
    const { el } = setup({ scrollTop: 50, clientHeight: 100, scrollHeight: 300 });
    expect(wheel(el("header"), { deltaY: 40 })).toBe(true);
  });

  it("leaves ctrl+wheel zoom alone", () => {
    const { el } = setup({ scrollTop: 0, clientHeight: 300, scrollHeight: 300 });
    expect(wheel(el("header"), { deltaY: 40, ctrlKey: true })).toBe(false);
  });
});
