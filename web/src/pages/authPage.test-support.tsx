import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { onTestFinished } from "vitest";
import { MemoryRouter, Route, Routes, RouterProvider, createBrowserRouter } from "react-router";

/** Renders `routes` (path to element) at `entry`, with `outside` above the routes. */
export function renderRoutes(
  entry: string,
  routes: Record<string, ReactNode>,
  outside?: ReactNode,
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        {outside}
        <Routes>
          {Object.entries(routes).map(([path, element]) => (
            <Route key={path} path={path} element={element} />
          ))}
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/**
 * Renders `routes` on the window's real history, so a test can read what the
 * address bar holds. Pass `entry` to open that address first; omit it to
 * reload the current entry, history state included.
 */
export function renderBrowserRoutes(entry: string | undefined, routes: Record<string, ReactNode>) {
  if (entry !== undefined) window.history.replaceState(null, "", entry);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createBrowserRouter(
    Object.entries(routes).map(([path, element]) => ({ path, element })),
  );
  // A browser router listens to the window until disposed.
  onTestFinished(() => router.dispose());
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

export function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** A v2 problem+json response of `type`, with any other problem fields in `fields`. */
export function problemResponse(
  status: number,
  type: string,
  fields: Record<string, unknown> = {},
) {
  return new Response(
    JSON.stringify({
      type: `https://siloserver.org/docs/api/v2/problems/${type}`,
      title: type,
      status,
      ...fields,
    }),
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}
