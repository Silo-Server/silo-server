import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

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

  it("calls navigate(-1) on click", async () => {
    mocks.navigate.mockClear();
    render(
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(-1);
  });

  it("applies the documented positioning and glass-subtle styling", () => {
    render(
      <MemoryRouter>
        <PageBack />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: "Go back" });
    expect(button).toHaveClass(
      "glass-subtle",
      "absolute",
      "top-4",
      "left-4",
      "z-20",
      "rounded-full",
      "p-1.5",
    );
  });
});
