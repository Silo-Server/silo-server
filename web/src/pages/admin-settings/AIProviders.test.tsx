import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AIProvider } from "@/hooks/queries/admin/aiProviders";

import { AIProvidersList } from "./AIProviders";

const mocks = vi.hoisted(() => ({ update: vi.fn() }));

const mutation = (mutateAsync = vi.fn()) => ({ mutateAsync, isPending: false });
vi.mock("@/hooks/queries/admin/aiProviders", () => ({
  useCreateAIProvider: () => mutation(),
  useUpdateAIProvider: () => mutation(mocks.update),
  useDeleteAIProvider: () => mutation(),
  useCheckAIProvider: () => mutation(),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

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
