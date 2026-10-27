// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createBrowserRouter, MemoryRouter, RouterProvider, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useScrollRestoration } from "./useScrollRestoration";

function Harness() {
  useScrollRestoration();
  const navigate = useNavigate();
  return (
    <>
      <button type="button" onClick={() => navigate("/item/1")}>
        open item
      </button>
      <button type="button" onClick={() => navigate(-1)}>
        back
      </button>
      <button type="button" onClick={() => navigate("/library?tab=library", { replace: true })}>
        normalize
      </button>
    </>
  );
}

let scrollHeight = 5_000;
let scrollY = 0;
const frames = new Map<number, FrameRequestCallback>();
let nextFrameID = 1;

function setScrollY(top: number) {
  scrollY = top;
  window.dispatchEvent(new Event("scroll"));
}

function runNextFrame() {
  const next = frames.entries().next().value;
  if (!next) throw new Error("no animation frame was requested");
  const [id, callback] = next;
  frames.delete(id);
  act(() => callback(performance.now()));
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Harness />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  scrollHeight = 5_000;
  scrollY = 0;
  frames.clear();
  nextFrameID = 1;
  Object.defineProperty(window, "scrollY", { configurable: true, get: () => scrollY });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  vi.spyOn(document.documentElement, "scrollHeight", "get").mockImplementation(() => scrollHeight);
  vi.spyOn(window, "scrollTo").mockImplementation(
    (options: ScrollToOptions | number, y?: number) => {
      const top = typeof options === "number" ? (y ?? 0) : (options.top ?? 0);
      const furthest = Math.max(0, scrollHeight - window.innerHeight);
      setScrollY(Math.max(0, Math.min(top, furthest)));
    },
  );
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    const id = nextFrameID++;
    frames.set(id, callback);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    frames.delete(id);
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete (window as { scrollY?: number }).scrollY;
  delete (window as { innerHeight?: number }).innerHeight;
  window.history.replaceState(null, "", "/");
});

describe("useScrollRestoration", () => {
  it("restores the list's position on Back and opens a pushed page at the top", () => {
    renderAt("/library");
    act(() => setScrollY(400));

    fireEvent.click(screen.getByRole("button", { name: "open item" }));
    expect(scrollY).toBe(0);

    fireEvent.click(screen.getByRole("button", { name: "back" }));
    runNextFrame();
    expect(scrollY).toBe(400);
  });

  it("waits for a virtualized list to grow before restoring", () => {
    renderAt("/library");
    act(() => setScrollY(400));
    fireEvent.click(screen.getByRole("button", { name: "open item" }));

    // The title page is short, so the commit clamps the library behind it
    // until its rows load.
    scrollHeight = 800;
    fireEvent.click(screen.getByRole("button", { name: "back" }));
    runNextFrame();
    expect(scrollY).toBe(0);

    scrollHeight = 5_000;
    runNextFrame();
    expect(scrollY).toBe(400);
  });

  it("keeps the position when a replace only corrects the URL", () => {
    renderAt("/library");
    act(() => setScrollY(400));

    fireEvent.click(screen.getByRole("button", { name: "normalize" }));

    expect(scrollY).toBe(400);
  });

  it("finishes a pending restore after the URL is replaced", () => {
    renderAt("/library");
    act(() => setScrollY(400));
    fireEvent.click(screen.getByRole("button", { name: "open item" }));

    scrollHeight = 800;
    fireEvent.click(screen.getByRole("button", { name: "back" }));
    fireEvent.click(screen.getByRole("button", { name: "normalize" }));

    scrollHeight = 5_000;
    runNextFrame();
    expect(scrollY).toBe(400);
  });

  it("leaves the browser's jump to an in-page anchor alone", async () => {
    // A data router's first entry and an entry a plain `#anchor` link creates
    // both carry React Router's key "default".
    window.history.replaceState(null, "", "/library");
    const router = createBrowserRouter([{ path: "*", element: <Harness /> }]);
    render(<RouterProvider router={router} />);
    act(() => setScrollY(3_000));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "open item" }));
    });
    act(() => setScrollY(200));

    // What the browser does for a skip link: a new entry with no state, the
    // jump to the anchor, then popstate.
    await act(async () => {
      window.history.pushState(null, "", "/item/1#main-content");
      setScrollY(40);
      window.dispatchEvent(new PopStateEvent("popstate", { state: null }));
    });

    expect(frames.size).toBe(0);
    expect(scrollY).toBe(40);
  });

  it("stops restoring once the reader takes over", () => {
    renderAt("/library");
    act(() => setScrollY(400));
    fireEvent.click(screen.getByRole("button", { name: "open item" }));

    scrollHeight = 800;
    fireEvent.click(screen.getByRole("button", { name: "back" }));
    fireEvent.wheel(window);

    scrollHeight = 5_000;
    expect(frames.size).toBe(0);
    expect(scrollY).toBe(0);
  });
});
