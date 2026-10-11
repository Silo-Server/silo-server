import { render as renderDOM, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AISettings from "./AISettings";

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
}));

const values: Record<string, string> = {};

const DEFAULT_VALUES: Record<string, string> = {
  "ai.base_url": "https://text.example.test",
  "ai.chat_model": "chat-model",
  "ai.asr_base_url": "",
  "ai.asr_model": "whisper-model",
  "ai.max_concurrent_jobs": "2",
  "subtitle_ai.base_url": "https://legacy.example.test",
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

let dirtyCount = 0;
let dirtyKeys: string[] = [];
const DEFAULT_SENSITIVE_CONFIGURED = ["subtitle_ai.api_key"];
let sensitiveConfigured: string[] = DEFAULT_SENSITIVE_CONFIGURED;
let reasoningLevels = ["low", "medium", "high", "xhigh", "max"];
let chatGPTStatus = {
  accounts: [] as { client_id: string; connected: boolean }[],
  active_client_id: "",
  login_pending: false,
  login_result: "idle",
};
let chatGPTModels: { id: string }[] = [];

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) => ({
  isLoading: false,
  getValue: (key: string) => values[key] ?? "",
  setValue: mocks.setValue,
  resetValue: mocks.resetValue,
  dirtyCount,
  dirtyKeys,
  isDirty: (key: string) => dirtyKeys.includes(key),
  isClearStaged: (key: string) => dirtyKeys.includes(key) && (values[key] ?? "") === "",
  save: mocks.save,
  discard: mocks.discard,
  isSaving: false,
  restartRequired: false,
  sensitiveConfigured,
  sensitiveManagedByEnv: [],
  sensitiveStatusReady: true,
  sensitiveStatusError: false,
  buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
}));

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
  useAdminAICapabilities: () => ({
    data: { reasoning_levels: reasoningLevels, chatgpt_sign_in: true },
  }),
  useChatGPTStatus: () => ({
    data: chatGPTStatus,
  }),
  useChatGPTModels: () => ({ data: { models: chatGPTModels } }),
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
  await user.click(within(tile).getByRole("button", { name: /Connect|Manage/ }));
  return screen.getByRole("group", { name });
}

