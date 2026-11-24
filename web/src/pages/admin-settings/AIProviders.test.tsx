import { onlineManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as renderUI, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useAIProviders, type AIProvider } from "@/hooks/queries/admin/aiProviders";

import { AddAIProviderDialog, AIProvidersList } from "./AIProviders";

const mocks = vi.hoisted(() => ({ update: vi.fn(), v2: vi.fn() }));

// Creates go through the real mutation, so its pending state is the one the
// dialog reads; only the request is stubbed.
const mutation = (mutateAsync = vi.fn()) => ({ mutateAsync, isPending: false });
vi.mock("@/hooks/queries/admin/aiProviders", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/admin/aiProviders")>()),
  useUpdateAIProvider: () => mutation(mocks.update),
  useDeleteAIProvider: () => mutation(),
  useCheckAIProvider: () => mutation(),
}));
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: mocks.v2,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function render(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderUI(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

const provider: AIProvider = {
  id: "openai-1",
  type: "openai" as const,
  name: "OpenAI",
  base_url: "https://api.openai.com",
  has_api_key: true,
  capabilities: ["chat" as const, "embeddings" as const, "speech" as const],
  used_by: ["text" as const],
};
const types = [
  {
    type: "openai" as const,
    label: "OpenAI",
    default_base_url: "https://api.openai.com",
    capabilities: ["chat" as const, "embeddings" as const, "speech" as const],
    key_required: true,
  },
];

async function openProvider(user: ReturnType<typeof userEvent.setup>, shown = provider) {
  render(<AIProvidersList providers={[shown]} types={types} />);
  await user.click(screen.getByRole("button", { name: "Manage" }));
  return screen.getByRole("group", { name: "OpenAI" });
}

describe("AIProvidersList", () => {
  beforeEach(() => mocks.update.mockReset().mockResolvedValue(provider));

  // Omitting the key keeps the stored one; an empty string erases it. An edit
  // that never touched the key must not send one.
  it.each([
    ["an edit that leaves the key alone", false, undefined],
    ["a chosen clear", true, ""],
  ])("sends the key only for %s", async (_name, clear, wantKey) => {
    const user = userEvent.setup();
    const panel = await openProvider(user);
    await user.type(within(panel).getByLabelText("Name"), " work");
    if (clear) await user.click(within(panel).getByRole("button", { name: "Clear saved value" }));
    await user.click(within(panel).getByRole("button", { name: "Save" }));
    const [{ id, draft }] = mocks.update.mock.lastCall!;
    expect(id).toBe("openai-1");
    expect(draft.name).toBe("OpenAI work");
    expect(draft.api_key).toBe(wantKey);
  });

  // The server refuses to edit or delete a provider the deployment declares.
  it("locks a provider the deployment declares", async () => {
    const user = userEvent.setup();
    const panel = await openProvider(user, {
      ...provider,
      used_by: [],
      managed_by_env_var: "SILO_CONFIG_FILE",
    });
    expect(
      within(panel).getByText("Set by SILO_CONFIG_FILE in the server’s environment."),
    ).toBeInTheDocument();
    expect(within(panel).getByLabelText("Name")).toBeDisabled();
    expect(within(panel).queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: "Test" })).toBeEnabled();
  });
});

function ProviderList() {
  useAIProviders();
  return null;
}

describe("AddAIProviderDialog", () => {
  // A caller such as the setup wizard stages the new provider from onCreated.
  // Closing the dialog while Add is pending would let the caller move on
  // before that, so the dialog holds until the create settles.
  it("stays open until a pending add finishes", async () => {
    let finish!: (created: AIProvider) => void;
    mocks.v2
      .mockReset()
      .mockImplementation((operation: string) =>
        operation === "POST /api/v2/admin/ai/providers"
          ? new Promise((resolve) => (finish = resolve))
          : Promise.resolve({ providers: [], types: [] }),
      );
    const onOpenChange = vi.fn();
    const onCreated = vi.fn();
    const user = userEvent.setup();
    render(
      <AddAIProviderDialog
        open
        onOpenChange={onOpenChange}
        types={types}
        capability="embeddings"
        onCreated={onCreated}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.type(screen.getByLabelText("API key"), "sk-test");
    await user.click(screen.getByRole("button", { name: "Add provider" }));

    await user.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Close" })).not.toBeInTheDocument();

    finish({ ...provider, id: "openai-2", used_by: [] });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(onCreated).toHaveBeenCalledWith(expect.objectContaining({ id: "openai-2" }));
    expect(onCreated.mock.invocationCallOrder[0]).toBeLessThan(
      onOpenChange.mock.invocationCallOrder[0]!,
    );
  });

  // Offline, a create that paused instead of failing would hold the dialog
  // shut until the connection came back.
  it("fails an add made offline at once, so the dialog can close", async () => {
    mocks.v2.mockReset().mockRejectedValue(new TypeError("Failed to fetch"));
    onlineManager.setOnline(false);
    try {
      const onOpenChange = vi.fn();
      const user = userEvent.setup();
      render(
        <AddAIProviderDialog open onOpenChange={onOpenChange} types={types} onCreated={vi.fn()} />,
      );
      await user.click(screen.getByRole("button", { name: "Continue" }));
      await user.type(screen.getByLabelText("API key"), "sk-test");
      await user.click(screen.getByRole("button", { name: "Add provider" }));
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument(),
      );
      await user.keyboard("{Escape}");
      expect(onOpenChange).toHaveBeenCalledWith(false);
    } finally {
      onlineManager.setOnline(true);
    }
  });

  // Writes get no deadline by default. A create the server never answers
  // would hold the dialog shut for good, so it gives up and lets it close,
  // without also waiting out the provider list's refresh (mounted here, as on
  // the pages that open the dialog).
  it("lets the dialog close when the server never answers an add", async () => {
    const { v2 } = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
    mocks.v2.mockReset().mockImplementation(v2);
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: unknown, init?: RequestInit) =>
          new Promise((_resolve, reject) =>
            init?.signal?.addEventListener("abort", () => reject(init.signal?.reason)),
          ),
      ),
    );
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const onOpenChange = vi.fn();
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      render(
        <>
          <ProviderList />
          <AddAIProviderDialog open onOpenChange={onOpenChange} types={types} onCreated={vi.fn()} />
        </>,
      );
      await user.click(screen.getByRole("button", { name: "Continue" }));
      await user.type(screen.getByLabelText("API key"), "sk-test");
      await user.click(screen.getByRole("button", { name: "Add provider" }));
      expect(screen.queryByRole("button", { name: "Close" })).not.toBeInTheDocument();

      await vi.advanceTimersByTimeAsync(31_000);
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument(),
      );
      await user.keyboard("{Escape}");
      expect(onOpenChange).toHaveBeenCalledWith(false);
    } finally {
      vi.useRealTimers();
      vi.unstubAllGlobals();
    }
  });
});
