import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  BrowserRouter,
  MemoryRouter,
  Route,
  Routes,
  useLocation,
  type InitialEntry,
} from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import NavigationTransitionProvider from "./NavigationTransitionProvider";
import PageBack from "./PageBack";
import ViewTransitionLink from "./ViewTransitionLink";
import { isExpectedNavigationCommit, pruneNavigationHistory } from "@/lib/backNavigation";
import { DETAIL_MAIN_STAGE_MOTION_ATTRIBUTE } from "./sidebarItemNavigation";

const originalStartViewTransition = Object.getOwnPropertyDescriptor(
  document,
  "startViewTransition",
);
const originalElementAnimate = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "animate");

function LocationOutput() {
  const location = useLocation();
  return <output aria-label="location">{location.pathname}</output>;
}

function transitionResult(finished: Promise<void>) {
  return {
    finished,
    ready: Promise.resolve(),
    updateCallbackDone: finished,
    skipTransition: vi.fn(),
  };
}

function installViewTransitionMock() {
  const pathsWhenUpdatesResolve: string[] = [];
  const directionsWhenUpdatesResolve: string[] = [];
  const updates: Promise<void>[] = [];
  const startViewTransition = vi.fn((update: () => void | Promise<void>) => {
    const finished = Promise.resolve()
      .then(update)
      .then(() => {
        directionsWhenUpdatesResolve.push(
          document.documentElement.dataset.navigationDirection ?? "",
        );
        pathsWhenUpdatesResolve.push(
          screen.getByRole("status", { name: "location" }).textContent ?? "",
        );
      });
    updates.push(finished);
    return transitionResult(finished);
  });

  Object.defineProperty(document, "startViewTransition", {
    configurable: true,
    value: startViewTransition,
  });

  return { directionsWhenUpdatesResolve, pathsWhenUpdatesResolve, startViewTransition, updates };
}

function installMainStageAnimationMock() {
  const cancel = vi.fn();
  const pause = vi.fn();
  const play = vi.fn();
  const animationState = { playState: "running" };
  const animation = {
    cancel,
    currentTime: null as number | null,
    finished: new Promise<Animation>(() => undefined),
    pause: () => {
      animationState.playState = "paused";
      pause();
    },
    play: () => {
      animationState.playState = "running";
      play();
    },
    get playState() {
      return animationState.playState;
    },
  } as unknown as Animation;
  const animate = vi.fn(() => animation);

  Object.defineProperty(HTMLElement.prototype, "animate", {
    configurable: true,
    value: animate,
  });

  return { animate, cancel, pause, play };
}

function renderRoutes(initialEntries: InitialEntry[]) {
  return render(
    <MemoryRouter initialEntries={initialEntries} initialIndex={initialEntries.length - 1}>
      <NavigationTransitionProvider>
        <div className="sidebar-main-stage" />
        <LocationOutput />
        <Routes>
          <Route
            path="/"
            element={
              <div>
                <ViewTransitionLink to="/item/series-1">Open series</ViewTransitionLink>
                <ViewTransitionLink to="/item/movie-1">Open movie</ViewTransitionLink>
              </div>
            }
          />
          <Route path="/item/movie-1" element={<PageBack />} />
          <Route
            path="/item/series-1"
            element={
              <div>
                <PageBack />
                <ViewTransitionLink to="/item/series-1">Current series</ViewTransitionLink>
                <ViewTransitionLink
                  to="/item/series-1"
                  state={{ parentSeriesHref: "/item/series-1" }}
                >
                  Current series with state
                </ViewTransitionLink>
                <ViewTransitionLink
                  to="/item/season-1"
                  state={{ parentSeriesHref: "/item/series-1" }}
                >
                  Season 1
                </ViewTransitionLink>
              </div>
            }
          />
          <Route
            path="/item/season-1"
            element={
              <div>
                <PageBack to="/item/series-1" preferHistory viewTransition replace />
                <ViewTransitionLink
                  to="/item/episode-1"
                  state={{ parentSeasonHref: "/item/season-1" }}
                >
                  Episode 1
                </ViewTransitionLink>
              </div>
            }
          />
          <Route
            path="/item/episode-1"
            element={<PageBack to="/item/season-1" preferHistory viewTransition replace />}
          />
        </Routes>
      </NavigationTransitionProvider>
    </MemoryRouter>,
  );
}

function renderBrowserRoutes() {
  return render(
    <BrowserRouter>
      <NavigationTransitionProvider>
        <LocationOutput />
        <Routes>
          <Route
            path="/item/movie-a"
            element={
              <div>
                <PageBack />
                <ViewTransitionLink to="/item/movie-b">Recommended movie</ViewTransitionLink>
                <ViewTransitionLink to="/person/1">Open actor</ViewTransitionLink>
              </div>
            }
          />
          <Route path="/item/movie-b" element={<PageBack />} />
          <Route path="/person/1" element={<PageBack />} />
        </Routes>
      </NavigationTransitionProvider>
    </BrowserRouter>,
  );
}

