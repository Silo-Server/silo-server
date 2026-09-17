import type { ReactNode } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { catalogKeys } from "@/hooks/queries/keys";
import { SIDEBAR_DETAILS_REVEAL_DEADLINE_MS } from "./sidebarItemNavigation";

const mocks = vi.hoisted(() => ({
  location: { pathname: "/", search: "", key: "home" },
  navigate: vi.fn(),
  prefetchQuery: vi.fn(),
  getQueryData: vi.fn(),
  renderSurface: true,
  beginResult: undefined as boolean | undefined,
  profile: {
    name: "Admin",
    avatar_url: "https://example.com/admin-avatar.webp",
  } as { name: string; avatar_url?: string },
}));

let browserUserAgent =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/151.0.0.0 Safari/537.36";

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return {
    ...actual,
    useLocation: () => mocks.location,
    useNavigate: () => mocks.navigate,
  };
});

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQueryClient: () => ({
      prefetchQuery: mocks.prefetchQuery,
      getQueryData: mocks.getQueryData,
    }),
  };
});

vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { username: "Admin" } }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: mocks.profile }),
}));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => false }));
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ isBackgroundBarVisible: false }),
}));
vi.mock("@/pages/audiobooks/player/audiobookPlaybackContext", () => ({
  useAudiobookPlaybackController: () => null,
}));
vi.mock("@/hooks/queries/catalogRead", () => ({ fetchCatalogItemDetail: vi.fn() }));
vi.mock("@/components/GlobalSearch", () => ({ GlobalSearch: () => null }));
vi.mock("@/components/ServerActivity", () => ({ default: () => null }));
vi.mock("@/components/SiloBrand", () => ({ SiloBrand: () => <span>Silo</span> }));
vi.mock("@/components/ui/avatar", () => ({
  Avatar: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AvatarImage: ({ src, alt }: { src: string; alt: string }) => <img src={src} alt={alt} />,
  AvatarFallback: ({ children }: { children: ReactNode }) => <span>{children}</span>,
}));
vi.mock("@/components/ViewTransitionLink", () => ({
  default: ({ children }: { children: ReactNode }) => <a href="/">{children}</a>,
}));
vi.mock("@/components/ui/sheet", () => ({
  Sheet: () => null,
  SheetContent: ({ children }: { children: ReactNode }) => <>{children}</>,
  SheetHeader: ({ children }: { children: ReactNode }) => <>{children}</>,
  SheetTitle: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/AppSidebar", () => ({
  default: ({ collapsed }: { collapsed?: boolean }) =>
    mocks.renderSurface ? (
      <aside
        className="sidebar-surface"
        data-collapsed={collapsed ? "true" : undefined}
        data-testid="sidebar-surface"
      >
        <div className="sidebar-inner" data-testid="sidebar-inner" />
        <div className="sidebar-row-shift" data-testid="sidebar-row" />
      </aside>
    ) : null,
}));

import Layout from "./Layout";
import {
  useSidebarItemDetailsReady,
  useSidebarItemEnteredFromHome,
  useSidebarItemNavigation,
} from "./sidebarItemNavigationContext";

function Harness() {
  const begin = useSidebarItemNavigation();
  const ready = useSidebarItemDetailsReady();
  const enteredFromHome = useSidebarItemEnteredFromHome();
  return (
    <>
      <output aria-label="details-ready">{String(ready)}</output>
      <output aria-label="entered-from-home">{String(enteredFromHome)}</output>
      <button
        onClick={() => {
          mocks.beginResult = begin?.({
            href: "/item/movie-1?libraryId=2",
            replace: true,
            state: { source: "home" },
          });
        }}
      >
        begin item
      </button>
      <button
        onClick={() => {
          mocks.beginResult = begin?.({ href: "/library/1" });
        }}
      >
        begin library
      </button>
    </>
  );
}

function renderLayout() {
  return render(
    <MemoryRouter>
      <Layout>
        <Harness />
      </Layout>
    </MemoryRouter>,
  );
}

function setRoute(pathname: string, key: string, search = "") {
  mocks.location = { pathname, search, key };
}

let onHeaderResize: ResizeObserverCallback;
const disconnectHeaderObserver = vi.fn();

