import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useCapability: vi.fn(),
  useSources: vi.fn(),
  useRuns: vi.fn(),
  useRun: vi.fn(),
  useCreateRun: vi.fn(),
  useLoginEmbyConnect: vi.fn(),
  createMutateAsync: vi.fn(),
  loginMutateAsync: vi.fn(),
  resetCreate: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
    success: vi.fn(),
  },
}));

vi.mock("@/components/realtimeEventsContext", () => ({
  useEventChannel: () => undefined,
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({
    profile: { id: "profile-1", name: "Owner", is_primary: true },
  }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    user: { id: 1, role: "user" },
  }),
}));

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({
    data: [{ id: "profile-1", name: "Owner", is_primary: true }],
  }),
}));

vi.mock("@/hooks/queries/history-import", () => ({
  useHistoryImportCapability: () => mocks.useCapability(),
  useHistoryImportSources: () => mocks.useSources(),
  useHistoryImportRuns: () => mocks.useRuns(),
  useHistoryImportRun: () => mocks.useRun(),
  useCreateHistoryImportRun: () => mocks.useCreateRun(),
  useLoginEmbyConnect: () => mocks.useLoginEmbyConnect(),
}));

import HistoryImportSettings from "./HistoryImportSettings";
import { isValidHttpServerURL } from "./HistoryImportSettings.utils";

function renderPage() {
  return render(
    <MemoryRouter>
      <HistoryImportSettings />
    </MemoryRouter>,
  );
}

