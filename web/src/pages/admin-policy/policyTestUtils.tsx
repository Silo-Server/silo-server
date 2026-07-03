import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";

import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";

function memoryStorage() {
  const state = new Map<string, string>();
  return {
    get length() {
      return state.size;
    },
    getItem: (key: string) => state.get(key) ?? null,
    key: (index: number) => Array.from(state.keys())[index] ?? null,
    setItem: (key: string, value: string) => {
      state.set(key, value);
    },
    removeItem: (key: string) => {
      state.delete(key);
    },
    clear: () => {
      state.clear();
    },
  } satisfies Storage;
}

export function installPolicyStorageMocks() {
  Object.defineProperty(globalThis, "localStorage", {
    value: memoryStorage(),
    configurable: true,
  });
  Object.defineProperty(globalThis, "sessionStorage", {
    value: memoryStorage(),
    configurable: true,
  });
  setAccessToken(null);
  setRefreshToken(null);
  setProfileId(null);
  setProfileToken(null);
}

export function renderWithPolicyProviders(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  const result = render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...result, client };
}

export function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
