// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { CatalogBrowser } from "./CatalogBrowser";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { setAccessToken, setRefreshToken } from "@/api/client";
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => false }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("installs the v2 portable download through the existing theme parser and CSS sanitizer", async () => {
  installPolicyStorageMocks();
  setAccessToken("access");
  setRefreshToken("refresh");
  const downloadUrl = "https://raw.githubusercontent.com/example/theme.json?rev=1&mode=preview";
  const fetchMock = vi.fn<typeof fetch>(async (input) => {
    const path = String(input);
    if (path === "/api/v2/theme/catalog")
      return jsonResponse({
        document: {
          version: 1,
          themes: [
            {
              id: "fixture",
              name: "Fixture",
              author: "Synthetic",
              previewAccent: "red",
              previewBg: "black",
              tags: [],
              downloadUrl,
              version: "1",
            },
          ],
        },
        stale: false,
      });
    if (path.startsWith("/api/v2/theme/download?")) {
      expect(new URL(path, "https://synthetic.example.test").searchParams.get("url")).toBe(
        downloadUrl,
      );
      return jsonResponse({
        document: {
          version: 1,
          name: "Fixture",
          baseTheme: "midnight-cinema",
          vars: { "--primary": "red" },
          customCss: '@import "https://unapproved.example.test/style.css"; body { color: red; }',
        },
      });
    }
    throw new Error("Unexpected request: " + path);
  });
  vi.stubGlobal("fetch", fetchMock);
  const onInstall = vi.fn();
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <CatalogBrowser onInstall={onInstall} />
    </QueryClientProvider>,
  );
  fireEvent.click(await screen.findByRole("button", { name: "Install" }));
  await waitFor(() => expect(onInstall).toHaveBeenCalledTimes(1));
  expect(onInstall.mock.calls[0]?.[0].baseTheme).toBe("midnight-cinema");
  expect(onInstall.mock.calls[0]?.[0].vars).toEqual({ "--primary": "red" });
  expect(onInstall.mock.calls[0]?.[0].customCss).toContain("color: red");
  expect(onInstall.mock.calls[0]?.[0].customCss).toContain("[blocked @import]");
  expect(onInstall.mock.calls[0]?.[0].customCss).not.toContain("unapproved.example.test");
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
