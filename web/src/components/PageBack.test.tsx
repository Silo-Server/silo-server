import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { resetNavigationHistory } from "@/lib/navigationHistory";

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

import PageBack from "./PageBack";

/** Stands in for the browser having committed the entry at `idx`. */
function commit(idx: number) {
  window.history.replaceState({ idx }, "");
}

async function clickBack(back: ReactNode, initialEntries?: string[]) {
  render(<MemoryRouter initialEntries={initialEntries}>{back}</MemoryRouter>);
  await userEvent.click(screen.getByRole("button", { name: "Go back" }));
}

describe("PageBack", () => {
  afterEach(() => {
    mocks.navigate.mockClear();
    resetNavigationHistory();
    window.history.replaceState(null, "");
    delete document.documentElement.dataset.navigationDirection;
    vi.unstubAllGlobals();
  });

  it("falls back to the default route when there is no router history", async () => {
    await clickBack(<PageBack />, ["/item/movie-1"]);

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith("/", { replace: false, viewTransition: true });
  });

  it("uses the snapshot-free fallback when leaving an item on desktop", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
    }));
    await clickBack(<PageBack />, ["/item/movie-1"]);

    expect(mocks.navigate).toHaveBeenCalledWith("/", {
      replace: false,
      viewTransition: false,
    });
  });

  it("uses browser history when a router history entry is available", async () => {
    commit(1);
    await clickBack(<PageBack />);

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(-1);
  });

  it("navigates to the declared ancestor when it is not behind us", async () => {
    commit(1);
    await clickBack(<PageBack to="/collections" up />);

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith("/collections", {
      replace: false,
      viewTransition: true,
    });
  });
});
