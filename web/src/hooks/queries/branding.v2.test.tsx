// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { useContext, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { BrandingContext, BrandingProvider } from "@/contexts/BrandingProvider";
import { useAdminPublicCss } from "./theme";
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
it("loads v2 branding and CSS before login and follows versioned v2 image URLs", async () => {
  const fetchMock = vi.fn<typeof fetch>(async (input) => {
    if (String(input) === "/api/v2/theme/branding")
      return jsonResponse({
        server_name: "Fixture",
        login_subtitle: "Welcome",
        storage_available: true,
        favicon_url: "/api/v2/branding/assets/favicon?v=ref.png",
      });
    if (String(input) === "/api/v2/theme/admin-css")
      return jsonResponse({ vars: '{"--primary":"red"}', raw_css: "body { color: red; }" });
    throw new Error("Unexpected request: " + String(input));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({ branding: useContext(BrandingContext), css: useAdminPublicCss() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.branding.serverName).toBe("Fixture"));
  await waitFor(() => expect(result.current.css.data?.rawCss).toBe("body { color: red; }"));
  expect(result.current.css.data?.vars).toEqual({ "--primary": "red" });
  expect(document.querySelector('link[rel="icon"]')?.getAttribute("href")).toBe(
    "/api/v2/branding/assets/favicon?v=ref.png",
  );
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
