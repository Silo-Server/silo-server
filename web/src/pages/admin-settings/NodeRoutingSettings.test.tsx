import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { NodeRoutingSettings } from "./NodeRoutingSettings";

const useSettingsFormMock = vi.fn();
const useAdminNodesMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: () => useAdminNodesMock(),
}));

const DEFAULT_ROUTING = {
  "playback.routing.direct_play_egress": "prefer_proxy",
  "playback.routing.remux_execution": "prefer_transcode",
  "playback.routing.remux_egress": "prefer_proxy",
  "playback.routing.video_transcode_execution": "prefer_transcode",
  "playback.routing.video_transcode_egress": "prefer_proxy",
};

function makeForm(values: Record<string, string>, dirty: string[] = []) {
  const dirtyKeys = new Set(dirty);
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    getPersistedValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    isDirty: (key: string) => dirtyKeys.has(key),
    dirtyCount: dirtyKeys.size,
    dirtyKeys: [...dirtyKeys],
    save: vi.fn().mockResolvedValue(undefined),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
  };
}

function transcodeNode(overrides: Record<string, unknown> = {}) {
  return { id: 1, name: "node-1", type: "transcode", enabled: true, healthy: true, ...overrides };
}

// The section guards its own staged edits with `useBlocker`, which needs a
// data router.
function renderRouting() {
  const router = createMemoryRouter([{ path: "/admin/nodes", element: <NodeRoutingSettings /> }], {
    initialEntries: ["/admin/nodes"],
  });
  return render(<RouterProvider router={router} />);
}

beforeEach(() => {
  useSettingsFormMock.mockReset();
  useAdminNodesMock.mockReturnValue({ data: [transcodeNode()], isSuccess: true });
});

describe("NodeRoutingSettings", () => {
  it("manages only the routing keys", () => {
    useSettingsFormMock.mockReturnValue(makeForm(DEFAULT_ROUTING));
    renderRouting();

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toEqual(Object.keys(DEFAULT_ROUTING));
  });

  it("stages every primitive setting when a preset is selected", async () => {
    const form = makeForm(DEFAULT_ROUTING);
    useSettingsFormMock.mockReturnValue(form);

    renderRouting();
    await userEvent.click(screen.getByRole("button", { name: "GPU offload" }));

    expect(form.setValue.mock.calls).toEqual([
      ["playback.routing.direct_play_egress", "prefer_api"],
      ["playback.routing.remux_execution", "prefer_api"],
      ["playback.routing.remux_egress", "prefer_api"],
      ["playback.routing.video_transcode_execution", "prefer_worker"],
      ["playback.routing.video_transcode_egress", "prefer_proxy"],
    ]);
    expect(form.save).not.toHaveBeenCalled();
  });

  it("warns when hard routes lack nodes or universal client-origin support", () => {
    useAdminNodesMock.mockReturnValue({
      data: [transcodeNode({ healthy: false })],
      isSuccess: true,
    });
    useSettingsFormMock.mockReturnValue(
      makeForm({
        ...DEFAULT_ROUTING,
        "playback.routing.direct_play_egress": "proxy_only",
        "playback.routing.remux_execution": "worker_only",
      }),
    );

    renderRouting();

    expect(screen.getByText(/no healthy supporting node/)).toBeInTheDocument();
    expect(
      screen.getByText(/requires every native client to support authorized media origins/),
    ).toBeInTheDocument();
  });

  it("saves staged routing through its own bar", async () => {
    const form = makeForm(DEFAULT_ROUTING, ["playback.routing.remux_egress"]);
    useSettingsFormMock.mockReturnValue(form);

    renderRouting();
    await userEvent.click(screen.getByRole("button", { name: /^Save/ }));

    expect(form.save).toHaveBeenCalledTimes(1);
  });
});
