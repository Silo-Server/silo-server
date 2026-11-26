import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { stubRadixDom } from "@/pages/adminPage.test-support";

import { FeaturesStep } from "./FeaturesStep";

stubRadixDom();

const { settings, saved } = vi.hoisted(() => ({
  settings: { current: {} as Record<string, string> },
  saved: { current: undefined as Record<string, string> | undefined },
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: settings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: { configured: [], managed_by_env: [] }, isError: false }),
  useUpdateServerSettings: () => ({
    mutateAsync: async (values: Record<string, string>) => {
      saved.current = values;
      return { values };
    },
    isPending: false,
  }),
  useCheckAdminSettingsConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/admin/aiProviders", () => ({
  useAIProviders: () => ({
    data: {
      providers: [{ id: "p1", name: "Embedder", type: "openai", capabilities: ["embeddings"] }],
      types: [],
    },
    isLoading: false,
  }),
  useAIProviderModels: () => ({ data: { models: [] } }),
  useAIProviderCreatePending: () => false,
}));
vi.mock("../WizardContext", () => ({
  useWizardContext: () => ({ markDone: vi.fn(), setSummary: vi.fn() }),
}));

async function continueStep() {
  await userEvent.click(screen.getByRole("button", { name: "Continue" }));
}

describe("FeaturesStep keeps unsaved edits in view", () => {
  beforeEach(() => {
    saved.current = undefined;
    settings.current = {
      "download.enabled": "false",
      "download.server_bandwidth_mbps": "0",
      "download.user_bandwidth_mbps": "0",
      "markers.mode": "off",
      "markers.lazy_playback": "true",
      "markers.online_storage": "stored",
      "markers.share_by_default": "true",
      "recommendations.enabled": "false",
      "notifications.apple_push_delivery_enabled": "true",
      "notifications.android_push_delivery_enabled": "true",
    };
  });

  it("shows a bandwidth edit after Downloads is turned off", async () => {
    render(<FeaturesStep />);
    const downloads = screen.getByRole("switch", { name: "Downloads" });
    await userEvent.click(downloads);
    const bandwidth = screen.getByRole("spinbutton", { name: "Total download bandwidth" });
    await userEvent.clear(bandwidth);
    await userEvent.type(bandwidth, "50");
    await userEvent.click(downloads);

    expect(screen.getByRole("spinbutton", { name: "Total download bandwidth" })).toHaveValue(50);
    expect(
      screen.queryByRole("spinbutton", { name: "Per-person download bandwidth" }),
    ).not.toBeInTheDocument();
  });

  it("shows a recommendation provider edit after Recommendations is turned off", async () => {
    render(<FeaturesStep />);
    const recommendations = screen.getByRole("switch", { name: "Recommendations" });
    await userEvent.click(recommendations);
    await userEvent.click(screen.getByRole("combobox", { name: "Embedding provider" }));
    await userEvent.click(screen.getByRole("option", { name: "Embedder" }));
    await userEvent.click(recommendations);

    expect(screen.getByRole("combobox", { name: "Embedding provider" })).toHaveTextContent(
      "Embedder",
    );
    expect(screen.queryByRole("textbox", { name: "Model" })).not.toBeInTheDocument();
  });

  it("saves nothing after online markers are turned on and off again", async () => {
    render(<FeaturesStep />);
    const online = screen.getByRole("switch", { name: "Skip markers from TheIntroDB and Silo" });
    await userEvent.click(online);
    await userEvent.click(screen.getByRole("combobox", { name: "Save online markers" }));
    await userEvent.click(screen.getByRole("option", { name: "Fetch when needed" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Save online markers" }));
    await userEvent.click(screen.getByRole("option", { name: "Save to library" }));
    await userEvent.click(online);
    await continueStep();

    expect(saved.current).toBeUndefined();
  });

  it("keeps the online storage edit in view after online markers are turned off", async () => {
    // Lazy playback saved off: the storage choice left in view doesn't turn
    // it on while every source is off.
    settings.current["markers.lazy_playback"] = "false";
    render(<FeaturesStep />);
    const online = screen.getByRole("switch", { name: "Skip markers from TheIntroDB and Silo" });
    await userEvent.click(online);
    await userEvent.click(screen.getByRole("combobox", { name: "Save online markers" }));
    await userEvent.click(screen.getByRole("option", { name: "Fetch when needed" }));
    await userEvent.click(online);

    expect(screen.getByRole("combobox", { name: "Save online markers" })).toHaveTextContent(
      "Fetch when needed",
    );
    await continueStep();
    expect(saved.current).toEqual({ "markers.online_storage": "on_demand" });
  });

  it("stages lazy playback from the standing edits, not from the order they were made", async () => {
    settings.current["markers.mode"] = "local";
    render(<FeaturesStep />);
    const online = screen.getByRole("switch", { name: "Skip markers from TheIntroDB and Silo" });
    const local = screen.getByRole("switch", { name: "Detect markers on this server" });
    await userEvent.click(online);
    await userEvent.click(screen.getByRole("combobox", { name: "Save online markers" }));
    await userEvent.click(screen.getByRole("option", { name: "Fetch when needed" }));
    await userEvent.click(local);
    await userEvent.click(online);
    await userEvent.click(local);
    await continueStep();

    expect(saved.current).toEqual({ "markers.online_storage": "on_demand" });
  });

  it("saves on-demand storage without turning lazy playback on", async () => {
    settings.current = {
      ...settings.current,
      "markers.mode": "both",
      "markers.lazy_playback": "false",
    };
    render(<FeaturesStep />);
    await userEvent.click(screen.getByRole("combobox", { name: "Save online markers" }));
    await userEvent.click(screen.getByRole("option", { name: "Fetch when needed" }));
    await continueStep();

    expect(saved.current).toEqual({ "markers.online_storage": "on_demand" });
  });

  it("saves nothing after push is turned off and on again", async () => {
    // Only one platform delivers; the switch reads on.
    settings.current["notifications.android_push_delivery_enabled"] = "false";
    render(<FeaturesStep />);
    const push = screen.getByRole("switch", { name: "Mobile push notifications" });
    await userEvent.click(push);
    await userEvent.click(push);
    await continueStep();

    expect(saved.current).toBeUndefined();
  });
});
