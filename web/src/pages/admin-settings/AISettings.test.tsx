import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { stubRadixDom } from "@/pages/adminPage.test-support";

import AISettings from "./AISettings";

stubRadixDom();

const { settings } = vi.hoisted(() => ({
  settings: { current: {} as Record<string, string> },
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: settings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: { configured: [], managed_by_env: [] }, isError: false }),
  useUpdateServerSettings: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCheckAdminSettingsConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/admin/aiProviders", () => ({
  useAIProviders: () => ({
    data: {
      providers: [
        { id: "oa", name: "OpenAI", type: "openai", capabilities: ["chat"] },
        { id: "other", name: "Other", type: "anthropic", capabilities: ["chat"] },
      ],
      types: [],
    },
    isLoading: false,
  }),
  useRefreshAIProviders: () => vi.fn(),
  useAIProviderModels: () => ({ data: { models: [] } }),
}));
vi.mock("@/hooks/queries/admin/chatgpt", () => ({
  useAdminAICapabilities: () => ({
    data: {
      chatgpt_sign_in: true,
      reasoning_levels: [],
      openai_service_tiers: ["default", "priority"],
    },
    isSuccess: true,
    isFetching: false,
  }),
  useChatGPTStatus: () => ({ data: undefined }),
  useChatGPTModels: () => ({ data: undefined }),
}));
vi.mock("@/hooks/useRestartKeys", () => ({ useRestartKeys: () => ({ has: () => false }) }));
vi.mock("./AIProviders", () => ({ AIProvidersList: () => null }));
vi.mock("./ChatGPTConnection", () => ({ ChatGPTConnection: () => null }));

const page = (
  <MemoryRouter>
    <AISettings />
  </MemoryRouter>
);

async function pick(label: string, option: string) {
  await userEvent.click(screen.getByRole("combobox", { name: label }));
  await userEvent.click(screen.getByRole("option", { name: option }));
}

async function openTextTile() {
  await userEvent.click(screen.getAllByRole("button", { name: "Manage" })[0]!);
}

describe("AI text model edits stay where the admin can see them", () => {
  beforeEach(() => {
    localStorage.clear();
    settings.current = {
      "ai.auth_mode": "api_key",
      "ai.text_provider_id": "oa",
      "ai.chat_model": "gpt-test",
    };
  });

  it("keeps a chosen service tier in view after switching to a provider without tiers", async () => {
    render(page);
    await openTextTile();
    // The text tile's own Advanced section comes first.
    const advanced = screen.getAllByRole("button", { name: /Advanced/ })[0]!;
    if (advanced.getAttribute("aria-expanded") !== "true") await userEvent.click(advanced);
    await pick("Service tier", "Fast/Priority");
    await pick("Provider", "Other");

    expect(screen.getByRole("combobox", { name: "Service tier" })).toHaveTextContent(
      "Fast/Priority",
    );
  });

  it("takes back a staged provider when the ChatGPT plan is chosen", async () => {
    render(page);
    await openTextTile();
    await pick("Provider", "Other");
    await pick("Provider", "ChatGPT plan");

    // Only the switch to the plan is staged.
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
  });
});
