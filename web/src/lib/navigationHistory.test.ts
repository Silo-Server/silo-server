import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  hasEarlierEntry,
  markNavigationDirection,
  resolveCommittedDirection,
  resetNavigationHistory,
} from "./navigationHistory";

/** Stands in for the browser having committed the entry at `idx`. */
function commit(idx: number) {
  window.history.replaceState({ idx }, "");
}

/** Walks an index in from a cold start, as a run of pushes would. */
function pushAll(depth: number) {
  for (let idx = 0; idx < depth; idx += 1) {
    commit(idx);
    resolveCommittedDirection();
  }
}

beforeEach(() => {
  resetNavigationHistory();
  window.history.replaceState(null, "");
  delete document.documentElement.dataset.navigationDirection;
});

afterEach(() => {
  window.history.replaceState(null, "");
  delete document.documentElement.dataset.navigationDirection;
});

describe("markNavigationDirection", () => {
  it("stamps the direction CSS selects page motion on", () => {
    markNavigationDirection("forward");
    expect(document.documentElement.dataset.navigationDirection).toBe("forward");

    markNavigationDirection("back");
    expect(document.documentElement.dataset.navigationDirection).toBe("back");
  });

  it("removes the attribute for an unknowable direction rather than guessing", () => {
    markNavigationDirection("back");
    markNavigationDirection(null);

    // Absent, not "forward": the neutral transition is the honest default, and
    // a stale "back" would play the wrong way on the next navigation.
    expect(document.documentElement.dataset.navigationDirection).toBeUndefined();
  });
});

describe("resolveCommittedDirection", () => {
  it("reads back and forward off the committed history index", () => {
    pushAll(3);

    commit(1);
    expect(resolveCommittedDirection()).toBe("back");

    commit(2);
    expect(resolveCommittedDirection()).toBe("forward");
  });

  it("reports a multi-entry jump as back", () => {
    pushAll(4);

    commit(0);
    expect(resolveCommittedDirection()).toBe("back");
  });

  it("compares against the entry we came from, not the last one recorded", () => {
    pushAll(2);

    commit(0);
    expect(resolveCommittedDirection()).toBe("back");
    // Without advancing the tracked index on every pop, returning to idx 1
    // would compare 1 against 1 and lose the direction.
    commit(1);
    expect(resolveCommittedDirection()).toBe("forward");
  });

  it("returns null for a pop that lands on the same index", () => {
    pushAll(1);

    commit(0);
    expect(resolveCommittedDirection()).toBeNull();
  });

  it("returns null when nothing has stamped an index yet", () => {
    commit(3);
    expect(resolveCommittedDirection()).toBeNull();
  });

  it("returns null for an entry the router never indexed", () => {
    pushAll(2);

    window.history.replaceState({}, "");
    expect(resolveCommittedDirection()).toBeNull();
  });
});

describe("hasEarlierEntry", () => {
  it("reports whether stepping back one entry stays in the app", () => {
    commit(0);
    expect(hasEarlierEntry()).toBe(false);

    commit(1);
    expect(hasEarlierEntry()).toBe(true);
  });

  it("survives a reload, because the index lives in history.state", () => {
    pushAll(2);
    resetNavigationHistory();
    commit(1);

    // "Is there something behind me" is answerable from the index alone. "What
    // is behind me" is not, which is why nothing here answers it — see the note
    // in navigationHistory.ts.
    expect(hasEarlierEntry()).toBe(true);
  });

  it("is false for an entry the router never indexed", () => {
    window.history.replaceState({}, "");
    expect(hasEarlierEntry()).toBe(false);
  });
});

describe("unindexed entries", () => {
  it("degrades direction to null rather than to a wrong answer", () => {
    // React Router derives a push index as `getIndex() + 1` over a nullable
    // history state, so an entry that lands without an idx — this app's raw
    // `<a href="#main-content">` skip links do exactly that — silently restarts
    // its numbering. A path-to-index map cannot survive that: the delta stops
    // matching real stack positions and `navigate(-n)` lands somewhere the user
    // never asked for. Direction can survive it, because the honest answer to
    // an unreadable index is "no attribute, neutral motion".
    pushAll(2);

    window.history.replaceState(null, "");
    expect(resolveCommittedDirection()).toBeNull();
    expect(hasEarlierEntry()).toBe(false);
  });
});
