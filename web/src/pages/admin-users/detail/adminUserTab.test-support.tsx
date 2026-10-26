import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { vi } from "vitest";

/** Stubs what Radix Select and DropdownMenu need to open under jsdom. */
export function stubRadixDom() {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

/** Answers every fetch with `route(url)` as JSON and records the URL in `requests`. */
export function serveAdminAPI(route: (url: URL) => unknown, requests: URL[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input) => {
      const url = new URL(String(input), "http://localhost");
      requests.push(url);
      return json(route(url));
    }),
  );
}

/** Renders a user-detail tab under a fresh query client at `entry`. */
export function renderAdminUserTab(tab: ReactNode, entry: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>{tab}</MemoryRouter>
    </QueryClientProvider>,
  );
}
