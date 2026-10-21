import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import Collections from "./Collections";

/** Renders the Collections page at /collections; a link it follows shows `elsewhere`. */
export function renderCollectionsPage(elsewhere: ReactNode = null) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
      }
    >
      <MemoryRouter initialEntries={["/collections"]}>
        <Routes>
          <Route path="/collections" element={<Collections />} />
          <Route path="*" element={elsewhere} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
