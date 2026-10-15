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
it("loads v2 branding before login and follows versioned v2 image URLs", async () => {
  const fetchMock = vi.fn<typeof fetch>(async (input) => {
    if (String(input) === "/api/v2/theme/branding")
      return jsonResponse({
        server_name: "Fixture",
        login_subtitle: "Welcome",
        accent_color: "#10b981",
        storage_available: true,
        favicon_url: "/api/v2/branding/assets/favicon?v=ref.png",
      });
    throw new Error("Unexpected request: " + String(input));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => ({ branding: useContext(BrandingContext) }), { wrapper });
  await waitFor(() => expect(result.current.branding.serverName).toBe("Fixture"));
  expect(result.current.branding.accentColor).toBe("#10b981");
  expect(document.getElementById("silo-accent")?.textContent).toBe(
    ":root {\n  --primary: #10b981;\n  --ring: #10b981;\n  --sidebar-primary: #10b981;\n}\n" +
      'html[data-high-contrast="true"] {\n  --ring: var(--contrast-boost);\n}',
  );
  expect(document.querySelector('link[rel="icon"]')?.getAttribute("href")).toBe(
    "/api/v2/branding/assets/favicon?v=ref.png",
  );
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

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
