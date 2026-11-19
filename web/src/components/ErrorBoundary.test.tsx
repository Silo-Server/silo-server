import { lazy, Suspense, type ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ErrorBoundary } from "./ErrorBoundary";

let consoleError: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
});
afterEach(() => {
  consoleError.mockRestore();
});

function renderInBoundary(children: ReactNode) {
  render(
    <MemoryRouter>
      <ErrorBoundary>
        <Suspense fallback={null}>{children}</Suspense>
      </ErrorBoundary>
    </MemoryRouter>,
  );
}

// React keeps a failed lazy import failed, so "Try again" would only show the same screen.
it.each([
  "Failed to fetch dynamically imported module: /assets/DeviceSettings.js",
  "Unable to preload CSS for /assets/admin-settings.css",
])("offers only a reload when a route chunk fails to load (%s)", async (message) => {
  const Route = lazy(() => Promise.reject(new Error(message)));
  renderInBoundary(<Route />);

  expect(await screen.findByRole("button", { name: "Refresh Page" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
});

it("keeps Try again for an ordinary render error", async () => {
  function Broken(): never {
    throw new Error("render failed");
  }
  renderInBoundary(<Broken />);

  expect(await screen.findByRole("button", { name: "Try again" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Refresh Page" })).toBeInTheDocument();
});
