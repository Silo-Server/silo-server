import {
  act,
  fireEvent,
  render as renderDOM,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AISettings from "./AISettings";
import { fakeSettingsForm } from "./settingsForm.test-support";
import { setAccessToken, setProfileId } from "@/api/client";

// JSDOM omits the browser methods used by Radix Select.
if (!HTMLElement.prototype.hasPointerCapture) HTMLElement.prototype.hasPointerCapture = () => false;
if (!HTMLElement.prototype.scrollIntoView) HTMLElement.prototype.scrollIntoView = () => {};

// The page links to Recommendations with a router <Link>, so it needs a router.
function render(ui: React.ReactElement) {
  return renderDOM(<MemoryRouter>{ui}</MemoryRouter>);
}

const mocks = vi.hoisted(() => ({
  checkConnection: vi.fn(),
  discard: vi.fn(),
  save: vi.fn(),
  setValue: vi.fn(),
  resetValue: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  refetchChatGPTStatus: vi.fn(),
  refetchChatGPTModels: vi.fn(),
  refreshProviders: vi.fn(),
}));

const values: Record<string, string> = {};

const DEFAULT_VALUES: Record<string, string> = {
  "ai.text_provider_id": "text",
  "ai.chat_model": "chat-model",
  "ai.speech_provider_id": "router",
  "ai.asr_model": "whisper-model",
  "ai.max_concurrent_jobs": "2",
  "subtitle_ai.chat_model": "legacy-chat-model",
  "subtitle_ai.max_concurrent_jobs": "3",
  "subtitle_ai.enabled": "true",
  "subtitle_ai.transcribe_enabled": "false",
  "subtitle_ai.batch_size": "40",
  "subtitle_ai.context_neighbors": "2",
  "subtitle_ai.asr_chunk_seconds": "600",
  "subtitle_ai.transcribe_quota_jobs": "0",
  "subtitle_ai.transcribe_quota_period": "day",
  "metadata_ai.enabled": "false",
  "metadata_ai.on_view": "button",
};

const DEFAULT_PROVIDERS = [
  {
    id: "text",
    type: "custom",
    name: "Text box",
    base_url: "https://text.example.test",
    has_api_key: true,
    capabilities: ["chat"],
    used_by: ["text"],
  },
  {
    id: "router",
    type: "openrouter",
    name: "OpenRouter",
    base_url: "https://openrouter.ai/api/v1",
    has_api_key: true,
    capabilities: ["chat", "speech"],
    used_by: ["speech"],
  },
];
let providers = DEFAULT_PROVIDERS;

let dirtyCount = 0;
let dirtyKeys: string[] = [];
let sensitiveConfigured: string[] = [];
let capabilitiesFetching = false;
let modelReasoningLevels: Record<string, string[]> = {};
let reasoningLevels = ["low", "medium", "high", "xhigh", "max"];
let chatGPTStatus = {
  accounts: [] as {
    client_id: string;
    connected: boolean;
    needs_login?: boolean;
    usage_limited?: boolean;
    plan_ineligible?: boolean;
  }[],
  active_client_id: "",
  login_pending: false,
  login_result: "idle",
};
let chatGPTModels: { id: string }[] = [];

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) =>
  fakeSettingsForm(values, {
    dirty: dirtyKeys,
    dirtyCount,
    setValue: mocks.setValue,
    resetValue: mocks.resetValue,
    save: mocks.save,
    discard: mocks.discard,
    sensitiveConfigured,
    buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
  }),
);

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (options: { keys: string[] }) => useSettingsFormMock(options),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set(["ai.max_concurrent_jobs"]),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: () => ({
    mutateAsync: mocks.checkConnection,
    isPending: false,
  }),
}));

vi.mock("@/hooks/queries/admin/chatgpt", () => ({
  useAdminAICapabilities: (model: string) => ({
    data: {
      reasoning_levels: modelReasoningLevels[model] ?? reasoningLevels,
      chatgpt_sign_in: true,
    },
    isFetching: capabilitiesFetching,
    isSuccess: true,
  }),
  useChatGPTStatus: () => ({
    data: chatGPTStatus,
    refetch: mocks.refetchChatGPTStatus,
  }),
  useChatGPTModels: () => ({
    data: { models: chatGPTModels },
    refetch: mocks.refetchChatGPTModels,
  }),
}));
vi.mock("@/hooks/queries/admin/aiProviders", () => ({
  useAIProviders: () => ({ data: { providers, types: [] }, isLoading: false }),
  useAIProviderModels: () => ({ data: { success: true, message: "", models: [] } }),
  useRefreshAIProviders: () => mocks.refreshProviders,
}));
vi.mock("./ChatGPTConnection", () => ({
  ChatGPTConnection: () => <div>ChatGPT connection controls</div>,
}));