beforeEach(() => {
  disconnectHeaderObserver.mockReset();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: ResizeObserverCallback) {
        onHeaderResize = callback;
      }
      observe = vi.fn();
      disconnect = disconnectHeaderObserver;
    },
  );
  vi.useFakeTimers();
  mocks.location = { pathname: "/", search: "", key: "home" };
  mocks.navigate.mockReset();
  mocks.prefetchQuery.mockReset();
  mocks.getQueryData.mockReset();
  mocks.renderSurface = true;
  mocks.beginResult = undefined;
  mocks.profile = {
    name: "Admin",
    avatar_url: "https://example.com/admin-avatar.webp",
  };
  browserUserAgent =
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/151.0.0.0 Safari/537.36";
  vi.spyOn(window.navigator, "userAgent", "get").mockImplementation(() => browserUserAgent);
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === "(min-width: 64rem)",
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }));
  vi.stubGlobal(
    "requestAnimationFrame",
    vi.fn(() => 1),
  );
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Layout mobile profile", () => {
  it("updates the viewport offset when the mobile header resizes or disappears", () => {
    const view = renderLayout();
    const header = view.container.querySelector<HTMLElement>(".mobile-header")!;
    const shell = header.parentElement!;
    header.style.marginTop = "15px";
    const bounds = vi.spyOn(header, "getBoundingClientRect");
    bounds.mockReturnValue({ height: 82 } as DOMRect);
    act(() => onHeaderResize([], {} as ResizeObserver));
    expect(shell.style.getPropertyValue("--detail-header-height")).toBe("97px");

    bounds.mockReturnValue({ height: 0 } as DOMRect);
    act(() => onHeaderResize([], {} as ResizeObserver));
    expect(shell.style.getPropertyValue("--detail-header-height")).toBe("0px");
    view.unmount();
    expect(disconnectHeaderObserver).toHaveBeenCalledOnce();
  });

  it("renders the current profile avatar in the settings link", () => {
    renderLayout();

    const settingsLink = screen.getByRole("link", { name: "Admin settings" });
    const avatar = screen.getByRole("img", { name: "Admin" });

    expect(settingsLink).toContainElement(avatar);
    expect(avatar).toHaveAttribute("src", "https://example.com/admin-avatar.webp");
  });

  it("marks only Home as the balanced sidebar return destination", () => {
    const view = renderLayout();
    expect(document.documentElement).toHaveAttribute("data-home-route", "true");

    setRoute("/library/1", "library");
    view.rerender(
      <MemoryRouter>
        <Layout>
          <Harness />
        </Layout>
      </MemoryRouter>,
    );

    expect(document.documentElement).not.toHaveAttribute("data-home-route");
  });
});

describe("Layout item navigation", () => {
  it("declines interception on an item route, for a non-item href, and below lg", () => {
    const view = renderLayout();
    const begin = screen.getByRole("button", { name: "begin item" });

    setRoute("/item/already-there", "item");
    view.rerender(
      <MemoryRouter>
        <Layout>
          <Harness />
        </Layout>
      </MemoryRouter>,
    );
    fireEvent.click(begin);
    expect(mocks.beginResult).toBe(false);
    expect(mocks.navigate).not.toHaveBeenCalled();

    setRoute("/", "home-2");
    view.rerender(
      <MemoryRouter>
        <Layout>
          <Harness />
        </Layout>
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "begin library" }));
    expect(mocks.beginResult).toBe(false);
    expect(mocks.navigate).not.toHaveBeenCalled();

    const contextBegin = screen.getByRole("button", { name: "begin item" });
    fireEvent.click(contextBegin);
    expect(mocks.beginResult).toBe(true);
    expect(mocks.navigate).toHaveBeenCalledOnce();

    mocks.navigate.mockReset();
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      media: "(min-width: 64rem)",
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    view.rerender(
      <MemoryRouter>
        <Layout>
          <Harness />
        </Layout>
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "begin item" }));
    expect(mocks.beginResult).toBe(false);
    expect(mocks.navigate).not.toHaveBeenCalled();
  });

  it("prefetches and forwards navigation options on a successful intercept", () => {
    renderLayout();
    fireEvent.click(screen.getByRole("button", { name: "begin item" }));

    expect(mocks.prefetchQuery).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: catalogKeys.itemDetail("movie-1", 2) }),
    );
    expect(mocks.getQueryData.mock.invocationCallOrder[0]!).toBeLessThan(
      mocks.prefetchQuery.mock.invocationCallOrder[0]!,
    );
    expect(mocks.prefetchQuery.mock.invocationCallOrder[0]!).toBeLessThan(
      mocks.navigate.mock.invocationCallOrder[0]!,
    );
    expect(mocks.navigate).toHaveBeenCalledWith("/item/movie-1?libraryId=2", {
      replace: true,
      state: { source: "home" },
      viewTransition: false,
    });
  });
});