function renderBrowserHierarchyRoutes() {
  return render(
    <BrowserRouter>
      <NavigationTransitionProvider>
        <LocationOutput />
        <Routes>
          <Route
            path="/item/series-1"
            element={
              <div>
                <PageBack />
                <ViewTransitionLink
                  to="/item/season-1"
                  state={{ parentSeriesHref: "/item/series-1" }}
                >
                  Season 1
                </ViewTransitionLink>
              </div>
            }
          />
          <Route
            path="/item/season-1"
            element={
              <div>
                <PageBack to="/item/series-1" preferHistory viewTransition replace />
                <ViewTransitionLink
                  to="/item/series-1"
                  replace
                  preferHistory
                  transitionDirection="back"
                >
                  Series breadcrumb
                </ViewTransitionLink>
                <ViewTransitionLink
                  to="/item/episode-1"
                  state={{ parentSeasonHref: "/item/season-1" }}
                >
                  Episode 1
                </ViewTransitionLink>
              </div>
            }
          />
          <Route
            path="/item/episode-1"
            element={
              <div>
                <PageBack to="/item/season-1" preferHistory viewTransition replace />
                <ViewTransitionLink
                  to="/item/series-1"
                  replace
                  preferHistory
                  transitionDirection="back"
                >
                  Series breadcrumb
                </ViewTransitionLink>
                <ViewTransitionLink
                  to="/item/season-1"
                  replace
                  preferHistory
                  transitionDirection="back"
                >
                  Season breadcrumb
                </ViewTransitionLink>
              </div>
            }
          />
        </Routes>
      </NavigationTransitionProvider>
    </BrowserRouter>,
  );
}

afterEach(() => {
  if (originalStartViewTransition) {
    Object.defineProperty(document, "startViewTransition", originalStartViewTransition);
  } else {
    Reflect.deleteProperty(document, "startViewTransition");
  }
  if (originalElementAnimate) {
    Object.defineProperty(HTMLElement.prototype, "animate", originalElementAnimate);
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, "animate");
  }
  window.history.replaceState(null, "", "/");
  window.sessionStorage.removeItem("silo:navigation-history-paths:v1");
  document.documentElement.removeAttribute(DETAIL_MAIN_STAGE_MOTION_ATTRIBUTE);
  vi.unstubAllGlobals();
});

