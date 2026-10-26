// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { useContext, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { BrandingContext, BrandingProvider } from "@/contexts/BrandingProvider";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { setAccessToken, setRefreshToken } from "@/api/client";

beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken(null);
  setRefreshToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <BrandingProvider>{children}</BrandingProvider>
    </QueryClientProvider>
  );
}

it("paints no accent when the stored value is not a hex color", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    jsonResponse({ server_name: "Fixture", accent_color: "red;} body{display:none" }),
  );
  vi.stubGlobal("fetch", fetchMock);
  document.getElementById("silo-accent")?.remove();
  const { result } = renderHook(() => useContext(BrandingContext), { wrapper });
  await waitFor(() => expect(result.current.serverName).toBe("Fixture"));
  expect(document.getElementById("silo-accent")).toBeNull();
});