vi.mock("sonner", () => ({
  toast: {
    error: mocks.toastError,
    success: mocks.toastSuccess,
  },
}));

/** Opens a model tile's connect panel. */
async function openTile(user: ReturnType<typeof userEvent.setup>, name: string) {
  const tile = screen.getByRole("group", { name });
  await user.click(within(tile).getByRole("button", { name: /Set up|Manage/ }));
  return screen.getByRole("group", { name });
}

describe("AISettings", () => {
  afterEach(() => vi.useRealTimers());
  beforeEach(() => {
    localStorage.clear();
    setAccessToken("ai-settings-test");
    setProfileId("profile-a");
    dirtyCount = 0;
    dirtyKeys = [];
    sensitiveConfigured = [];
    providers = DEFAULT_PROVIDERS;
    capabilitiesFetching = false;
    modelReasoningLevels = {};
    reasoningLevels = ["low", "medium", "high", "xhigh", "max"];
    chatGPTStatus = {
      accounts: [],
      active_client_id: "",
      login_pending: false,
      login_result: "idle",
    };
    chatGPTModels = [];
    for (const mock of Object.values(mocks)) mock.mockReset();
    for (const key of Object.keys(values)) delete values[key];
    Object.assign(values, DEFAULT_VALUES);
  });

  it("opens the text model's Advanced section when the model drops the saved level", () => {
    values["ai.reasoning_effort"] = "high";
    dirtyKeys = ["ai.chat_model"];
    dirtyCount = 1;
    reasoningLevels = [];
    render(<AISettings />);

    // Save refuses this level, so the field it names has to be on screen.
    const tile = screen.getByRole("group", { name: "Text model" });
    expect(within(tile).getByRole("combobox", { name: "Reasoning level" })).toHaveTextContent(
      "high (unsupported by this model)",
    );
  });

  it("rejects a saved reasoning effort after choosing a model without reasoning", async () => {
    values["ai.reasoning_effort"] = "high";
    dirtyKeys = ["ai.chat_model"];
    reasoningLevels = [];
    dirtyCount = 1;
    render(<AISettings />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.save).not.toHaveBeenCalled();
    expect(mocks.toastError).toHaveBeenCalledWith(
      "Choose a reasoning level supported by the selected model.",
    );
  });

  it("saves against backend validation while capabilities still describe the previous model", async () => {
    vi.useFakeTimers();
    values["ai.chat_model"] = "gpt-4o-mini";
    values["ai.reasoning_effort"] = "high";
    modelReasoningLevels = { "gpt-4o-mini": [], "gpt-6-luna": ["high"] };
    dirtyKeys = ["ai.chat_model"];
    dirtyCount = 1;
    const view = render(<AISettings />);
    values["ai.chat_model"] = "gpt-6-luna";
    view.rerender(
      <MemoryRouter>
        <AISettings />
      </MemoryRouter>,
    );
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Save" }));
    });
    expect(mocks.save).toHaveBeenCalledOnce();
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("defers reasoning validation to the backend while the capabilities request is fetching", async () => {
    values["ai.reasoning_effort"] = "high";
    dirtyKeys = ["ai.reasoning_effort"];
    dirtyCount = 1;
    reasoningLevels = [];
    capabilitiesFetching = true;
    render(<AISettings />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.save).toHaveBeenCalledOnce();
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("requires a connected account and a model from that account before saving ChatGPT", async () => {
    values["ai.auth_mode"] = "chatgpt";
    dirtyCount = 1;
    dirtyKeys = ["ai.auth_mode"];
    const view = render(<AISettings />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.save).not.toHaveBeenCalled();
    chatGPTStatus = {
      ...chatGPTStatus,
      active_client_id: "app",
      accounts: [{ client_id: "app", connected: true }],
    };
    view.rerender(
      <MemoryRouter>
        <AISettings />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.save).not.toHaveBeenCalled();
    chatGPTModels = [{ id: values["ai.chat_model"]! }];
    view.rerender(
      <MemoryRouter>
        <AISettings />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.save).toHaveBeenCalledOnce();
  });

  it.each([
    ["subtitle_ai.enabled", "false"],
    ["metadata_ai.enabled", "false"],
    ["ai.asr_model", "updated-speech-model"],
    ["subtitle_ai.transcribe_quota_jobs", "5"],
    ["ai.max_concurrent_jobs", "3"],
  ])("saves %s while ChatGPT is disconnected", async (key, value) => {
    values["ai.auth_mode"] = "chatgpt";
    values["ai.reasoning_effort"] = "high";
    reasoningLevels = [];
    values[key] = value;
    dirtyCount = 1;
    dirtyKeys = [key];
    render(<AISettings />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.save).toHaveBeenCalledOnce();
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("keeps the save callback rejection at the Save button boundary", async () => {
    dirtyCount = 1;
    dirtyKeys = ["ai.chat_model"];
    mocks.save.mockRejectedValue(new Error("412 precondition failed"));
    render(<AISettings />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(mocks.save).toHaveBeenCalledOnce();
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
  });

  it.each([
    ["an untouched setting", false, "legacy-chat-model"],
    ["an explicitly cleared draft", true, ""],
  ])("resolves the legacy chat model for %s", async (_name, edited, expectedModel) => {
    const user = userEvent.setup();
    values["ai.chat_model"] = "";
    dirtyKeys = edited ? ["ai.chat_model"] : [];
    dirtyCount = dirtyKeys.length;

    render(<AISettings />);
    const tile = edited
      ? screen.getByRole("group", { name: "Text model" })
      : await openTile(user, "Text model");

    expect(within(tile).getByLabelText("Model")).toHaveValue(expectedModel);
    if (edited) {
      expect(within(tile).getByRole("button", { name: "Test text model" })).toBeDisabled();
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(mocks.save).not.toHaveBeenCalled();
      expect(mocks.toastError).toHaveBeenCalledWith("Choose a text provider and a model.");
    }
  });

  it("tests and enables speech on a provider that serves it", async () => {
    const user = userEvent.setup();
    mocks.checkConnection.mockResolvedValue({
      success: true,
      message: "Timestamped speech received.",
    });

    render(<AISettings />);
    expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeEnabled();
    const tile = await openTile(user, "Speech-to-text");
    await user.click(within(tile).getByRole("button", { name: "Test speech-to-text" }));
    expect(mocks.checkConnection).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "ai_transcription" }),
    );
  });

  it.each([
    ["no provider chosen", ""],
    ["a provider that only serves chat", "text"],
    ["a removed provider", "gone"],
  ])("does not count speech ready with %s", (_name, providerID) => {
    values["ai.speech_provider_id"] = providerID;

    render(<AISettings />);

    expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeDisabled();
    // The text job keeps its own provider.
    expect(screen.getByRole("switch", { name: "Translate descriptions" })).toBeEnabled();
  });

  it("counts nothing ready on a fresh install without providers", () => {
    providers = [];
    values["ai.text_provider_id"] = "";
    values["ai.speech_provider_id"] = "";
    values["subtitle_ai.enabled"] = "false";

    render(<AISettings />);

    const text = screen.getByRole("group", { name: "Text model" });
    expect(within(text).getByText("Choose a provider and a model")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Translate subtitles" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeDisabled();
  });

  it("refuses to save a feature switch without a text provider", async () => {
    values["ai.text_provider_id"] = "";
    values["metadata_ai.enabled"] = "true";
    dirtyKeys = ["metadata_ai.enabled"];
    dirtyCount = 1;

    render(<AISettings />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(mocks.save).not.toHaveBeenCalled();
    expect(mocks.toastError).toHaveBeenCalledWith("Choose a text provider and a model.");
  });

  it("chooses the ChatGPT plan by authentication, keeping the API-key provider", async () => {
    const user = userEvent.setup();
    render(<AISettings />);
    const tile = await openTile(user, "Text model");

    await user.click(within(tile).getByRole("combobox", { name: "Provider" }));
    await user.click(screen.getByRole("option", { name: "ChatGPT plan" }));
    expect(mocks.setValue).toHaveBeenCalledWith("ai.auth_mode", "chatgpt");
    expect(mocks.setValue).not.toHaveBeenCalledWith("ai.text_provider_id", expect.anything());

    mocks.setValue.mockReset();
    await user.click(within(tile).getByRole("combobox", { name: "Provider" }));
    await user.click(screen.getByRole("option", { name: "OpenRouter" }));
    expect(mocks.setValue).toHaveBeenCalledWith("ai.auth_mode", "api_key");
    expect(mocks.setValue).toHaveBeenCalledWith("ai.text_provider_id", "router");
  });

  it.each([
    ["with translation off", "false", true],
    ["while subtitles still translate", "true", false],
  ])("saves None for the text provider %s", async (_name, translating, saves) => {
    values["ai.text_provider_id"] = "";
    values["subtitle_ai.enabled"] = translating;
    values["metadata_ai.enabled"] = "false";
    dirtyKeys = ["ai.text_provider_id"];
    dirtyCount = 1;

    render(<AISettings />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    if (saves) expect(mocks.save).toHaveBeenCalledOnce();
    else
      expect(mocks.toastError).toHaveBeenCalledWith(
        "Turn off subtitle and description translation before removing the text provider.",
      );
  });

  // Releasing a job's provider is how an unused provider becomes removable.
  it.each([
    ["Text model", "ai.text_provider_id"],
    ["Speech-to-text", "ai.speech_provider_id"],
  ])("clears the %s provider when None is chosen", async (tileName, key) => {
    const user = userEvent.setup();
    render(<AISettings />);
    const tile = await openTile(user, tileName);

    await user.click(within(tile).getByRole("combobox", { name: "Provider" }));
    await user.click(screen.getByRole("option", { name: "None" }));

    expect(mocks.setValue).toHaveBeenCalledWith(key, "");
  });

  it("blocks turning on a feature whose model cannot serve it", () => {
    // Whitespace leaves the speech model unconfigured.
    values["ai.asr_model"] = " ";

    render(<AISettings />);

    expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeDisabled();
  });

  it("still lets an enabled feature be turned off after its model degrades", () => {
    values["ai.asr_model"] = " ";
    values["subtitle_ai.transcribe_enabled"] = "true";

    render(<AISettings />);

    expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeEnabled();
  });

  it.each([
    ["ai.max_concurrent_jobs", "1.5", "Max concurrent jobs must be a positive whole number."],
    ["subtitle_ai.batch_size", "2abc", "Subtitle batch size must be a positive whole number."],
    [
      "subtitle_ai.context_neighbors",
      "1.5",
      "Subtitle context lines must be zero or a positive whole number.",
    ],
    [
      "subtitle_ai.asr_chunk_seconds",
      "120seconds",
      "Transcription chunk length must be between 60 and 600 seconds.",
    ],
    // parseInt would stop at the first non-digit and let "5abc" through as 5,
    // saving a quota nobody typed.
    [
      "subtitle_ai.transcribe_quota_jobs",
      "5abc",
      "Transcription limit must be zero or a positive whole number.",
    ],
    [
      "subtitle_ai.transcribe_quota_jobs",
      "1.5",
      "Transcription limit must be zero or a positive whole number.",
    ],
  ])("rejects malformed integer input for %s", async (key, malformedValue, message) => {
    const user = userEvent.setup();
    dirtyCount = 1;
    values[key] = malformedValue;
    render(<AISettings />);

    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(mocks.toastError).toHaveBeenCalledWith(message);
    expect(mocks.save).not.toHaveBeenCalled();
  });

  it.each([true, false])(
    "refreshes paused usage after a text check returns success=%s",
    async (success) => {
      values["ai.auth_mode"] = "chatgpt";
      chatGPTStatus = {
        ...chatGPTStatus,
        active_client_id: "paused-app",
        accounts: [{ client_id: "paused-app", connected: false, usage_limited: true }],
      };
      chatGPTModels = [{ id: values["ai.chat_model"]! }];
      mocks.checkConnection.mockResolvedValue({
        success,
        message: success ? "Text connection verified." : "ChatGPT usage is still limited.",
      });
      mocks.refetchChatGPTStatus.mockImplementation(async () => {
        chatGPTStatus = {
          ...chatGPTStatus,
          accounts: [{ client_id: "paused-app", connected: success, usage_limited: !success }],
        };
      });
      const view = render(<AISettings />);
      await openTile(userEvent.setup(), "Text model");
      expect(screen.getByRole("button", { name: "Test text model" })).toBeEnabled();
      expect(screen.getByRole("switch", { name: "Translate descriptions" })).toBeDisabled();

      await userEvent.click(screen.getByRole("button", { name: "Test text model" }));
      await waitFor(() => expect(mocks.refetchChatGPTStatus).toHaveBeenCalledOnce());
      expect(mocks.refetchChatGPTModels).toHaveBeenCalledOnce();
      expect(mocks.checkConnection).toHaveBeenCalledWith(
        expect.objectContaining({ kind: "ai_chat" }),
      );
      view.rerender(
        <MemoryRouter>
          <AISettings />
        </MemoryRouter>,
      );
      const descriptions = screen.getByRole("switch", { name: "Translate descriptions" });
      if (success) expect(descriptions).toBeEnabled();
      else expect(descriptions).toBeDisabled();
      expect(screen.getByRole("button", { name: "Test text model" })).toBeEnabled();
    },
  );

  it("can test a paused saved model without a loaded catalog and refreshes after a request failure", async () => {
    values["ai.auth_mode"] = "chatgpt";
    chatGPTStatus = {
      ...chatGPTStatus,
      active_client_id: "paused-app",
      accounts: [{ client_id: "paused-app", connected: false, usage_limited: true }],
    };
    mocks.checkConnection.mockRejectedValue(new Error("Connection interrupted."));
    render(<AISettings />);
    await openTile(userEvent.setup(), "Text model");
    await userEvent.click(screen.getByRole("button", { name: "Test text model" }));
    expect(await screen.findByText("Connection interrupted.")).toBeInTheDocument();
    expect(mocks.refetchChatGPTStatus).toHaveBeenCalledOnce();
    expect(mocks.refetchChatGPTModels).toHaveBeenCalledOnce();
    expect(screen.getByRole("switch", { name: "Translate descriptions" })).toBeDisabled();
  });

  it("keeps paused tests disabled until missing credentials and model configuration are restored", async () => {
    values["ai.auth_mode"] = "chatgpt";
    chatGPTStatus = {
      ...chatGPTStatus,
      active_client_id: "paused-app",
      accounts: [
        { client_id: "paused-app", connected: false, usage_limited: true, needs_login: true },
      ],
    };
    const view = render(<AISettings />);
    await openTile(userEvent.setup(), "Text model");
    expect(screen.getByRole("button", { name: "Test text model" })).toBeDisabled();
    chatGPTStatus.accounts[0]!.needs_login = false;
    values["ai.chat_model"] = "";
    values["subtitle_ai.chat_model"] = "";
    view.rerender(
      <MemoryRouter>
        <AISettings />
      </MemoryRouter>,
    );
    expect(screen.getByRole("button", { name: "Test text model" })).toBeDisabled();
    values["ai.chat_model"] = "chat-model";
    view.rerender(
      <MemoryRouter>
        <AISettings />
      </MemoryRouter>,
    );
    expect(screen.getByRole("button", { name: "Test text model" })).toBeEnabled();
  });

  it("ignores a paused test result and cache refresh after the initiating profile changes", async () => {
    values["ai.auth_mode"] = "chatgpt";
    chatGPTStatus = {
      ...chatGPTStatus,
      active_client_id: "paused-app",
      accounts: [{ client_id: "paused-app", connected: false, usage_limited: true }],
    };
    let resolveCheck!: (value: { success: boolean; message: string }) => void;
    mocks.checkConnection.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveCheck = resolve;
        }),
    );
    render(<AISettings />);
    await openTile(userEvent.setup(), "Text model");
    await userEvent.click(screen.getByRole("button", { name: "Test text model" }));
    setProfileId("profile-b");
    await act(async () => resolveCheck({ success: true, message: "Old profile verified." }));
    expect(mocks.refetchChatGPTStatus).not.toHaveBeenCalled();
    expect(mocks.refetchChatGPTModels).not.toHaveBeenCalled();
    expect(screen.queryByText(/Old profile verified/)).not.toBeInTheDocument();
  });
});