describe("HistoryImportSettings", () => {
  beforeEach(() => {
    mocks.createMutateAsync.mockReset().mockResolvedValue({ id: "run-1" });
    mocks.loginMutateAsync.mockReset().mockResolvedValue({
      connect_session_id: "sess-1",
      servers: [{ server_id: "srv-1", name: "Home Emby" }],
      expires_at: "2026-11-26T00:00:00Z",
    });
    mocks.resetCreate.mockReset();
    mocks.toastError.mockReset();

    mocks.useCapability.mockReset().mockReturnValue({
      data: {
        state: "available",
        plex_connection_fallback: true,
        max_plex_connections: 8,
        emby_custom_server: true,
      },
    });
    mocks.useSources.mockReset().mockReturnValue({ data: [], isLoading: false });
    mocks.useRuns.mockReset().mockReturnValue({ data: [] });
    mocks.useRun.mockReset().mockReturnValue({ data: undefined, error: null, refetch: vi.fn() });
    mocks.useCreateRun.mockReset().mockReturnValue({
      mutateAsync: mocks.createMutateAsync,
      isPending: false,
      error: null,
      reset: mocks.resetCreate,
    });
    mocks.useLoginEmbyConnect.mockReset().mockReturnValue({
      mutateAsync: mocks.loginMutateAsync,
      isPending: false,
      error: null,
    });
  });

  it("hides the Emby Server URL mode when the server does not advertise capability support", () => {
    mocks.useCapability.mockReturnValue({
      data: {
        state: "available",
        plex_connection_fallback: true,
        max_plex_connections: 8,
      },
    });

    renderPage();

    expect(screen.getByRole("button", { name: "Emby Connect" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Saved Server" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Server URL" })).not.toBeInTheDocument();
  });

  it("submits direct Emby server URL imports with trimmed inputs and optional password, then clears the password", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "Server URL" }));

    const urlInput = screen.getByLabelText("Emby Server URL");
    const usernameInput = screen.getByLabelText("Emby Username");
    const passwordInput = screen.getByLabelText("Emby Password");
    const startButton = screen.getByRole("button", { name: "Start Import" });

    expect(startButton).toBeDisabled();

    await user.type(urlInput, "   ");
    await user.type(usernameInput, "   ");
    expect(startButton).toBeDisabled();

    await user.clear(urlInput);
    await user.clear(usernameInput);
    await user.type(urlInput, "  https://emby.example.test  ");
    await user.type(usernameInput, "  alice  ");
    expect(startButton).toBeEnabled();

    await user.type(passwordInput, "secret-pass");
    await user.click(startButton);

    expect(mocks.createMutateAsync).toHaveBeenCalledWith({
      profile_id: "profile-1",
      source: "emby",
      emby_base_url: "https://emby.example.test",
      username: "alice",
      password: "secret-pass",
    });
    await waitFor(() => {
      expect(passwordInput).toHaveValue("");
    });
  });

  it("scopes start errors to the submitted mode without calling mutation reset", async () => {
    const user = userEvent.setup();
    let currentError: Error | null = null;
    let isPending = false;
    let rejectRun: ((err: Error) => void) | undefined;
    mocks.createMutateAsync.mockImplementation(
      () =>
        new Promise((_resolve, reject) => {
          isPending = true;
          rejectRun = (err: Error) => {
            isPending = false;
            currentError = err;
            reject(err);
          };
        }),
    );
    mocks.useCreateRun.mockImplementation(() => ({
      mutateAsync: mocks.createMutateAsync,
      isPending,
      error: currentError,
      reset: mocks.resetCreate,
    }));

    const { rerender } = renderPage();
    await user.click(screen.getByRole("button", { name: "Server URL" }));
    await user.type(screen.getByLabelText("Emby Server URL"), "emby.example.test");
    await user.type(screen.getByLabelText("Emby Username"), "alice");
    await user.click(screen.getByRole("button", { name: "Start Import" }));

    // Switching away while pending hides the inline alert on other tabs and notifies via toast.
    await user.click(screen.getByRole("button", { name: "Emby Connect" }));
    rejectRun?.(
      new Error(
        "Emby server URL must be an HTTP or HTTPS URL without credentials, a query, or a fragment.",
      ),
    );
    await waitFor(() => {
      expect(mocks.toastError).toHaveBeenCalledWith(
        "Emby server URL must be an HTTP or HTTPS URL without credentials, a query, or a fragment.",
      );
    });
    rerender(
      <MemoryRouter>
        <HistoryImportSettings />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    // Returning to the failed tab still shows the error, marks the invalid URL field, and clicking the active tab keeps it.
    await user.click(screen.getByRole("button", { name: "Server URL" }));
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Emby server URL must be an HTTP or HTTPS URL without credentials, a query, or a fragment.",
    );
    const urlInput = screen.getByLabelText("Emby Server URL");
    expect(urlInput).toHaveAttribute("aria-invalid", "true");
    await user.click(screen.getByRole("button", { name: "Server URL" }));
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Emby server URL must be an HTTP or HTTPS URL without credentials, a query, or a fragment.",
    );

    // Fixing the URL syntax clears aria-invalid immediately, and failing on the visible tab does not toast.
    await user.clear(urlInput);
    await user.type(urlInput, "https://emby.example.test");
    expect(urlInput).not.toHaveAttribute("aria-invalid");
    mocks.toastError.mockReset();
    await user.click(screen.getByRole("button", { name: "Start Import" }));
    rejectRun?.(new Error("Invalid credentials"));
    await waitFor(() => {
      expect(isPending).toBe(false);
    });
    expect(mocks.toastError).not.toHaveBeenCalled();

    // Leaving the failed tab after seeing the error dismisses it without calling mutation.reset().
    await user.click(screen.getByRole("button", { name: "Emby Connect" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(mocks.resetCreate).not.toHaveBeenCalled();
  });

  it("disables Find Servers until credentials are filled and catches login rejection", async () => {
    const user = userEvent.setup();
    mocks.loginMutateAsync.mockRejectedValueOnce(new Error("Invalid credentials"));

    renderPage();

    const findServers = screen.getByRole("button", { name: "Find Servers" });
    expect(findServers).toBeDisabled();

    const textboxes = screen.getAllByRole("textbox");
    const emailInput = textboxes[0]!;
    const passwordInput = document.querySelector<HTMLElement>('input[type="password"]')!;
    await user.type(emailInput, "   ");
    await user.type(passwordInput, "secret");
    expect(findServers).toBeDisabled();

    await user.clear(emailInput);
    await user.type(emailInput, "alice@example.test");
    expect(findServers).toBeEnabled();

    await user.click(findServers);
    expect(mocks.loginMutateAsync).toHaveBeenCalledWith({
      username: "alice@example.test",
      password: "secret",
    });
  });

  it("requires non-whitespace Jellyfin URL and username before enabling Start Import", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: /Jellyfin/i }));
    const startButton = screen.getByRole("button", { name: "Start Import" });

    const textboxes = screen.getAllByRole("textbox");
    const urlInput = textboxes[0]!;
    const usernameInput = textboxes[1]!;
    const passwordInput = document.querySelector<HTMLElement>('input[type="password"]')!;
    await user.type(urlInput, "   ");
    await user.type(usernameInput, "   ");
    await user.type(passwordInput, "pw");
    expect(startButton).toBeDisabled();

    await user.clear(urlInput);
    await user.clear(usernameInput);
    await user.type(urlInput, "https://jellyfin.example.test");
    await user.type(usernameInput, "alice");
    expect(startButton).toBeEnabled();
  });

  it("matches backend HTTP server URL rules including empty queries, missing slashes, and bare credentials", () => {
    expect(isValidHttpServerURL("https://emby.example.test")).toBe(true);
    expect(isValidHttpServerURL("http://emby.example.test:8096/emby")).toBe(true);
    expect(isValidHttpServerURL("https://emby.example.test?")).toBe(false);
    expect(isValidHttpServerURL("https://emby.example.test/?")).toBe(false);
    expect(isValidHttpServerURL("https://emby.example.test#")).toBe(false);
    expect(isValidHttpServerURL("https:emby.example.test")).toBe(false);
    expect(isValidHttpServerURL("http://@emby.example.test")).toBe(false);
    expect(isValidHttpServerURL("https://user:pass@emby.example.test")).toBe(false);
  });
});
