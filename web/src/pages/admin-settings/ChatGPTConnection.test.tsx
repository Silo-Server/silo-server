import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { ChatGPTConnection } from "./ChatGPTConnection";

const mocks = vi.hoisted(() => ({
  start: vi.fn(),
  reset: vi.fn(),
  complete: vi.fn(),
  completeReset: vi.fn(),
  select: vi.fn(),
  disconnect: vi.fn(),
  refetch: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));
type Status = {
  active_client_id: string;
  accounts: { client_id: string; email: string; connected: boolean }[];
  login_pending: boolean;
  login_result: string;
  attempt_id: string;
};
let status: Status;
let statusVersion = 0;
let statusFetching = false;
let completePending = false;
let loginExpiresAt: string;
let modelError = false;
vi.mock("@/hooks/queries/admin/chatgpt", () => ({
  useChatGPTStatus: () => ({
    data: status,
    dataUpdatedAt: statusVersion,
    isFetching: statusFetching,
    isLoading: false,
    isError: false,
    refetch: mocks.refetch,
  }),
  useStartChatGPTLogin: () => ({
    mutateAsync: mocks.start,
    reset: mocks.reset,
    isPending: false,
    data: {
      attempt_id: "fixture-state",
      expires_at: loginExpiresAt,
      authorization_url: "https://auth.openai.com/api/accounts/authorize?state=fixture-state",
      callback_uri: "http://127.0.0.1:51121/auth/callback",
    },
  }),
  useCompleteChatGPTLogin: () => ({
    mutateAsync: mocks.complete,
    reset: mocks.completeReset,
    isPending: completePending,
  }),
  useSelectChatGPTAccount: () => ({ mutateAsync: mocks.select, isPending: false }),
  useDisconnectChatGPTAccount: () => ({ mutateAsync: mocks.disconnect, isPending: false }),
  useChatGPTModels: () => ({
    data: modelError ? undefined : { models: [{ id: "gpt-6.1-sol", name: "GPT-6.1 Sol" }] },
    isLoading: false,
    isError: modelError,
    refetch: mocks.refetch,
  }),
}));
vi.mock("sonner", () => ({ toast: { success: mocks.success, error: mocks.error } }));

describe("ChatGPTConnection", () => {
  afterEach(() => vi.useRealTimers());
  beforeEach(() => {
    setAccessToken("chatgpt-test");
    setProfileId("profile-a");
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.start.mockResolvedValue({ attempt_id: "fixture-state" });
    status = {
      active_client_id: "",
      accounts: [],
      login_pending: false,
      login_result: "idle",
      attempt_id: "old-attempt",
    };
    modelError = false;
    statusVersion = 0;
    statusFetching = false;
    completePending = false;
    loginExpiresAt = new Date(Date.now() + 600_000).toISOString();
  });

  it("clears the login dialog when the active authority changes", async () => {
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Browser URL" }), "old-callback");
    setProfileId("profile-b");
    view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mocks.complete).not.toHaveBeenCalled();
    expect(mocks.success).not.toHaveBeenCalled();
  });

  it("does not open an old pending login result after an account switch", async () => {
    let resolve!: (value: object) => void;
    mocks.start.mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    setAccessToken("another-account");
    view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await act(async () => {
      resolve({ attempt_id: "fixture-state" });
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it("opens browser sign-in, submits the pasted URL, and welcomes a confirmed connection", async () => {
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    expect(mocks.start).toHaveBeenCalledWith(undefined);
    expect(screen.getByRole("link", { name: "Open ChatGPT sign-in" })).toHaveAttribute(
      "href",
      "https://auth.openai.com/api/accounts/authorize?state=fixture-state",
    );
    expect(screen.getByText(/may show “can’t connect/)).toBeInTheDocument();
    expect(screen.queryByText(/silo ai login/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect ChatGPT" })).toBeDisabled();
    const callbackURL =
      "http://127.0.0.1:51121/auth/callback?state=fixture-state&code=fixture-auth-code&client_id=issued-app";
    await userEvent.type(screen.getByRole("textbox", { name: "Browser URL" }), callbackURL);
    await userEvent.click(screen.getByRole("button", { name: "Connect ChatGPT" }));
    expect(mocks.complete).toHaveBeenCalledWith(callbackURL);
    expect(mocks.success).not.toHaveBeenCalled();
    act(() => {
      status = { ...status, login_result: "connected", attempt_id: "fixture-state" };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(
      screen.getByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).toBeInTheDocument();
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
  });

  it("accepts a matching successful result observed after the local login deadline", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    loginExpiresAt = new Date(Date.now() + 1_000).toISOString();
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    act(() => {
      vi.setSystemTime(Date.parse(loginExpiresAt) + 1);
      status = { ...status, login_result: "connected", attempt_id: "fixture-state" };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
    expect(mocks.error).not.toHaveBeenCalled();
    expect(
      screen.getByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).toBeInTheDocument();
  });

  it("keeps an accepted completion open across expiry and reconciles its settled result", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    loginExpiresAt = new Date(Date.now() + 1_000).toISOString();
    let resolveCompletion!: () => void;
    mocks.complete.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveCompletion = resolve;
        }),
    );
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    await userEvent.type(
      screen.getByRole("textbox", { name: "Browser URL" }),
      "http://127.0.0.1:51121/auth/callback?state=fixture-state&code=auth-code&client_id=issued-app",
    );
    await userEvent.click(screen.getByRole("button", { name: "Connect ChatGPT" }));
    act(() => {
      completePending = true;
      vi.setSystemTime(Date.parse(loginExpiresAt) + 1);
      status = {
        ...status,
        attempt_id: "fixture-state",
        login_pending: false,
        login_result: "expired",
      };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(screen.getByRole("button", { name: "Connecting…" })).toBeDisabled();
    expect(mocks.error).not.toHaveBeenCalled();
    expect(mocks.completeReset).not.toHaveBeenCalled();
    await act(async () => {
      resolveCompletion();
      completePending = false;
      statusFetching = true;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(screen.getByRole("textbox", { name: "Browser URL" })).toBeInTheDocument();
    expect(mocks.error).not.toHaveBeenCalled();
    act(() => {
      statusFetching = false;
      status = { ...status, login_pending: false, login_result: "connected" };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it("rejects an authorization link or unrelated callback before submitting", async () => {
    render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    const input = screen.getByRole("textbox", { name: "Browser URL" });
    for (const url of [
      "https://auth.openai.com/api/accounts/authorize?state=fixture-state",
      "https://example.test/auth/callback?state=fixture-state&code=auth-code",
      "http://127.0.0.1:51122/auth/callback?state=fixture-state&code=auth-code",
    ]) {
      await userEvent.clear(input);
      await userEvent.type(input, url);
      await userEvent.click(screen.getByRole("button", { name: "Connect ChatGPT" }));
      expect(screen.getByRole("alert")).toHaveTextContent("Paste the full URL");
    }
    expect(mocks.complete).not.toHaveBeenCalled();
  });

  it("keeps the dialog open after a failed submission and reconciles its status", async () => {
    mocks.complete.mockRejectedValue(new Error("network failure"));
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    await userEvent.type(
      screen.getByRole("textbox", { name: "Browser URL" }),
      "http://127.0.0.1:51121/auth/callback?state=fixture-state&code=auth-code&client_id=issued-app",
    );
    await userEvent.click(screen.getByRole("button", { name: "Connect ChatGPT" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Could not complete sign-in");
    expect(mocks.refetch).toHaveBeenCalledOnce();
    expect(mocks.complete).toHaveBeenCalledOnce();
    expect(mocks.success).not.toHaveBeenCalled();
    act(() => {
      status = { ...status, login_result: "connected", attempt_id: "fixture-state" };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
  });

  it("clears the pasted URL when the dialog closes", async () => {
    render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Browser URL" }), "private-code");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    expect(screen.getByRole("textbox", { name: "Browser URL" })).toHaveValue("");
    expect(mocks.completeReset).toHaveBeenCalled();
  });

  it("reuses the saved registration and reports failed consent even with an existing account", async () => {
    status = {
      ...status,
      active_client_id: "saved-app",
      accounts: [{ client_id: "saved-app", email: "admin@example.test", connected: true }],
    };
    const view = render(<ChatGPTConnection model="gpt-6.1-sol" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Reconnect admin@example.test" }));
    expect(mocks.start).toHaveBeenCalledWith("saved-app");
    act(() => {
      status = { ...status, login_result: "failed", attempt_id: "fixture-state" };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="gpt-6.1-sol" onModelChange={vi.fn()} />);
    });
    expect(mocks.error).toHaveBeenCalledWith(expect.stringContaining("Sign-in did not complete"));
    expect(mocks.success).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).not.toBeInTheDocument();
  });

  it("offers a retry when the account model catalog fails", async () => {
    status = {
      ...status,
      active_client_id: "app",
      accounts: [{ client_id: "app", email: "admin@example.test", connected: true }],
    };
    modelError = true;
    render(<ChatGPTConnection model="old-model" onModelChange={vi.fn()} />);
    expect(screen.getByRole("combobox", { name: "Model" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Retry models" }));
    expect(mocks.refetch).toHaveBeenCalledOnce();
  });

  it("reports a local disconnect when remote revocation was not confirmed", async () => {
    status = {
      ...status,
      active_client_id: "app",
      accounts: [{ client_id: "app", email: "admin@example.test", connected: true }],
    };
    mocks.disconnect.mockResolvedValue({ revocation_confirmed: false });
    render(<ChatGPTConnection model="gpt-6.1-sol" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
    expect(mocks.disconnect).toHaveBeenCalledWith("app");
    expect(mocks.error).toHaveBeenCalledWith(expect.stringContaining("Disconnected locally"));
    expect(mocks.success).not.toHaveBeenCalled();
  });
  it("ignores another attempt's terminal status and doesn't toast after closing", async () => {
    status = { ...status, login_result: "connected" };
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(mocks.success).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    status = { ...status, attempt_id: "fixture-state" };
    statusVersion++;
    view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    expect(mocks.success).not.toHaveBeenCalled();
  });
});
