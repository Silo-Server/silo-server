import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ApiKeysSettings from "./ApiKeysSettings";

const mocks = vi.hoisted(() => ({
  capability: vi.fn(),
  profile: vi.fn(),
  create: vi.fn(),
}));

vi.mock("@/hooks/queries/personalApiKeys", () => ({
  usePersonalApiKeyScopes: () => ({
    data: mocks.capability(),
    isLoading: false,
    isError: mocks.capability() === undefined,
  }),
  usePersonalApiKeys: () => ({
    data: { pages: [{ items: [] }] },
    isLoading: false,
    isError: false,
    hasNextPage: false,
  }),
  useCreatePersonalApiKey: () => ({ mutateAsync: mocks.create, isPending: false }),
  useRevokePersonalApiKey: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({
    data: [
      { id: "parent", name: "Parent", is_primary: true },
      { id: "kid", name: "Kid", is_primary: false },
    ],
  }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: mocks.profile() }),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: 1, role: "user" } }) }));

// Radix measures its checkbox and select.
class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
vi.stubGlobal("ResizeObserver", MockResizeObserver);

const scopes = [
  { name: "watch:read", description: "Read watch history.", audience: "user" },
  { name: "admin:users", description: "Manage users.", audience: "admin" },
];

describe("Settings › API Keys", () => {
  beforeEach(() => {
    mocks.capability.mockReturnValue({
      available: true,
      user_keys_enabled: true,
      max_user_keys: 25,
      scopes,
    });
    mocks.create.mockResolvedValue({ key: "sa_shown_once" });
  });
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("gives a profile other than the primary one a key for itself only, with user scopes", async () => {
    mocks.profile.mockReturnValue({ id: "kid", name: "Kid", is_primary: false });
    render(<ApiKeysSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Create Key" }));
    expect(screen.queryByLabelText("Acts as")).toBeNull();
    expect(screen.queryByLabelText("admin:users")).toBeNull();
    fireEvent.change(screen.getByLabelText("Label"), { target: { value: "Sync" } });
    fireEvent.click(screen.getByLabelText("watch:read"));
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(mocks.create).toHaveBeenCalledWith({
        label: "Sync",
        scopes: ["watch:read"],
        profileId: "kid",
      }),
    );
    expect(await screen.findByText("sa_shown_once")).toBeTruthy();
    // A profile key is told it needs no profile header.
    expect(screen.getByText(/always acts as Kid/)).toBeTruthy();
    expect(screen.queryByText(/X-Profile-Id/)).toBeNull();
  });

  it("offers the primary profile a whole-account key by default", async () => {
    mocks.profile.mockReturnValue({ id: "parent", name: "Parent", is_primary: true });
    render(<ApiKeysSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Create Key" }));
    expect(screen.getByLabelText("Acts as")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Label"), { target: { value: "Everything" } });
    fireEvent.click(screen.getByLabelText("watch:read"));
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(mocks.create).toHaveBeenCalledWith({
        label: "Everything",
        scopes: ["watch:read"],
        profileId: undefined,
      }),
    );
    // A whole-account key is shown the header that picks its profile.
    expect(await screen.findByText("X-Profile-Id: parent")).toBeTruthy();
  });

  it("reports a failed capability read as a failure, not as keys being turned off", () => {
    mocks.profile.mockReturnValue({ id: "parent", name: "Parent", is_primary: true });
    mocks.capability.mockReturnValue(undefined);
    render(<ApiKeysSettings />);
    expect(screen.queryByRole("button", { name: "Create Key" })).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("Couldn't load your API keys");
    expect(screen.queryByText(/doesn't let users create API keys/)).toBeNull();
  });

  it("offers no key while the server keeps user keys off", () => {
    mocks.profile.mockReturnValue({ id: "parent", name: "Parent", is_primary: true });
    mocks.capability.mockReturnValue({
      available: true,
      user_keys_enabled: false,
      max_user_keys: 25,
      scopes,
    });
    render(<ApiKeysSettings />);
    expect(screen.queryByRole("button", { name: "Create Key" })).toBeNull();
    expect(screen.getByRole("status").textContent).toContain("doesn't let users create API keys");
  });
});
