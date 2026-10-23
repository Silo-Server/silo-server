import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import Collections from "./Collections";

/**
 * Renders the Collections page at /collections; a link it follows shows `elsewhere`.
 * Returns the page's query client.
 */
export function renderCollectionsPage(elsewhere: ReactNode = null) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/collections"]}>
        <Routes>
          <Route path="/collections" element={<Collections />} />
          <Route path="*" element={elsewhere} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}
