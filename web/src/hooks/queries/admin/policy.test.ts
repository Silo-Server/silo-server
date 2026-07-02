// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { useSimulatePolicy } from "./policy";

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

describe("policy admin hooks", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        expect(String(input)).toBe("/api/v1/admin/policy/simulate");
        expect(JSON.parse(String(init?.body))).toMatchObject({
          domain: "scope",
          input: { schema_version: 1 },
        });
        return jsonResponse({
          decision: { schema_version: 1, unrestricted: true },
          eval_time_ns: 5000,
          generation: 3,
        });
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("posts simulation requests through the shared API client", async () => {
    const { result } = renderHook(() => useSimulatePolicy(), { wrapper: createWrapper() });
    let response: Awaited<ReturnType<typeof result.current.mutateAsync>> | undefined;

    await act(async () => {
      response = await result.current.mutateAsync({
        domain: "scope",
        source: "package silo_custom.scope",
        input: { schema_version: 1 },
      });
    });

    expect(response).toMatchObject({
      eval_time_ns: 5000,
      generation: 3,
    });
  });
});