describe("AISettings", () => {
  beforeEach(() => {
    localStorage.clear();
    dirtyCount = 0;
    dirtyKeys = [];
    sensitiveConfigured = DEFAULT_SENSITIVE_CONFIGURED;
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

  it("stages reasoning effort and clears it for the model default", async () => {
    const user = userEvent.setup();
    const view = render(<AISettings />);
    await openTile(user, "Text model");
    await user.click(screen.getByRole("combobox", { name: "Reasoning level" }));
    await user.click(screen.getByRole("option", { name: "High" }));
    expect(mocks.setValue).toHaveBeenCalledWith("ai.reasoning_effort", "high");
    values["ai.reasoning_effort"] = "high";
    view.rerender(
      <MemoryRouter>
        <AISettings />
      </MemoryRouter>,
    );
    await user.click(screen.getByRole("combobox", { name: "Reasoning level" }));
    await user.click(screen.getByRole("option", { name: "Provider default" }));
    expect(mocks.setValue).toHaveBeenCalledWith("ai.reasoning_effort", "");
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

  it("stages ChatGPT authentication from the provider selector", async () => {
    const user = userEvent.setup();
    render(<AISettings />);
    await openTile(user, "Text model");
    await user.click(screen.getByRole("combobox", { name: "Authentication" }));
    await user.click(screen.getByRole("option", { name: "ChatGPT plan" }));
    expect(mocks.setValue).toHaveBeenCalledWith("ai.auth_mode", "chatgpt");
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

  it("falls back to the legacy subtitle_ai values", async () => {
    const user = userEvent.setup();
    values["ai.base_url"] = "";
    values["ai.chat_model"] = "";

    render(<AISettings />);
    await openTile(user, "Text model");

    expect(screen.getByDisplayValue("https://legacy.example.test")).toBeInTheDocument();
    expect(screen.getByDisplayValue("legacy-chat-model")).toBeInTheDocument();
  });

  it.each(["shared", "dedicated"])(
    "allows an OpenRouter %s speech endpoint to be tested and enabled",
    async (endpoint) => {
      const user = userEvent.setup();
      values[endpoint === "shared" ? "ai.base_url" : "ai.asr_base_url"] =
        "https://openrouter.ai/api/v1";
      values["ai.asr_model"] = "openai/whisper-large-v3-turbo";
      mocks.checkConnection.mockResolvedValue({
        success: true,
        message: "Timestamped speech received.",
      });

      render(<AISettings />);
      expect(screen.queryByText("Cannot transcribe")).not.toBeInTheDocument();
      expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeEnabled();
      const tile = await openTile(user, "Speech-to-text");
      await user.click(within(tile).getByRole("button", { name: "Test speech-to-text" }));
      expect(mocks.checkConnection).toHaveBeenCalledWith(
        expect.objectContaining({ kind: "ai_transcription" }),
      );
    },
  );

  it("applies the OpenRouter transcription preset", async () => {
    const user = userEvent.setup();
    render(<AISettings />);
    await openTile(user, "Speech-to-text");
    await user.click(screen.getByRole("button", { name: "OpenRouter" }));
    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_base_url", "https://openrouter.ai/api/v1");
    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_model", "openai/whisper-large-v3-turbo");
  });

  it("applies a speech-to-text preset", async () => {
    const user = userEvent.setup();
    render(<AISettings />);
    await openTile(user, "Speech-to-text");

    await user.click(screen.getByRole("button", { name: "Groq - fast" }));

    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_base_url", "https://api.groq.com/openai");
    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_model", "whisper-large-v3-turbo");
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

  it("runs the text model check against the staged values", async () => {
    const user = userEvent.setup();
    mocks.checkConnection.mockResolvedValue({
      success: true,
      message: "Text connection verified.",
    });
    render(<AISettings />);
    await openTile(user, "Text model");

    await user.click(screen.getByRole("button", { name: "Test text model" }));

    expect(await screen.findByText(/Text connection verified\./)).toBeInTheDocument();
    expect(mocks.checkConnection).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "ai_chat" }),
    );
  });

  it("emptying a saved AI key reverts the draft instead of staging an empty value", async () => {
    const user = userEvent.setup();
    values["ai.api_key"] = "draft";
    render(<AISettings />);
    const tile = await openTile(user, "Text model");

    // No Replace step: the saved key is a masked, always-editable input.
    const input = within(tile).getByLabelText("API key");
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    await user.clear(input);

    // Staging "" would erase the stored key on the next save.
    expect(mocks.setValue).not.toHaveBeenCalledWith("ai.api_key", "");
    expect(mocks.resetValue).toHaveBeenCalledWith("ai.api_key");
  });

  it("clears the legacy key alongside the modern one", async () => {
    const user = userEvent.setup();
    render(<AISettings />);
    const tile = await openTile(user, "Text model");

    await user.click(within(tile).getByRole("button", { name: "Clear saved value" }));

    // An empty `ai.api_key` falls back to `subtitle_ai.api_key`, so clearing
    // only the modern key would leave the old secret in force.
    expect(mocks.setValue).toHaveBeenCalledWith("ai.api_key", "");
    expect(mocks.setValue).toHaveBeenCalledWith("subtitle_ai.api_key", "");
  });
  it("keeps the saved speech key when its staged clear is cancelled", async () => {
    const user = userEvent.setup();
    dirtyKeys = ["ai.asr_api_key"];
    dirtyCount = 1;
    values["ai.asr_api_key"] = "";
    sensitiveConfigured = ["ai.asr_api_key"];

    render(<AISettings />);
    const tile = screen.getByRole("group", { name: "Speech-to-text" });

    await user.click(within(tile).getByRole("button", { name: "Keep saved value" }));
    expect(mocks.resetValue).toHaveBeenCalledWith("ai.asr_api_key");
  });
});
