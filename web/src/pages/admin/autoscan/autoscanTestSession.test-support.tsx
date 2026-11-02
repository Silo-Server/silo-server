import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";

import { InlineConnectionPicker } from "./InlineConnectionPicker";

const BROWSER_METHODS = [
  "hasPointerCapture",
  "setPointerCapture",
  "releasePointerCapture",
  "scrollIntoView",
] as const;

/**
 * Registers the session every autoscan admin test runs under: clean storage, a
 * synthetic admin with a PIN-proven profile, and global stubs undone after each
 * test. `pointerCapture` also stubs the element methods Radix needs in jsdom and
 * restores the originals afterwards.
 */
export function setupAutoscanAdmin({
  profileId = "a",
  profileToken = "pin-a",
  pointerCapture = false,
} = {}) {
  const originals = BROWSER_METHODS.map((name) =>
    Object.getOwnPropertyDescriptor(HTMLElement.prototype, name),
  );
  beforeEach(() => {
    if (pointerCapture) {
      vi.stubGlobal("PointerEvent", MouseEvent);
      for (const name of BROWSER_METHODS)
        Object.defineProperty(HTMLElement.prototype, name, {
          configurable: true,
          value: name === "hasPointerCapture" ? () => false : () => {},
        });
    }
    localStorage.clear();
    sessionStorage.clear();
    setAccessToken("synthetic-admin");
    setRefreshToken(null);
    setProfileId(profileId);
    setProfileToken(profileToken);
  });
  afterEach(() => {
    cleanup();
    if (pointerCapture)
      BROWSER_METHODS.forEach((name, index) => {
        const original = originals[index];
        if (original) Object.defineProperty(HTMLElement.prototype, name, original);
        else Reflect.deleteProperty(HTMLElement.prototype, name);
      });
    vi.unstubAllGlobals();
  });
}

export function renderWithClient(component: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  return {
    client,
    ...render(<QueryClientProvider client={client}>{component}</QueryClientProvider>),
  };
}

/** Stubs fetch with one JSON response whose body arrives only on `finish()`. */
export function deferredJSON(body: unknown, status = 200) {
  let release!: (s: string) => void;
  const res = new Response(null, { status, headers: { "Content-Type": "application/json" } });
  res.text = () =>
    new Promise((resolve) => {
      release = resolve;
    });
  const fetchMock = vi.fn().mockResolvedValue(res);
  vi.stubGlobal("fetch", fetchMock);
  return {
    fetchMock,
    ready: () => waitFor(() => expect(release).toBeTypeOf("function")),
    finish: () => act(async () => release(JSON.stringify(body))),
  };
}

export const CREATED_CONNECTION = {
  id: "created",
  name: "Synthetic",
  kind: "sonarr",
  has_api_key: true,
};

export function fillConnectionURL() {
  fireEvent.change(screen.getByPlaceholderText("http://localhost:8989"), {
    target: { value: "https://example.invalid" },
  });
}

/** Renders an empty Sonarr picker and opens its "+ Add a server…" dialog. */
export async function openInlineConnectionDialog(onChange = vi.fn()) {
  renderWithClient(
    <InlineConnectionPicker
      value=""
      onChange={onChange}
      options={[]}
      required={false}
      connectionKinds={["sonarr"]}
    />,
  );
  fireEvent.keyDown(screen.getByRole("combobox"), { key: "ArrowDown" });
  fireEvent.click(await screen.findByRole("option", { name: "+ Add a server…" }));
}
