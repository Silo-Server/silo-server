import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

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

const provider = {
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

async function openProvider(user: ReturnType<typeof userEvent.setup>) {
  render(<AIProvidersList providers={[provider]} types={types} />);
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
});
