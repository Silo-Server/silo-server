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

import PageUnavailable from "./PageUnavailable";

function renderPage(props: Partial<Parameters<typeof PageUnavailable>[0]> = {}) {
  return render(
    <MemoryRouter initialEntries={["/item/movie-1"]}>
      <PageUnavailable title="This item isn't available" description="It may be gone." {...props} />
    </MemoryRouter>,
  );
}

describe("PageUnavailable", () => {
  afterEach(() => {
    mocks.navigate.mockClear();
    resetNavigationHistory();
    window.history.replaceState(null, "");
    delete document.documentElement.dataset.navigationDirection;
  });

  it("names the page and always offers a way home", () => {
    renderPage();

    expect(
      screen.getByRole("heading", { level: 1, name: "This item isn't available" }),
    ).toBeInTheDocument();
    expect(screen.getByText("It may be gone.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go home" })).toHaveAttribute("href", "/");
  });

  it("leaves Back out on a cold entry, where there is nothing behind the page", () => {
    renderPage();

    expect(screen.queryByRole("button", { name: "Back" })).not.toBeInTheDocument();
  });

  it("steps back one entry when the app has history behind the page", async () => {
    window.history.replaceState({ idx: 2 }, "");
    renderPage();

    await userEvent.click(screen.getByRole("button", { name: "Back" }));

    expect(mocks.navigate).toHaveBeenCalledWith(-1);
  });

  it("places page-specific actions alongside the navigation", () => {
    renderPage({ children: <a href="/library/4">Browse library</a> });

    expect(screen.getByRole("link", { name: "Browse library" })).toHaveAttribute(
      "href",
      "/library/4",
    );
  });

  it("offers Try again for a failed read and holds it while the retry runs", async () => {
    const onRetry = vi.fn();
    const { rerender } = renderPage({ onRetry });

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledTimes(1);

    rerender(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <PageUnavailable
          title="Couldn't load"
          description="Try later."
          onRetry={onRetry}
          retrying
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole("button", { name: "Try again" })).toBeDisabled();
  });
});
