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
  useAdminApiKeys: () => ({
    data: [
      {
        id: 7,
        user_id: 1,
        username: "admin",
        label: "CI",
        key: "silo_listed_key_0123456789",
        rate_tier: "standard",
        created_at: "2026-09-01T00:00:00Z",
      },
    ],
    isLoading: false,
  }),
  useAdminCreateApiKey: () => ({ mutate: mocks.createKey, isPending: false }),
  useAdminDeleteApiKey: () => ({ mutate: vi.fn() }),
  useAdminUpdateApiKeyTier: () => ({ mutate: vi.fn() }),
}));

const CREATED_KEY = "silo_created_key_9876543210";

async function createKey() {
  await userEvent.click(screen.getByRole("button", { name: /Create Key/ }));
  await userEvent.type(screen.getByPlaceholderText(/CI\/CD Pipeline/), "Deploy bot");
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
    mocks.createKey.mockImplementation((_body, options) => {
      options.onSuccess({ key: CREATED_KEY });
    });
  });

  it("copies a listed key through the shared clipboard helper", async () => {
    render(<AdminApiKeys />);

    await userEvent.click(screen.getByRole("button", { name: "Copy API key CI" }));

    await waitFor(() =>
      expect(mocks.copyTextToClipboard).toHaveBeenCalledWith("silo_listed_key_0123456789"),
    );
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Copied to clipboard");
  });

  it("closes the create dialog once the new key is copied", async () => {
    render(<AdminApiKeys />);
    await createKey();

    await userEvent.click(screen.getByRole("button", { name: /Copy & Close/ }));

    await waitFor(() => expect(mocks.copyTextToClipboard).toHaveBeenCalledWith(CREATED_KEY));
    await waitFor(() => expect(screen.queryByText(CREATED_KEY)).not.toBeInTheDocument());
  });

  it("keeps the new key on screen when the copy fails", async () => {
    // The key is only ever shown once, so a failed copy must not close the dialog.
    mocks.copyTextToClipboard.mockRejectedValue(new Error("clipboard blocked"));
    render(<AdminApiKeys />);
    await createKey();

    await userEvent.click(screen.getByRole("button", { name: /Copy & Close/ }));

    await waitFor(() =>
      expect(mocks.toastError).toHaveBeenCalledWith(
        "Couldn't copy — select the key and copy it manually",
      ),
    );
    expect(mocks.toastSuccess).not.toHaveBeenCalledWith("Copied to clipboard");
    expect(screen.getByText(CREATED_KEY)).toBeInTheDocument();
  });
});
