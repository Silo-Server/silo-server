import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AdminApiKeys from "./AdminApiKeys";

const mocks = vi.hoisted(() => ({
  copyTextToClipboard: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  createKey: vi.fn(),
}));

vi.mock("@/lib/clipboard", () => ({
  copyTextToClipboard: (...args: unknown[]) => mocks.copyTextToClipboard(...args),
}));

vi.mock("sonner", () => ({
  toast: { error: mocks.toastError, success: mocks.toastSuccess },
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: { id: 1 } }),
}));

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({ data: [{ id: 1, username: "admin" }] }),
}));

vi.mock("@/hooks/queries/admin/apiKeys", () => ({
  useAdminApiKeyCapabilities: () => ({ data: { available: true } }),
  useAdminApiKeys: () => ({
    data: {
      pages: [
        {
          items: [
            {
              id: "7",
              user_id: "1",
              username: "admin",
              label: "CI",
              key_prefix: "silo_listed",
              rate_tier: "standard",
              scopes: [],
              created_at: "2026-09-01T00:00:00Z",
            },
          ],
        },
      ],
    },
    isLoading: false,
  }),
  useAdminCreateApiKey: () => ({
    mutateAsync: mocks.createKey,
    isPending: false,
    reset: vi.fn(),
  }),
  useAdminDeleteApiKey: () => ({ mutate: vi.fn() }),
  useAdminUpdateApiKeyTier: () => ({ mutate: vi.fn() }),
}));

vi.mock("@/api/v2/adminApiKeys", () => ({
  captureAdminApiKeyAuthority: () => ({
    serverOrigin: "https://test.local",
    authContextVersion: 1,
    profileId: "1",
  }),
  adminApiKeyScope: () => "test-scope",
  getAdminApiKey: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  isCapturedProfileAuthorityActive: () => true,
}));

const CREATED_KEY = "silo_created_key_9876543210";

async function createKey() {
  await userEvent.click(screen.getByRole("button", { name: /Create Key/ }));
  await userEvent.type(screen.getByLabelText("Label"), "Deploy bot");
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  await screen.findByText(CREATED_KEY);
}

describe("AdminApiKeys", () => {
  beforeEach(() => {
    mocks.copyTextToClipboard.mockReset();
    mocks.copyTextToClipboard.mockResolvedValue(undefined);
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.createKey.mockReset();
    mocks.createKey.mockResolvedValue({ key: CREATED_KEY });
  });

  it("lists keys by prefix only; the full secret is never available to copy", () => {
    render(<AdminApiKeys />);

    expect(screen.getByText("silo_listed…")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Copy API key/ })).not.toBeInTheDocument();
  });

  it("closes the create dialog once the new key is copied", async () => {
    render(<AdminApiKeys />);
    await createKey();

    await userEvent.click(screen.getByRole("button", { name: /Copy & Close/ }));

    await waitFor(() => expect(mocks.copyTextToClipboard).toHaveBeenCalledWith(CREATED_KEY));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Copied to clipboard");
    await waitFor(() => expect(screen.queryByText(CREATED_KEY)).not.toBeInTheDocument());
  });

  it("keeps the new key on screen when the copy fails", async () => {
    // The key is only ever shown once, so a failed copy must not close the dialog.
    mocks.copyTextToClipboard.mockRejectedValue(new Error("clipboard blocked"));
    render(<AdminApiKeys />);
    await createKey();

    await userEvent.click(screen.getByRole("button", { name: /Copy & Close/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Couldn't copy — select the key and copy it manually",
    );
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
    expect(screen.getByText(CREATED_KEY)).toBeInTheDocument();
  });
});
