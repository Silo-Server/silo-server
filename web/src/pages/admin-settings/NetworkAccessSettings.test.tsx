import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  NetworkAccessCapabilities,
  NetworkAccessStatus,
} from "@/hooks/queries/admin/networkAccess";

import NetworkAccessSettings from "./NetworkAccessSettings";

const mocks = vi.hoisted(() => ({
  capabilities: { data: undefined as NetworkAccessCapabilities | undefined, isLoading: false },
  status: {
    data: undefined as NetworkAccessStatus | undefined,
    isLoading: false,
    isError: false,
    error: null as unknown,
  },
  statusCalls: [] as Array<string | null>,
  connect: vi.fn(),
  disconnect: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/networkAccess", () => ({
  useNetworkAccessCapabilities: () => mocks.capabilities,
  useAdminNetworkAccessStatus: (provider: string | null) => {
    mocks.statusCalls.push(provider);
    return mocks.status;
  },
  useConnectNetworkAccess: () => ({ mutate: mocks.connect, isPending: false }),
  useDisconnectNetworkAccess: () => ({ mutate: mocks.disconnect, isPending: false }),
}));

function capabilities(
  providers: NetworkAccessCapabilities["providers"] = [],
): NetworkAccessCapabilities {
  return {
    revision: "r1",
    state: providers.length ? "available" : "not_configured",
    allowed: providers.length > 0,
    providers,
  };
}

const tailscale = { provider: "tailscale", display_name: "Tailscale", installation_id: "7" };

function host(
  overrides: Partial<NetworkAccessStatus["hosts"][number]> = {},
): NetworkAccessStatus["hosts"][number] {
  return {
    host: { id: "api", role: "api", name: "Living Room" },
    state: "disconnected",
    addresses: [],
    ...overrides,
  };
}

describe("NetworkAccessSettings", () => {
  beforeEach(() => {
    mocks.capabilities = { data: capabilities(), isLoading: false };
    mocks.status = { data: undefined, isLoading: false, isError: false, error: null };
    mocks.statusCalls = [];
    mocks.connect.mockReset();
    mocks.disconnect.mockReset();
  });

  it("heads the page and explains that providers are plugins when none is installed", () => {
    render(<NetworkAccessSettings />);

    expect(screen.getByRole("heading", { level: 1, name: "Network Access" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "No providers installed" })).toBeInTheDocument();
    expect(
      screen.getByText(/Install a network access provider from the Plugins page/),
    ).toBeVisible();
    expect(mocks.statusCalls).toHaveLength(0);
  });

  it("lists each provider with a row per host, its state and origin", () => {
    mocks.capabilities = { data: capabilities([tailscale]), isLoading: false };
    mocks.status = {
      data: {
        provider: "tailscale",
        hosts: [
          host({
            state: "connected",
            hostname: "silo.tail1234.ts.net",
            origin: "https://silo.tail1234.ts.net",
            addresses: ["100.64.0.7"],
            provider_version: "tsnet 1.102.4",
            updated_at: "2026-09-14T09:00:00.000Z",
          }),
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    };

    render(<NetworkAccessSettings />);

    expect(mocks.statusCalls).toEqual(["tailscale"]);
    const group = screen.getByRole("group", { name: "Tailscale" });
    const row = within(group).getByTestId("network-access-host-tailscale-api");
    expect(row).toHaveAttribute("data-state", "connected");
    expect(within(row).getByText("Living Room · API server")).toBeInTheDocument();
    expect(within(row).getByText("Connected")).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "https://silo.tail1234.ts.net" })).toHaveAttribute(
      "href",
      "https://silo.tail1234.ts.net",
    );
    expect(within(row).getByText("100.64.0.7")).toBeInTheDocument();
    expect(within(row).getByText("tsnet 1.102.4")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: "Disconnect" })).toBeEnabled();
    expect(within(row).queryByRole("button", { name: "Connect" })).not.toBeInTheDocument();
    // Internal enum names never reach the admin.
    expect(group).not.toHaveTextContent("awaiting_authorization");
  });

  it("links the authorization page while a host waits for enrollment", () => {
    mocks.capabilities = { data: capabilities([tailscale]), isLoading: false };
    mocks.status = {
      data: {
        provider: "tailscale",
        hosts: [
          host({ state: "awaiting_authorization", auth_url: "https://login.example.test/a/abc" }),
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    };

    render(<NetworkAccessSettings />);

    const row = screen.getByTestId("network-access-host-tailscale-api");
    expect(within(row).getByText("Waiting for authorization")).toBeInTheDocument();
    const link = within(row).getByRole("link", { name: /Open the authorization page/ });
    expect(link).toHaveAttribute("href", "https://login.example.test/a/abc");
    expect(link).toHaveAttribute("target", "_blank");
    // Enrollment can be abandoned from here.
    expect(within(row).getByRole("button", { name: "Disconnect" })).toBeEnabled();
  });

  it("sends connect and disconnect for the row's host only", async () => {
    const user = userEvent.setup();
    mocks.capabilities = { data: capabilities([tailscale]), isLoading: false };
    mocks.status = {
      data: { provider: "tailscale", hosts: [host()] },
      isLoading: false,
      isError: false,
      error: null,
    };

    render(<NetworkAccessSettings />);

    await user.click(screen.getByRole("button", { name: "Connect" }));
    expect(mocks.connect).toHaveBeenCalledWith({ provider: "tailscale", hosts: ["api"] });
    expect(mocks.disconnect).not.toHaveBeenCalled();

    mocks.status = {
      data: { provider: "tailscale", hosts: [host({ state: "connected" })] },
      isLoading: false,
      isError: false,
      error: null,
    };
    render(<NetworkAccessSettings />);
    await user.click(screen.getByRole("button", { name: "Disconnect" }));
    expect(mocks.disconnect).toHaveBeenCalledWith({ provider: "tailscale", hosts: ["api"] });
  });

  it("explains a host whose plugin is not running and keeps Connect disabled there", () => {
    mocks.capabilities = { data: capabilities([tailscale]), isLoading: false };
    mocks.status = {
      data: {
        provider: "tailscale",
        hosts: [
          host({ state: "unavailable", error: "plugin process is backoff: plugin process exited" }),
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    };

    render(<NetworkAccessSettings />);

    const row = screen.getByTestId("network-access-host-tailscale-api");
    expect(within(row).getByText("Plugin not running")).toBeInTheDocument();
    expect(within(row).getByRole("status")).toHaveTextContent("plugin process exited");
    expect(within(row).getByRole("button", { name: "Connect" })).toBeDisabled();
    expect(row).not.toHaveTextContent("unavailable");
  });

  it("surfaces a status read failure without hiding the provider", () => {
    mocks.capabilities = { data: capabilities([tailscale]), isLoading: false };
    mocks.status = {
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("Network access provider not found."),
    };

    render(<NetworkAccessSettings />);

    expect(screen.getByRole("group", { name: "Tailscale" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Network access provider not found.");
  });
});
