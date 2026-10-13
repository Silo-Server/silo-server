// @vitest-environment jsdom
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "@/hooks/useAuth";
import { useUpdateSignInBinding } from "@/hooks/queries/admin/externalSignIn";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import { queryClient } from "@/lib/query-client";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}
function requestPath(input: RequestInfo | URL) {
  return new URL(
    typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
    "http://localhost",
  ).pathname;
}
afterEach(() => {
  cleanup();
  queryClient.clear();
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken(null);
  setProfileId(null);
  setRefreshToken(null);
  vi.unstubAllGlobals();
});
function ProviderProbe() {
  const { providers, setupLoading, logoutOfSiloOnly } = useAuth();
  const binding = useUpdateSignInBinding();
  return (
    <div>
      <p data-testid="providers">
        {setupLoading ? "loading" : providers.map((p) => p.id).join(",")}
      </p>
      <button
        onClick={() =>
          binding.mutate({
            installationId: 5,
            body: {
              capability_id: "oidc",
              enabled: true,
              display_order: 0,
              auto_provision: true,
              default_login: false,
            },
          })
        }
      >
        Enable OIDC
      </button>
      <p data-testid="saved">{String(binding.isSuccess)}</p>
      <button onClick={logoutOfSiloOnly}>Sign out</button>
    </div>
  );
}
it("refreshes discovery after a live provider save and again after signing out", async () => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken(null);
  setRefreshToken(null);
  let discovery = {
    items: [{ id: "local", mode: "credentials", display_name: "Silo account", default: true }],
    password_login: true,
  };
  let reads = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const p = requestPath(input);
      if (p === "/api/v2/system/setup") return json({ needs_setup: false, wizard_completed: true });
      if (p === "/api/v2/auth/providers") {
        reads++;
        return json(discovery);
      }
      if (p === "/api/v2/admin/plugins/installations/5/auth-binding") {
        discovery = {
          items: [{ id: "plugin:5:oidc", mode: "oauth", display_name: "SSO", default: true }],
          password_login: false,
        };
        return new Response(null, { status: 204 });
      }
      if (p === "/api/v2/auth/logout") return new Response(null, { status: 204 });
      throw Error("unexpected " + p + " " + init?.method);
    }),
  );
  render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <ProviderProbe />
      </AuthProvider>
    </QueryClientProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("providers").textContent).toBe("local"));
  setAccessToken("owner-session");
  setRefreshToken("owner-refresh");
  setProfileId("owner-profile");
  await act(async () => screen.getByRole("button", { name: "Enable OIDC" }).click());
  await waitFor(() => expect(screen.getByTestId("saved").textContent).toBe("true"));
  await waitFor(() => expect(screen.getByTestId("providers").textContent).toBe("plugin:5:oidc"));
  expect(reads).toBeGreaterThan(1);
  discovery = {
    items: [{ id: "local", mode: "credentials", display_name: "Silo account", default: true }],
    password_login: true,
  };
  await act(async () => screen.getByRole("button", { name: "Sign out" }).click());
  await waitFor(() => expect(screen.getByTestId("providers").textContent).toBe("local"));
});
