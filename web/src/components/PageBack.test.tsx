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

describe("PageBack", () => {
  afterEach(() => {
    mocks.navigate.mockClear();
    resetNavigationHistory();
    window.history.replaceState(null, "");
    delete document.documentElement.dataset.navigationDirection;
  });

  it("renders a button with the default 'Go back' aria-label", () => {
    render(
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    expect(screen.getByRole("button", { name: "Go back" })).toBeInTheDocument();
  });

  it("uses a custom label when provided", () => {
    render(
      <MemoryRouter>
        <PageBack label="Return to library" />
      </MemoryRouter>,
    );

    expect(screen.getByRole("button", { name: "Return to library" })).toBeInTheDocument();
  });

  it("falls back to the default route when there is no router history", async () => {
    render(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <PageBack />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith("/", { replace: false, viewTransition: true });
  });

  it("uses browser history when a router history entry is available", async () => {
    commit(1);
    render(
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(-1);
  });

  it("navigates to the declared ancestor when it is not behind us", async () => {
    commit(1);
    render(
      <MemoryRouter>
        <PageBack to="/collections" up />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith("/collections", {
      replace: false,
      viewTransition: true,
    });
  });

  it("moves the page backwards whichever route it takes", async () => {
    commit(1);
    render(
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(document.documentElement.dataset.navigationDirection).toBe("back");
  });

  it("applies the documented positioning and glass styling", () => {
    render(
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: "Go back" });
    expect(button).toHaveClass(
      "glass",
      "glass-hover",
      "glass-hover-accent",
      "absolute",
      "top-4",
      "left-2",
      "z-20",
      "rounded-full",
      "p-1.5",
    );
    expect(button).not.toHaveClass("hover:bg-accent");
    expect(button).not.toHaveClass("transition-colors");
  });

  it("pins to the viewport on lg+ when floating is set", () => {
    render(
      <MemoryRouter>
        <PageBack floating />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: "Go back" });
    expect(button).toHaveClass("lg:fixed", "lg:left-[268px]");
  });
});
