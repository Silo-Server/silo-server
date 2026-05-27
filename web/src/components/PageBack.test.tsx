import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

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

describe("PageBack", () => {
  afterEach(() => {
    mocks.navigate.mockClear();
    window.history.replaceState(null, "");
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
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith("/");
  });

  it("uses browser history when a router history entry is available", async () => {
    window.history.replaceState({ idx: 1 }, "");
    render(
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(-1);
  });

  it("uses the explicit target when history preference is disabled", async () => {
    window.history.replaceState({ idx: 1 }, "");
    render(
      <MemoryRouter>
        <PageBack to="/collections" preferHistory={false} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith("/collections");
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
      "absolute",
      "top-4",
      "left-2",
      "z-20",
      "rounded-full",
      "p-1.5",
    );
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
