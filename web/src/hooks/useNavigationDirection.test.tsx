// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { markNavigationDirection, resetNavigationHistory } from "@/lib/navigationHistory";

import { useNavigationDirection } from "./useNavigationDirection";

/**
 * These drive real router navigations rather than synthetic `popstate` events,
 * because the navigation type is what the hook branches on and only the router
 * can produce it.
 *
 * What they still cannot prove is listener ordering against a real browser
 * history — MemoryRouter keeps its own stack and registers no `popstate`
 * listener at all. An earlier revision derived direction from a listener and
 * every test here passed while browser Back was a silent no-op in the app. The
 * ordering is now structural rather than racy — one writer, in the location
 * effect — which is what makes these tests meaningful; they pin the arithmetic,
 * not the race.
 *
 * `window.history.state.idx` is the index the hook reads, and MemoryRouter never
 * writes it, so each test sets it by hand exactly as the browser would have
 * before the router commits.
 */
function Harness({ to }: { to?: string }) {
  useNavigationDirection();
  const navigate = useNavigate();
  return (
    <>
      {to ? <button onClick={() => navigate(to)}>push</button> : null}
      <button onClick={() => navigate(-1)}>back</button>
      <button onClick={() => navigate(1)}>forward</button>
    </>
  );
}

/** The index the browser is already on when the tree mounts. */
function startAt(idx: number) {
  window.history.replaceState({ idx }, "");
}

/** Commits the index the browser would have installed, then navigates. */
function travel(button: "push" | "back" | "forward", idx: number | undefined) {
  window.history.replaceState(idx === undefined ? {} : { idx }, "");
  fireEvent.click(screen.getByRole("button", { name: button }));
}

function direction() {
  return document.documentElement.dataset.navigationDirection;
}

beforeEach(() => {
  resetNavigationHistory();
  window.history.replaceState({ idx: 0 }, "");
  delete document.documentElement.dataset.navigationDirection;
});

afterEach(() => {
  window.history.replaceState(null, "");
  delete document.documentElement.dataset.navigationDirection;
});

describe("useNavigationDirection", () => {
  it("stamps back when the router commits an earlier entry", () => {
    startAt(1);
    render(
      <MemoryRouter initialEntries={["/a", "/b"]} initialIndex={1}>
        <Harness />
      </MemoryRouter>,
    );

    travel("back", 0);

    expect(direction()).toBe("back");
  });

  it("stamps forward when the router commits a later entry", () => {
    startAt(1);
    render(
      <MemoryRouter initialEntries={["/a", "/b"]} initialIndex={1}>
        <Harness />
      </MemoryRouter>,
    );

    travel("back", 0);
    travel("forward", 1);

    expect(direction()).toBe("forward");
  });

  it("clears a stale direction when the pop's direction is unknowable", () => {
    startAt(1);
    render(
      <MemoryRouter initialEntries={["/a", "/b"]} initialIndex={1}>
        <Harness />
      </MemoryRouter>,
    );
    markNavigationDirection("back");

    // No router index on the entry: something outside the router pushed it.
    travel("back", undefined);

    expect(direction()).toBeUndefined();
  });

  it("leaves the pushing caller's direction standing", () => {
    render(
      <MemoryRouter initialEntries={["/item/series"]}>
        <Harness to="/item/season" />
      </MemoryRouter>,
    );

    // A push is stamped at click time by whoever knew the intent. Re-deriving it
    // from the index here could only agree — or, for a replace, wrongly clear it.
    markNavigationDirection("forward");
    travel("push", 1);

    expect(direction()).toBe("forward");
  });

  it("compares against the entry we came from, not the last one committed", () => {
    startAt(2);
    render(
      <MemoryRouter initialEntries={["/a", "/b", "/c"]} initialIndex={2}>
        <Harness />
      </MemoryRouter>,
    );

    travel("back", 1);
    expect(direction()).toBe("back");

    // Without advancing the tracked index on every commit, returning to idx 2
    // would compare 2 against 2 and lose the direction.
    travel("forward", 2);
    expect(direction()).toBe("forward");
  });

  it("stops stamping once unmounted", () => {
    startAt(1);
    const { unmount } = render(
      <MemoryRouter initialEntries={["/a", "/b"]} initialIndex={1}>
        <Harness />
      </MemoryRouter>,
    );
    unmount();

    window.history.replaceState({ idx: 0 }, "");

    expect(direction()).toBeUndefined();
  });
});
