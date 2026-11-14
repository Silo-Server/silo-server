import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { moveLinkTokenOutOfAddress } from "@/lib/linkToken";

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
 * Opens a password reset or invitation link as a page load does: its token
 * leaves the address before anything renders.
 */
export function openLink(url: string) {
  window.history.replaceState(null, "", url);
  moveLinkTokenOutOfAddress();
}

/** The JSON bodies of the requests a fetch mock saw whose URL ends with `suffix`. */
export function bodiesSentTo(fetchMock: { mock: { calls: unknown[][] } }, suffix: string) {
  return fetchMock.mock.calls
    .filter(([url]) => String(url).endsWith(suffix))
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)) as Record<string, unknown>);
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
