import { act, render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId } from "@/api/client";
import { AuthProvider, useAuth } from "@/hooks/useAuth";
import { ChatGPTConnection } from "./ChatGPTConnection";

// JSDOM omits the browser methods used by Radix Select.
if (!HTMLElement.prototype.hasPointerCapture) HTMLElement.prototype.hasPointerCapture = () => false;
if (!HTMLElement.prototype.scrollIntoView) HTMLElement.prototype.scrollIntoView = () => {};

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
  login_first_connection?: boolean;
  accounts: {
    client_id: string;
    email: string;
    connected: boolean;
    needs_login?: boolean;
    plan_ineligible?: boolean;
    usage_limited?: boolean;
  }[];
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

/** Makes `mock` answer only when the returned function is called. */
function hold(mock: Mock) {
  let resolve!: (value: object) => void;
  mock.mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done;
      }),
  );
  return (value: object) => resolve(value);
}

/** Pastes the browser's callback URL for the fixture attempt and submits it. */
async function submitCallback() {
  await userEvent.type(
    screen.getByRole("textbox", { name: "Browser URL" }),
    "http://127.0.0.1:51121/auth/callback?state=fixture-state&code=auth-code&client_id=issued-app",
  );
  await userEvent.click(screen.getByRole("button", { name: "Connect ChatGPT" }));
}

