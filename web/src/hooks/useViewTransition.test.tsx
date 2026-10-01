import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { To } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { resetNavigationHistory, resolveCommittedDirection } from "@/lib/navigationHistory";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
}));

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return {
    ...actual,
    useNavigate: () => mocks.navigate,
  };
});

import { useViewTransitionNavigate, type ViewTransitionNavigateOptions } from "./useViewTransition";

/** Stands in for the browser having committed the entry at `idx`. */
function commit(idx: number) {
  window.history.replaceState({ idx }, "");
}

function Harness({
  to,
  options,
  at = "/item/season",
}: {
  to: To | number;
  options?: ViewTransitionNavigateOptions;
  at?: string;
}) {
  function Trigger() {
    const navigate = useViewTransitionNavigate();
    return (
      <button type="button" onClick={() => navigate(to, options)}>
        go
      </button>
    );
  }

  return (
    <MemoryRouter initialEntries={[at]}>
      <Trigger />
    </MemoryRouter>
  );
}

async function go() {
  await userEvent.click(screen.getByRole("button", { name: "go" }));
}

afterEach(() => {
  mocks.navigate.mockClear();
  resetNavigationHistory();
  window.history.replaceState(null, "");
  delete document.documentElement.dataset.navigationDirection;
  vi.unstubAllGlobals();
});

describe("useViewTransitionNavigate", () => {
  it("opts an ordinary navigation into a forward view transition", async () => {
    render(<Harness to="/item/episode" />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith("/item/episode", {
      replace: false,
      viewTransition: true,
    });
    expect(document.documentElement.dataset.navigationDirection).toBe("forward");
  });

  it("replaces rather than pushes a second entry for the current URL", async () => {
    render(<Harness to="/item/season?tab=extras" at="/item/season?tab=extras" />);
    await go();

    // React Router does this for `<Link>`; imperative callers get nothing, and
    // a duplicate entry makes the browser's back button look broken.
    expect(mocks.navigate).toHaveBeenCalledWith("/item/season?tab=extras", {
      replace: true,
      viewTransition: true,
    });
  });

  it("leaves an explicit replace alone", async () => {
    render(<Harness to="/item/episode" options={{ replace: false }} />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith("/item/episode", {
      replace: false,
      viewTransition: true,
    });
  });

  it("pushes an `up` target, stamping only the direction", async () => {
    commit(0);
    resolveCommittedDirection();

    render(<Harness to="/item/series" options={{ up: true }} />);
    await go();

    // `up` changes the motion, never the entry: backwards animation, ordinary
    // push, every NavigateOptions field forwarded.
    expect(mocks.navigate).toHaveBeenCalledWith("/item/series", {
      replace: false,
      viewTransition: true,
    });
    expect(document.documentElement.dataset.navigationDirection).toBe("back");
  });

  it("will not push a duplicate entry for an `up` link to the current URL", async () => {
    render(<Harness to="/item/season" options={{ up: true }} at="/item/season" />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith("/item/season", {
      replace: true,
      viewTransition: true,
    });
  });

  it("reads direction off the sign of a raw delta", async () => {
    render(<Harness to={-1} />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith(-1);
    expect(document.documentElement.dataset.navigationDirection).toBe("back");
  });

  it("does not forward `up` to the router as a navigate option", async () => {
    render(<Harness to="/item/series" options={{ up: true, state: { from: "crumb" } }} />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith("/item/series", {
      state: { from: "crumb" },
      replace: false,
      viewTransition: true,
    });
  });

  it.each([
    ["enters", "/", "/item/movie-1"],
    ["leaves", "/item/movie-1", "/"],
  ])("skips the root snapshot when desktop navigation %s an item route", async (_label, at, to) => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
    }));
    render(<Harness to={to} at={at} />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith(to, {
      replace: false,
      viewTransition: false,
    });
  });

  it("keeps mobile item navigation on the viewport transition", async () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    render(<Harness to="/item/movie-1" at="/" />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith("/item/movie-1", {
      replace: false,
      viewTransition: true,
    });
  });

  it.each([
    ["item to item", "/item/series-1", "/item/episode-1"],
    ["library to item", "/library/1", "/item/movie-1"],
    ["item to library", "/item/movie-1", "/library/1"],
  ])("keeps %s navigation on its existing transition", async (_label, at, to) => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
    }));
    render(<Harness to={to} at={at} />);
    await go();

    expect(mocks.navigate).toHaveBeenCalledWith(to, {
      replace: false,
      viewTransition: true,
    });
  });
});
