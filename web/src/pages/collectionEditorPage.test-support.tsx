/* eslint-disable react-refresh/only-export-components -- test helpers, never hot-reloaded. */
/**
 * Shared setup for the collection editor page tests that answer requests
 * through `@/test/v2Recorder`. Each test file still declares its own
 * `vi.mock` lines and calls `installV2Recorder()`.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider, useLocation } from "react-router";
import { vi } from "vitest";

import { adminCapabilities, personalCapabilities } from "@/test/fixtures/collectionAnswers";
import { v2Recorder } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

/**
 * Stubs what Radix menus and selects and the artwork picker need under jsdom,
 * and answers both scopes' collection capabilities. Call it in `beforeEach`.
 */
export function prepareEditorPage() {
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
  URL.createObjectURL = () => "blob:artwork";
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
}

/** Shows the current address, for tests that follow the editor off the page. */
function Where() {
  const location = useLocation();
  return <output aria-label="Location">{`${location.pathname}${location.search}`}</output>;
}

export const location = () => screen.getByRole("status", { name: "Location" }).textContent ?? "";
export const locationParams = () => new URL(location(), "https://silo.test").searchParams;

type Entry = string | { pathname: string; search?: string; state?: unknown };

/**
 * Renders the editor page for both scopes in a data router (the page guards
 * unsaved changes with `useBlocker`), beside the pages it leaves for. With
 * `where`, those pages, Home rows and Settings > Home Screen show the address
 * instead (see `location`).
 */
export function showEditorPage(entry: Entry, { where = false } = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const rowPages = where
    ? [
        { path: "/admin/sections", element: <Where /> },
        { path: "/settings/home-screen", element: <Where /> },
      ]
    : [];
  const router = createMemoryRouter(
    [
      { path: "/admin/collections", element: where ? <Where /> : <p>Admin collections page</p> },
      {
        element: <CollectionEditorPage scope="server" />,
        children: [{ path: "/admin/collections/new" }, { path: "/admin/collections/:id/edit" }],
      },
      { path: "/collections", element: where ? <Where /> : <p>Collections page</p> },
      {
        element: <CollectionEditorPage scope="personal" />,
        children: [{ path: "/collections/new" }, { path: "/collections/:id/edit" }],
      },
      { path: "/catalog", element: <p>Collection page</p> },
      ...rowPages,
    ],
    { initialEntries: [entry] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

export const nameField = () => screen.findByRole("textbox", { name: "Name" });
export const saveBar = () => screen.getByRole("region", { name: "Unsaved changes" });

/** Opens a Radix select and picks an option. */
export function choose(combobox: HTMLElement, option: string) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  fireEvent.click(screen.getByRole("option", { name: option }));
}
