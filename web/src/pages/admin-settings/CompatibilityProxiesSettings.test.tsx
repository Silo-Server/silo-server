import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";

import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";

const SETTINGS = {
  "jellyfin_compat.enabled": "true",
  "jellyfin_compat.web_enabled": "true",
  "jellyfin_compat.public_url": "https://media.example.com",
};

const { sensitiveStatus } = vi.hoisted(() => ({
  sensitiveStatus: {
    current: { configured: [], managed_by_env: [] } as {
      configured: string[];
      managed_by_env: string[];
      managed_by_env_vars?: Record<string, string>;
    },
  },
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: SETTINGS, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: sensitiveStatus.current, isError: false }),
  useUpdateServerSettings: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useJellyfinCompatStatus: () => ({
    data: { enabled: true, web_enabled: true, api_state: "running", web_state: "missing" },
    isLoading: false,
  }),
  useInstallJellyfinCompatWeb: () => ({ mutate: vi.fn(), isPending: false }),
  useRemoveJellyfinCompatWeb: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateJellyfinCompatSettings: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/useRestartKeys", () => ({ useRestartKeys: () => ({ has: () => false }) }));

beforeEach(() => {
  sensitiveStatus.current = { configured: [], managed_by_env: [] };
});

it("stages nothing when the Jellyfin API is switched off and back on", async () => {
  render(<CompatibilityProxiesSettings />);
  const api = screen.getByRole("switch", { name: "Allow Jellyfin apps to connect" });
  await userEvent.click(api);
  await userEvent.click(api);

  expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument();
});

it("takes back the Jellyfin Web switch-off once the environment sets the API switch", async () => {
  const { rerender } = render(<CompatibilityProxiesSettings />);
  await userEvent.click(screen.getByRole("switch", { name: "Allow Jellyfin apps to connect" }));
  expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();

  // A restart with SILO_JELLYFIN_ENABLED set: the form drops the API draft.
  sensitiveStatus.current = {
    configured: [],
    managed_by_env: ["jellyfin_compat.enabled"],
    managed_by_env_vars: { "jellyfin_compat.enabled": "SILO_JELLYFIN_ENABLED" },
  };
  rerender(<CompatibilityProxiesSettings />);

  expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument();
});