describe("ChatGPTConnection", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  beforeEach(() => {
    setAccessToken("chatgpt-test");
    setProfileId("profile-a");
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.start.mockResolvedValue({ attempt_id: "fixture-state" });
    mocks.complete.mockResolvedValue({
      beforeETag: "before",
      afterETag: "after",
      firstConnection: true,
    });
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

  it("clears a prepared dialog when AuthProvider replaces the Silo session", async () => {
    setRefreshToken(null);
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              items: [],
              needs_setup: false,
              wizard_completed: true,
              id: "1",
              username: "fixture",
              email: "",
              role: "admin",
              permissions: [],
              download_allowed: true,
            }),
            {
              headers: { "Content-Type": "application/json" },
            },
          ),
      ),
    );
    let replaceSession!: (token: string) => void;
    function SessionControls() {
      const { completeLogin, loading } = useAuth();
      useEffect(() => {
        replaceSession = (token) => {
          completeLogin({
            access_token: token,
            refresh_token: "fixture-refresh",
            expires_in: 3600,
            user: {
              id: 1,
              username: "fixture",
              email: "",
              role: "admin",
              permissions: [],
              download_allowed: true,
            },
          });
          setProfileId("profile-a");
        };
      }, [completeLogin]);
      return <span data-testid="auth-ready">{loading ? "loading" : "ready"}</span>;
    }
    render(
      <AuthProvider>
        <SessionControls />
        <ChatGPTConnection model="" onModelChange={vi.fn()} />
      </AuthProvider>,
    );
    await waitFor(() => expect(screen.getByTestId("auth-ready")).toHaveTextContent("ready"));
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    act(() => replaceSession("replacement-session"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mocks.complete).not.toHaveBeenCalled();
    expect(mocks.success).not.toHaveBeenCalled();
  });

  it("does not open an old pending login result after an account switch", async () => {
    const resolve = hold(mocks.start);
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
    expect(mocks.success).toHaveBeenCalledOnce();
    act(() => {
      status = {
        ...status,
        login_result: "connected",
        attempt_id: "fixture-state",
        login_first_connection: true,
      };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(
      screen.getByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).toBeInTheDocument();
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
    expect(mocks.success).toHaveBeenCalledOnce();
  });

  it("confirms a successful completion after another browser replaces its login status", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    loginExpiresAt = new Date(Date.now() + 1_000).toISOString();
    const resolve = hold(mocks.complete);
    const confirmed = vi.fn();
    const acceptTransition = vi.fn();
    const props = {
      model: "",
      onModelChange: vi.fn(),
      onConnectionConfirmed: confirmed,
      onSettingsTransitionStarted: vi.fn(() => acceptTransition),
    };
    const view = render(<ChatGPTConnection {...props} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    await submitCallback();
    act(() => {
      completePending = true;
      status = {
        ...status,
        attempt_id: "another-browser",
        login_result: "pending",
        login_pending: true,
      };
      statusVersion++;
      view.rerender(<ChatGPTConnection {...props} />);
    });
    const change = { beforeETag: "before", afterETag: "after", firstConnection: true };
    await act(async () => {
      vi.setSystemTime(Date.parse(loginExpiresAt) + 1);
      resolve(change);
      completePending = false;
      view.rerender(<ChatGPTConnection {...props} />);
    });
    expect(acceptTransition).toHaveBeenCalledExactlyOnceWith(change);
    expect(confirmed).toHaveBeenCalledOnce();
    expect(mocks.success).toHaveBeenCalledOnce();
    expect(mocks.error).not.toHaveBeenCalled();
    expect(
      screen.getByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Browser URL" })).not.toBeInTheDocument();
  });

  it.each(["status", "response"])(
    "confirms once when %s arrives before the other completion signal",
    async (first) => {
      const resolve = hold(mocks.complete);
      const confirmed = vi.fn();
      const acceptTransition = vi.fn();
      const props = {
        model: "",
        onModelChange: vi.fn(),
        onConnectionConfirmed: confirmed,
        onSettingsTransitionStarted: vi.fn(() => acceptTransition),
      };
      const view = render(<ChatGPTConnection {...props} />);
      await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
      await submitCallback();
      const change = { beforeETag: "before", afterETag: "after", firstConnection: true };
      const signals = {
        status: async () => {
          act(() => {
            status = {
              ...status,
              attempt_id: "fixture-state",
              login_result: "connected",
              login_first_connection: true,
            };
            statusVersion++;
            view.rerender(<ChatGPTConnection {...props} />);
          });
        },
        response: async () => {
          await act(async () => {
            resolve(change);
          });
        },
      };
      await signals[first as keyof typeof signals]();
      expect(confirmed).toHaveBeenCalledOnce();
      expect(mocks.success).toHaveBeenCalledOnce();
      await userEvent.click(screen.getByRole("button", { name: "Got it" }));
      await signals[first === "status" ? "response" : "status"]();
      expect(acceptTransition).toHaveBeenCalledExactlyOnceWith(change);
      expect(confirmed).toHaveBeenCalledOnce();
      expect(mocks.success).toHaveBeenCalledOnce();
      expect(mocks.error).not.toHaveBeenCalled();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    },
  );

  it.each(["cancel", "new attempt", "authority change", "unmount"])(
    "ignores a late successful completion after %s",
    async (ending) => {
      const resolve = hold(mocks.complete);
      const confirmed = vi.fn();
      const acceptTransition = vi.fn();
      const props = {
        model: "",
        onModelChange: vi.fn(),
        onConnectionConfirmed: confirmed,
        onSettingsTransitionStarted: vi.fn(() => acceptTransition),
      };
      const view = render(<ChatGPTConnection {...props} />);
      await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
      await submitCallback();
      if (ending === "authority change") {
        setProfileId("profile-b");
        view.rerender(<ChatGPTConnection {...props} />);
      } else if (ending === "unmount") {
        view.unmount();
      } else {
        await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
        if (ending === "new attempt") {
          await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
        }
      }
      await act(async () => {
        resolve({ beforeETag: "before", afterETag: "after", firstConnection: true });
      });
      expect(acceptTransition).not.toHaveBeenCalled();
      expect(confirmed).not.toHaveBeenCalled();
      expect(mocks.success).not.toHaveBeenCalled();
      expect(mocks.error).not.toHaveBeenCalled();
      expect(
        screen.queryByRole("heading", { name: "You’re using your ChatGPT plan" }),
      ).not.toBeInTheDocument();
    },
  );

  it("uses the confirmed login outcome when a stale status missed another activation", async () => {
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    act(() => {
      status = {
        ...status,
        login_first_connection: false,
        login_result: "connected",
        attempt_id: "fixture-state",
      };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
    expect(
      screen.queryByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).not.toBeInTheDocument();
  });

  it("welcomes a first successful account selection and skips later selections with stale status", async () => {
    status.accounts = [{ client_id: "saved-app", email: "admin@example.test", connected: true }];
    const change = { beforeETag: "before", afterETag: "after", firstConnection: true };
    mocks.select.mockResolvedValue(change);
    const acceptTransition = vi.fn();
    const props = {
      model: "",
      onModelChange: vi.fn(),
      onSettingsTransitionStarted: vi.fn(() => acceptTransition),
    };
    const view = render(<ChatGPTConnection {...props} />);
    await userEvent.click(screen.getByRole("combobox", { name: "ChatGPT account" }));
    await userEvent.click(screen.getByRole("option", { name: "admin@example.test · 1" }));
    expect(
      screen.getByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).toBeInTheDocument();
    expect(acceptTransition).toHaveBeenCalledExactlyOnceWith(change);
    view.unmount();
    mocks.select.mockResolvedValue({ ...change, firstConnection: false });
    render(<ChatGPTConnection {...props} />);
    await userEvent.click(screen.getByRole("combobox", { name: "ChatGPT account" }));
    await userEvent.click(screen.getByRole("option", { name: "admin@example.test · 1" }));
    expect(
      screen.queryByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).not.toBeInTheDocument();
  });

  it("ignores a first selection result after the initiating profile changes", async () => {
    status.accounts = [{ client_id: "saved-app", email: "admin@example.test", connected: true }];
    const resolve = hold(mocks.select);
    const acceptTransition = vi.fn();
    const props = {
      model: "",
      onModelChange: vi.fn(),
      onSettingsTransitionStarted: vi.fn(() => acceptTransition),
    };
    const view = render(<ChatGPTConnection {...props} />);
    await userEvent.click(screen.getByRole("combobox", { name: "ChatGPT account" }));
    await userEvent.click(screen.getByRole("option", { name: "admin@example.test · 1" }));
    setProfileId("profile-b");
    view.rerender(<ChatGPTConnection {...props} />);
    await act(async () => {
      resolve({ beforeETag: "before", afterETag: "after", firstConnection: true });
    });
    expect(acceptTransition).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).not.toBeInTheDocument();
  });

  it("accepts a matching successful result observed after the local login deadline", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    loginExpiresAt = new Date(Date.now() + 1_000).toISOString();
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    act(() => {
      vi.setSystemTime(Date.parse(loginExpiresAt) + 1);
      status = {
        ...status,
        login_result: "connected",
        attempt_id: "fixture-state",
        login_first_connection: true,
      };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
    expect(mocks.error).not.toHaveBeenCalled();
    expect(
      screen.getByRole("heading", { name: "You’re using your ChatGPT plan" }),
    ).toBeInTheDocument();
  });

  it("keeps server-confirmed pending sign-in open when the browser clock passes its deadline", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    loginExpiresAt = new Date(Date.now() + 1_000).toISOString();
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    act(() => {
      vi.setSystemTime(Date.parse(loginExpiresAt) + 3_600_000);
      status = {
        ...status,
        attempt_id: "fixture-state",
        login_pending: true,
        login_result: "pending",
      };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(screen.getByRole("textbox", { name: "Browser URL" })).toBeInTheDocument();
    expect(mocks.error).not.toHaveBeenCalled();
    expect(mocks.completeReset).not.toHaveBeenCalled();
    await submitCallback();
    expect(mocks.complete).toHaveBeenCalledOnce();
    expect(mocks.success).toHaveBeenCalledOnce();
  });

  it.each(["expired", "superseded"])("closes a settled %s sign-in", async (ending) => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    act(() => {
      status = {
        ...status,
        attempt_id: ending === "expired" ? "fixture-state" : "another-browser",
        login_pending: ending !== "expired",
        login_result: ending === "expired" ? "expired" : "pending",
      };
      if (ending === "superseded") vi.setSystemTime(Date.parse(loginExpiresAt) + 1);
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(screen.queryByRole("textbox", { name: "Browser URL" })).not.toBeInTheDocument();
    expect(mocks.error).toHaveBeenCalledOnce();
    expect(mocks.complete).not.toHaveBeenCalled();
  });

  it("keeps an accepted completion open across expiry and confirms its settled response", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    loginExpiresAt = new Date(Date.now() + 1_000).toISOString();
    const resolveCompletion = hold(mocks.complete);
    const view = render(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    await submitCallback();
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
      resolveCompletion({ beforeETag: "before", afterETag: "after", firstConnection: true });
      completePending = false;
      statusFetching = true;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(screen.queryByRole("textbox", { name: "Browser URL" })).not.toBeInTheDocument();
    expect(mocks.error).not.toHaveBeenCalled();
    act(() => {
      statusFetching = false;
      status = { ...status, login_pending: false, login_result: "connected" };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="" onModelChange={vi.fn()} />);
    });
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining("ChatGPT connected"));
    expect(mocks.success).toHaveBeenCalledOnce();
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
    const refreshSettings = vi.fn();
    const panel = (
      <ChatGPTConnection model="" onModelChange={vi.fn()} onConnectionConfirmed={refreshSettings} />
    );
    const view = render(panel);
    await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    await submitCallback();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Could not complete sign-in");
    expect(mocks.refetch).toHaveBeenCalledOnce();
    expect(mocks.complete).toHaveBeenCalledOnce();
    expect(mocks.success).not.toHaveBeenCalled();
    expect(refreshSettings).not.toHaveBeenCalled();
    act(() => {
      status = { ...status, login_result: "connected", attempt_id: "fixture-state" };
      statusVersion++;
      view.rerender(
        <ChatGPTConnection
          model=""
          onModelChange={vi.fn()}
          onConnectionConfirmed={refreshSettings}
        />,
      );
    });
    expect(mocks.success).toHaveBeenCalledWith(
      "ChatGPT connected. Review your text model settings.",
    );
    expect(refreshSettings).toHaveBeenCalledOnce();
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

  it("disconnects an inactive account needing sign-in while keeping the active account and model", async () => {
    status = {
      ...status,
      active_client_id: "active-app",
      accounts: [
        { client_id: "active-app", email: "active@example.test", connected: true },
        { client_id: "inactive-app", email: "inactive@example.test", connected: false },
      ],
    };
    mocks.disconnect.mockResolvedValue({ revocation_confirmed: true });
    const onModelChange = vi.fn();
    const onSettingsTransitionStarted = vi.fn();
    render(
      <ChatGPTConnection
        model="gpt-6.1-sol"
        onModelChange={onModelChange}
        onSettingsTransitionStarted={onSettingsTransitionStarted}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Disconnect inactive@example.test" }));

    expect(mocks.disconnect).toHaveBeenCalledExactlyOnceWith("inactive-app");
    expect(mocks.select).not.toHaveBeenCalled();
    expect(onModelChange).not.toHaveBeenCalled();
    expect(onSettingsTransitionStarted).not.toHaveBeenCalled();
    expect(screen.getByRole("combobox", { name: "ChatGPT account" })).toHaveTextContent(
      "active@example.test",
    );
    expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("GPT-6.1 Sol");
    expect(screen.getByText("Using ChatGPT plan · active@example.test")).toBeInTheDocument();
    expect(mocks.success).toHaveBeenCalledWith("ChatGPT disconnected.");
  });

  it("selects an inactive paused account without restarting sign-in", async () => {
    status = {
      ...status,
      active_client_id: "active-app",
      accounts: [
        { client_id: "active-app", email: "active@example.test", connected: true },
        {
          client_id: "paused-app",
          email: "paused@example.test",
          connected: false,
          usage_limited: true,
        },
        {
          client_id: "expired-app",
          email: "expired@example.test",
          connected: false,
          usage_limited: true,
          needs_login: true,
        },
        {
          client_id: "blocked-app",
          email: "blocked@example.test",
          connected: false,
          usage_limited: true,
          plan_ineligible: true,
        },
      ],
    };
    const change = { beforeETag: "before", afterETag: "after" };
    const acceptTransition = vi.fn();
    const onSettingsTransitionStarted = vi.fn(() => acceptTransition);
    mocks.select.mockResolvedValue(change);
    render(
      <ChatGPTConnection
        model="gpt-6.1-sol"
        onModelChange={vi.fn()}
        onSettingsTransitionStarted={onSettingsTransitionStarted}
      />,
    );
    await userEvent.click(screen.getByRole("combobox", { name: "ChatGPT account" }));
    const pausedOption = screen.getByRole("option", {
      name: "paused@example.test · 2 (usage paused)",
    });
    expect(pausedOption).not.toHaveAttribute("aria-disabled", "true");
    expect(
      screen.getByRole("option", { name: "expired@example.test · 3 (sign-in required)" }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(
      screen.getByRole("option", { name: "blocked@example.test · 4 (plan sharing unavailable)" }),
    ).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(pausedOption);
    expect(mocks.select).toHaveBeenCalledExactlyOnceWith("paused-app");
    expect(acceptTransition).toHaveBeenCalledExactlyOnceWith(change);
    expect(mocks.start).not.toHaveBeenCalled();
  });

  it("offers credential renewal for a paused account that needs sign-in and keeps its pause", async () => {
    status = {
      ...status,
      active_client_id: "paused-app",
      accounts: [
        {
          client_id: "paused-app",
          email: "paused@example.test",
          connected: false,
          needs_login: true,
          usage_limited: true,
        },
      ],
    };
    const view = render(<ChatGPTConnection model="gpt-6.1-sol" onModelChange={vi.fn()} />);
    expect(screen.getByRole("combobox", { name: "ChatGPT account" })).toHaveTextContent(
      "paused@example.test · 1 (sign-in required)",
    );
    expect(screen.queryByRole("combobox", { name: "Model" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Reconnect paused@example.test" }));
    expect(mocks.start).toHaveBeenCalledExactlyOnceWith("paused-app");
    act(() => {
      status = {
        ...status,
        attempt_id: "fixture-state",
        login_result: "connected",
        accounts: [{ ...status.accounts[0]!, needs_login: false }],
      };
      statusVersion++;
      view.rerender(<ChatGPTConnection model="gpt-6.1-sol" onModelChange={vi.fn()} />);
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "ChatGPT account" })).toHaveTextContent(
      "paused@example.test · 1 (usage paused)",
    );
    expect(
      screen.queryByRole("button", { name: "Reconnect paused@example.test" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Model" })).toBeEnabled();
    expect(screen.queryByText("Using ChatGPT plan · paused@example.test")).not.toBeInTheDocument();
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

it("allows disconnecting a saved active account that needs sign-in", async () => {
  setAccessToken("chatgpt-test");
  setProfileId("admin-profile");
  status = {
    active_client_id: "app",
    accounts: [{ client_id: "app", email: "user@example.test", connected: false }],
    login_pending: false,
    login_result: "idle",
    attempt_id: "",
  };
  mocks.disconnect.mockResolvedValue({ revocation_confirmed: true });
  render(<ChatGPTConnection model="gpt-6.1-sol" onModelChange={vi.fn()} />);
  await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
  expect(mocks.disconnect).toHaveBeenCalledWith("app");
});