describe("NavigationTransitionProvider", () => {
  it("does not let a stale delayed destination resolve the latest transition barrier", () => {
    const sourceKey = "location-a";

    expect(isExpectedNavigationCommit("/item/c", 2, 2, sourceKey, "location-b", "/item/b")).toBe(
      false,
    );
    expect(isExpectedNavigationCommit("/item/c", 2, 2, sourceKey, "location-c", "/item/c")).toBe(
      true,
    );
  });

  it("bounds long-running history around a deeply backed-to entry", () => {
    const paths = new Map<number, string>();
    for (let index = 0; index <= 220; index += 1) {
      paths.set(index, `/item/${index}`);
    }

    pruneNavigationHistory(paths, 220);
    expect(paths.size).toBe(128);
    expect(paths.get(93)).toBe("/item/93");
    expect(paths.has(92)).toBe(false);

    // A 120-entry Back jump remains inside the bounded window. After the
    // destination commits (and could be reloaded), its immediate Back target
    // is still retained while discarded forward entries are gone.
    pruneNavigationHistory(paths, 100);
    expect(paths.get(99)).toBe("/item/99");
    expect(paths.get(100)).toBe("/item/100");
    expect(paths.has(101)).toBe(false);
  });

  it("keeps a Series to Season update pending until the Season location commits", async () => {
    const transition = installViewTransitionMock();
    renderRoutes(["/item/series-1"]);

    await userEvent.click(screen.getByRole("link", { name: "Season 1" }));
    await waitFor(() =>
      expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/season-1"),
    );
    await transition.updates[0];

    expect(transition.startViewTransition).toHaveBeenCalledOnce();
    expect(transition.pathsWhenUpdatesResolve).toEqual(["/item/season-1"]);
    expect(transition.directionsWhenUpdatesResolve).toEqual(["forward"]);
  });

  it("matches Home's desktop compositor motion when opening and backing out of a Season", async () => {
    const animationFrames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      animationFrames.push(callback);
      return animationFrames.length;
    });
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    window.history.replaceState({ idx: 1 }, "");
    const viewTransition = installViewTransitionMock();
    const mainStage = installMainStageAnimationMock();
    renderRoutes(["/item/series-1"]);

    await userEvent.click(screen.getByRole("link", { name: "Season 1" }));
    await waitFor(() => expect(mainStage.animate).toHaveBeenCalledTimes(1));

    expect(viewTransition.startViewTransition).not.toHaveBeenCalled();
    expect(document.documentElement).toHaveAttribute(DETAIL_MAIN_STAGE_MOTION_ATTRIBUTE);
    expect(mainStage.pause).toHaveBeenCalledOnce();
    expect(mainStage.animate).toHaveBeenNthCalledWith(
      1,
      [{ transform: "translateX(196px)" }, { transform: "translateX(0)" }],
      {
        duration: 300,
        easing: "cubic-bezier(0.32, 0.72, 0, 1)",
        fill: "both",
      },
    );
    animationFrames.shift()?.(16);
    animationFrames.shift()?.(32);
    animationFrames.shift()?.(48);
    expect(mainStage.play).toHaveBeenCalledOnce();

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    await waitFor(() => expect(mainStage.animate).toHaveBeenCalledTimes(2));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
    expect(mainStage.animate).toHaveBeenNthCalledWith(
      2,
      [{ transform: "translateX(-196px)" }, { transform: "translateX(0)" }],
      {
        duration: 300,
        easing: "cubic-bezier(0.32, 0.72, 0, 1)",
        fill: "both",
      },
    );
    expect(mainStage.cancel).toHaveBeenCalled();
  });

  it("awaits the committed Series page on Season back and preserves the next back to Home", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    window.history.replaceState({ idx: 2 }, "");
    const transition = installViewTransitionMock();
    renderRoutes([
      "/",
      "/item/series-1",
      {
        pathname: "/item/season-1",
        state: { parentSeriesHref: "/item/series-1" },
      },
    ]);

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    await transition.updates[0];

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
    expect(transition.pathsWhenUpdatesResolve[0]).toBe("/item/series-1");
    expect(transition.directionsWhenUpdatesResolve[0]).toBe("back");

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/");
    expect(transition.startViewTransition).toHaveBeenCalledOnce();
    expect(transition.pathsWhenUpdatesResolve).toEqual(["/item/series-1"]);
  });

  it("keeps forward and return motion aligned through Home, Series, Season, and Episode", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    window.history.replaceState({ idx: 6 }, "");
    const transition = installViewTransitionMock();
    renderRoutes(["/"]);

    await userEvent.click(screen.getByRole("link", { name: "Open series" }));
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
    expect(transition.startViewTransition).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("link", { name: "Season 1" }));
    await transition.updates[0];
    await userEvent.click(screen.getByRole("link", { name: "Episode 1" }));
    await transition.updates[1];

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    await transition.updates[2];
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/season-1");

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    await transition.updates[3];
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/");

    expect(transition.pathsWhenUpdatesResolve).toEqual([
      "/item/season-1",
      "/item/episode-1",
      "/item/season-1",
      "/item/series-1",
    ]);
    expect(transition.directionsWhenUpdatesResolve).toEqual(["forward", "forward", "back", "back"]);
    expect(transition.startViewTransition).toHaveBeenCalledTimes(4);
  });

  it("navigates immediately when the View Transitions API is unavailable", async () => {
    Reflect.deleteProperty(document, "startViewTransition");
    renderRoutes(["/item/series-1"]);

    await userEvent.click(screen.getByRole("link", { name: "Season 1" }));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/season-1");
  });

  it("does not start or hold a transition for the current location", async () => {
    const transition = installViewTransitionMock();
    renderRoutes(["/item/series-1"]);

    await userEvent.click(screen.getByRole("link", { name: "Current series" }));

    expect(transition.startViewTransition).not.toHaveBeenCalled();
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
  });

  it("does not push the current location again when a link supplies navigation state", async () => {
    const transition = installViewTransitionMock();
    renderRoutes(["/item/series-1"]);

    await userEvent.click(screen.getByRole("link", { name: "Current series with state" }));

    expect(transition.startViewTransition).not.toHaveBeenCalled();
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
  });

  it("uses the tracked history destination when returning between two items", async () => {
    window.history.replaceState({ idx: 0, key: "initial", usr: null }, "", "/item/movie-a");
    const transition = installViewTransitionMock();
    renderBrowserRoutes();

    await userEvent.click(screen.getByRole("link", { name: "Recommended movie" }));
    await transition.updates[0];
    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    await transition.updates[1];

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/movie-a");
    expect(transition.pathsWhenUpdatesResolve).toEqual(["/item/movie-b", "/item/movie-a"]);
    expect(transition.directionsWhenUpdatesResolve).toEqual(["forward", "back"]);
  });

  it("reuses the real Series entry for a parent breadcrumb instead of pushing a duplicate", async () => {
    window.history.replaceState({ idx: 0, key: "initial", usr: null }, "", "/item/series-1");
    const transition = installViewTransitionMock();
    renderBrowserHierarchyRoutes();

    await userEvent.click(screen.getByRole("link", { name: "Season 1" }));
    await transition.updates[0];
    await userEvent.click(screen.getByRole("link", { name: "Episode 1" }));
    await transition.updates[1];
    await userEvent.click(screen.getByRole("link", { name: "Series breadcrumb" }));
    await transition.updates[2];

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
    expect((window.history.state as { idx?: number }).idx).toBe(0);
    expect(transition.directionsWhenUpdatesResolve).toEqual(["forward", "forward", "back"]);
  });

  it("restores item history after a reload so Back keeps the item-to-item transition", async () => {
    window.history.replaceState({ idx: 0, key: "initial", usr: null }, "", "/item/movie-a");
    const transition = installViewTransitionMock();
    const firstRender = renderBrowserRoutes();

    await userEvent.click(screen.getByRole("link", { name: "Recommended movie" }));
    await transition.updates[0];
    firstRender.unmount();

    renderBrowserRoutes();
    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    await transition.updates[1];

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/movie-a");
    expect(transition.pathsWhenUpdatesResolve).toEqual(["/item/movie-b", "/item/movie-a"]);
    expect(transition.directionsWhenUpdatesResolve).toEqual(["forward", "back"]);
  });

  it("restores breadcrumb provenance after a reload without duplicating the Series entry", async () => {
    window.history.replaceState({ idx: 0, key: "initial", usr: null }, "", "/item/series-1");
    const transition = installViewTransitionMock();
    const firstRender = renderBrowserHierarchyRoutes();

    await userEvent.click(screen.getByRole("link", { name: "Season 1" }));
    await transition.updates[0];
    firstRender.unmount();

    renderBrowserHierarchyRoutes();
    await userEvent.click(screen.getByRole("link", { name: "Series breadcrumb" }));
    await transition.updates[1];

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
    expect((window.history.state as { idx?: number }).idx).toBe(0);
    expect(transition.directionsWhenUpdatesResolve).toEqual(["forward", "back"]);
  });

  it("preserves the live sidebar boundary when returning from a person to an item", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    window.history.replaceState({ idx: 0, key: "initial", usr: null }, "", "/item/movie-a");
    const transition = installViewTransitionMock();
    renderBrowserRoutes();

    await userEvent.click(screen.getByRole("link", { name: "Open actor" }));
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/person/1");
    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    await waitFor(() =>
      expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/movie-a"),
    );
    expect(transition.startViewTransition).not.toHaveBeenCalled();
  });

  it("restores the live person-to-item sidebar boundary after a reload", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    window.history.replaceState({ idx: 0, key: "initial", usr: null }, "", "/item/movie-a");
    const transition = installViewTransitionMock();
    const firstRender = renderBrowserRoutes();

    await userEvent.click(screen.getByRole("link", { name: "Open actor" }));
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/person/1");
    firstRender.unmount();

    renderBrowserRoutes();
    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    await waitFor(() =>
      expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/movie-a"),
    );

    expect(transition.startViewTransition).not.toHaveBeenCalled();
  });

  it("preserves the live desktop sidebar motion when entering an item from Home", async () => {
    const transition = installViewTransitionMock();
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    renderRoutes(["/"]);

    await userEvent.click(screen.getByRole("link", { name: "Open series" }));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
    expect(transition.startViewTransition).not.toHaveBeenCalled();
  });

  it("uses the same live desktop flow from Home to a Movie and back", async () => {
    const transition = installViewTransitionMock();
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    window.history.replaceState({ idx: 1 }, "");
    renderRoutes(["/"]);

    await userEvent.click(screen.getByRole("link", { name: "Open movie" }));
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/movie-1");

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/");
    expect(transition.startViewTransition).not.toHaveBeenCalled();
  });

  it("leaves modified clicks to the browser without starting an app transition", () => {
    const transition = installViewTransitionMock();
    renderRoutes(["/item/series-1"]);

    fireEvent.click(screen.getByRole("link", { name: "Season 1" }), { metaKey: true });

    expect(transition.startViewTransition).not.toHaveBeenCalled();
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series-1");
  });

  it("navigates without a transition when reduced motion is requested", async () => {
    const transition = installViewTransitionMock();
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(prefers-reduced-motion: reduce)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    renderRoutes(["/item/series-1"]);

    await userEvent.click(screen.getByRole("link", { name: "Season 1" }));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/season-1");
    expect(transition.startViewTransition).not.toHaveBeenCalled();
  });
});