describe("Layout detail reveal", () => {
  it("reveals immediately when the sidebar surface is absent", () => {
    mocks.renderSurface = false;
    const view = renderLayout();
    setRoute("/item/movie-1", "item");

    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );

    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
  });

  it("reveals cached details immediately when the item entry is not from Home", () => {
    mocks.getQueryData.mockImplementation((queryKey: unknown) =>
      JSON.stringify(queryKey) === JSON.stringify(catalogKeys.itemDetail("movie-1", undefined))
        ? { content_id: "movie-1" }
        : undefined,
    );
    const view = renderLayout();

    setRoute("/library/1", "library");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );

    setRoute("/item/movie-1", "item");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );

    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("renders cached Home item entries immediately without replaying their reveal animation", () => {
    mocks.getQueryData.mockImplementation((queryKey: unknown) =>
      JSON.stringify(queryKey) === JSON.stringify(catalogKeys.itemDetail("movie-1", 2))
        ? { content_id: "movie-1" }
        : undefined,
    );
    const view = renderLayout();

    const enterCachedItem = (key: string) => {
      fireEvent.click(screen.getByRole("button", { name: "begin item" }));
      setRoute("/item/movie-1", key, "?libraryId=2");
      act(() =>
        view.rerender(
          <MemoryRouter>
            <Layout>
              <Harness />
            </Layout>
          </MemoryRouter>,
        ),
      );
    };

    enterCachedItem("item-first");
    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
    expect(screen.getByRole("status", { name: "entered-from-home" })).toHaveTextContent("true");
    expect(document.documentElement).not.toHaveAttribute("data-home-item-entry");
    expect(vi.getTimerCount()).toBe(0);

    setRoute("/", "home-again");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );
    expect(document.documentElement).toHaveAttribute("data-home-item-return", "true");

    enterCachedItem("item-repeat");
    expect(document.documentElement).not.toHaveAttribute("data-home-item-return");
    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
    expect(document.documentElement).not.toHaveAttribute("data-home-item-entry");
  });

  it("does not mistake a prefetch that completes before route commit for a cache hit", () => {
    mocks.getQueryData.mockReturnValueOnce(undefined);
    mocks.prefetchQuery.mockImplementation(() => {
      mocks.getQueryData.mockReturnValue({ content_id: "movie-1" });
      return Promise.resolve();
    });
    const view = renderLayout();

    fireEvent.click(screen.getByRole("button", { name: "begin item" }));
    setRoute("/item/movie-1", "item", "?libraryId=2");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );

    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("false");
    expect(document.documentElement).toHaveAttribute("data-home-item-entry", "true");
  });

  it("reveals as soon as the surface settles", () => {
    const view = renderLayout();
    setRoute("/item/movie-1", "item");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );
    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("false");

    screen.getByTestId("sidebar-surface").setAttribute("data-collapsed", "true");
    act(() => vi.advanceTimersByTime(50));

    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
  });

  it("reveals by the deadline while hover expansion holds the surface open", () => {
    const view = renderLayout();
    setRoute("/item/movie-1", "item");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );

    act(() => vi.advanceTimersByTime(SIDEBAR_DETAILS_REVEAL_DEADLINE_MS));

    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
  });

  it("reveals by the deadline when requestAnimationFrame never fires", () => {
    const view = renderLayout();
    setRoute("/item/movie-1", "item");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );

    act(() => vi.advanceTimersByTime(SIDEBAR_DETAILS_REVEAL_DEADLINE_MS));

    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
  });

  it("cancels reveal polling on unmount", () => {
    const view = renderLayout();
    setRoute("/item/movie-1", "item");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );
    expect(vi.getTimerCount()).toBeGreaterThan(0);

    view.unmount();

    expect(vi.getTimerCount()).toBe(0);
  });

  it("reveals only for a collapsed surface transform transition", () => {
    const view = renderLayout();
    setRoute("/item/movie-1", "item");
    act(() =>
      view.rerender(
        <MemoryRouter>
          <Layout>
            <Harness />
          </Layout>
        </MemoryRouter>,
      ),
    );
    const surface = screen.getByTestId("sidebar-surface");
    surface.setAttribute("data-collapsed", "true");

    fireEvent.transitionEnd(screen.getByTestId("sidebar-inner"), { propertyName: "transform" });
    fireEvent.transitionEnd(screen.getByTestId("sidebar-row"), { propertyName: "transform" });
    fireEvent.transitionEnd(surface, { propertyName: "opacity" });
    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("false");

    fireEvent.transitionEnd(surface, { propertyName: "transform" });
    expect(screen.getByRole("status", { name: "details-ready" })).toHaveTextContent("true");
  });
});
