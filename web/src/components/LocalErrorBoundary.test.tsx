import { lazy, Suspense } from "react";
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocalErrorBoundary } from "./LocalErrorBoundary";

describe("LocalErrorBoundary", () => {
  let consoleError: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
  });
  afterEach(() => {
    consoleError.mockRestore();
  });

  it("renders its children while they work", () => {
    render(
      <LocalErrorBoundary fallback={<p>fallback</p>}>
        <p>content</p>
      </LocalErrorBoundary>,
    );

    expect(screen.getByText("content")).toBeInTheDocument();
    expect(screen.queryByText("fallback")).not.toBeInTheDocument();
  });

  it("contains a failed lazy import and reports it once", async () => {
    const failure = new Error("Failed to fetch dynamically imported module");
    const Broken = lazy(() => Promise.reject(failure));
    const onError = vi.fn();

    render(
      <main>
        <p>page</p>
        <LocalErrorBoundary
          onError={onError}
          fallback={(error) => <p>{error instanceof Error ? error.message : "failed"}</p>}
        >
          <Suspense fallback={null}>
            <Broken />
          </Suspense>
        </LocalErrorBoundary>
      </main>,
    );

    expect(await screen.findByText(failure.message)).toBeInTheDocument();
    expect(screen.getByText("page")).toBeInTheDocument();
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledWith(failure);
  });

  it("renders nothing in place of failed children by default", async () => {
    const Broken = lazy(() => Promise.reject(new Error("chunk failed")));
    const onError = vi.fn();

    const { container } = render(
      <LocalErrorBoundary onError={onError}>
        <Suspense fallback={null}>
          <Broken />
        </Suspense>
      </LocalErrorBoundary>,
    );

    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));
    expect(container).toBeEmptyDOMElement();
  });
});
