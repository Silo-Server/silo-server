// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { DirectConnectionsHostStatus } from "@/hooks/queries/admin/directConnections";

import { DirectConnectionsSection } from "./DirectConnectionsSection";

type Knob = DirectConnectionsHostStatus["knobs"][number];
type Feature = DirectConnectionsHostStatus["features"][number];
type Listener = NonNullable<DirectConnectionsHostStatus["listeners"]>[number];

const state = vi.hoisted(() => ({
  host: undefined as unknown,
  persistedEnabled: "true",
  dirtyCount: 0,
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: () => ({
    getValue: () => "",
    getPersistedValue: (key: string) =>
      key === "direct_connections.enabled" ? state.persistedEnabled : "",
    setValue: vi.fn(),
    isDirty: () => false,
    dirtyCount: state.dirtyCount,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    envVar: () => undefined,
  }),
}));
vi.mock("@/hooks/queries/admin/directConnections", () => ({
  useAdminDirectConnectionsStatus: () => ({
    isLoading: false,
    isError: false,
    data: { enabled: true, hosts: [state.host] },
  }),
  useRefreshDirectConnections: () => ({ isPending: false, isError: false, mutate: vi.fn() }),
}));

function show(overrides: Partial<DirectConnectionsHostStatus>) {
  state.host = {
    host: { id: "api", name: "API", role: "api" },
    checked_at: "2026-11-01T10:00:00.000Z",
    features: [],
    knobs: [],
    ...overrides,
  } satisfies DirectConnectionsHostStatus;
  render(<DirectConnectionsSection />);
}

/** The row (term, value, and notes) that holds a label. */
function row(label: string) {
  const container = screen.getByText(label).closest("div.grid");
  if (!(container instanceof HTMLElement)) throw new Error(`no row for ${label}`);
  return within(container);
}

const congestion: Knob = {
  env: "SILO_NET_TCP_CONGESTION",
  default: "bbr",
  effective: "bbr",
  allowed: ["bbr", "off"],
};
const sendBuffer: Knob = {
  env: "SILO_NET_TCP_SEND_BUFFER",
  default: "33554432",
  effective: "33554432",
};

function listener(sendBufferRow: Listener["send_buffer"]): Listener {
  return {
    listener: "api",
    requested: "bbr",
    granted: "bbr",
    state: "on",
    reason: "",
    send_buffer: sendBufferRow,
  };
}

function netAdmin(overrides: Partial<Feature>): Feature {
  return { feature: "cap_net_admin", kind: "local", state: "off", ...overrides };
}

afterEach(() => {
  cleanup();
  state.persistedEnabled = "true";
  state.dirtyCount = 0;
});

describe("host tuning values", () => {
  it("shows algorithm names in capitals and byte sizes formatted", () => {
    show({ knobs: [congestion, sendBuffer] });

    const algorithm = row("TCP congestion control");
    expect(algorithm.getByText("Effective: BBR")).toBeInTheDocument();
    expect(algorithm.getByText("Default: BBR")).toBeInTheDocument();
    expect(algorithm.getByText("Allowed: BBR, Off")).toBeInTheDocument();

    const buffer = row("TCP send buffer");
    expect(buffer.getByText("Effective: 32.0 MB")).toBeInTheDocument();
    expect(buffer.getByText("Default: 32.0 MB")).toBeInTheDocument();
    expect(screen.queryByText(/33554432/)).not.toBeInTheDocument();
  });

  it("shows off as the Off state and keeps an unparsed request as sent", () => {
    show({
      knobs: [
        { ...congestion, effective: "off", requested: "bbrr", reason: "invalid" },
        { ...sendBuffer, effective: "off", requested: "lots", reason: "invalid" },
      ],
    });

    const algorithm = row("TCP congestion control");
    expect(algorithm.getByText("Effective: Off")).toBeInTheDocument();
    expect(algorithm.getByText("Requested: bbrr")).toBeInTheDocument();
    const buffer = row("TCP send buffer");
    expect(buffer.getByText("Effective: Off")).toBeInTheDocument();
    expect(buffer.getByText("Requested: lots")).toBeInTheDocument();
  });
});

describe("states that need no action", () => {
  it("shows a send buffer that needs no increase as Not needed, without a reason line", () => {
    show({
      listeners: [listener({ state: "off", reason: "not_needed", requested: 0, granted: 0 })],
    });

    expect(screen.getByText("Send buffer: Not needed")).toBeInTheDocument();
    expect(screen.queryByText("No increase needed")).not.toBeInTheDocument();
    expect(screen.queryByText("Send buffer: Off")).not.toBeInTheDocument();
  });

  it("shows a dropped NET_ADMIN capability as Not needed when no buffer needed it", () => {
    show({
      features: [
        netAdmin({ reason: "net_admin_dropped" }),
        {
          feature: "socket_send_buffer",
          kind: "local",
          state: "on",
          requested: "33554432",
          granted: "33554432",
        },
      ],
    });

    const capability = row("NET_ADMIN capability");
    expect(capability.getByText("Not needed")).toBeInTheDocument();
    expect(screen.queryByText("NET_ADMIN capability dropped")).not.toBeInTheDocument();
    expect(screen.queryByText("NET_ADMIN capability missing")).not.toBeInTheDocument();
  });

  it("keeps a missing NET_ADMIN capability Off when a buffer needed it, and says why on the buffer", () => {
    show({
      features: [
        netAdmin({ reason: "no_cap_net_admin" }),
        {
          feature: "socket_send_buffer",
          kind: "local",
          state: "capped",
          reason: "no_cap_net_admin",
          requested: "33554432",
          granted: "212992",
        },
      ],
    });

    const capability = row("NET_ADMIN capability");
    expect(capability.getByText("Off")).toBeInTheDocument();
    expect(capability.queryByText("NET_ADMIN capability missing")).not.toBeInTheDocument();
    // The reason stays on the buffer row, which is what it affects.
    expect(row("Socket send buffer").getByText("NET_ADMIN capability missing")).toBeInTheDocument();
  });

  it("keeps a missing NET_ADMIN capability Off when only a listener send buffer needed it", () => {
    show({
      features: [
        netAdmin({ reason: "no_cap_net_admin" }),
        { feature: "socket_send_buffer", kind: "local", state: "on" },
      ],
      listeners: [
        listener({
          state: "capped",
          reason: "no_net_admin",
          requested: 33554432,
          granted: 212992,
        }),
      ],
    });

    const capability = row("NET_ADMIN capability");
    expect(capability.getByText("Off")).toBeInTheDocument();
    expect(capability.queryByText("Not needed")).not.toBeInTheDocument();
    expect(capability.queryByText("NET_ADMIN capability missing")).not.toBeInTheDocument();
    // The reason shows once, on the listener's send buffer.
    expect(screen.getAllByText("NET_ADMIN capability missing")).toHaveLength(1);
  });
});

describe("host tuning field names", () => {
  it("shows a variable with no label by its name alone", () => {
    show({ knobs: [{ env: "SILO_NET_FUTURE_KNOB", default: "a", effective: "a" }] });

    const variable = screen.getByText("SILO_NET_FUTURE_KNOB");
    expect(variable.closest("dt")).toHaveTextContent(/^SILO_NET_FUTURE_KNOB$/);
  });
});

describe("section visibility", () => {
  it("hides the section once enablement is off and nothing is staged", () => {
    state.persistedEnabled = "false";
    show({});

    expect(screen.queryByRole("heading", { name: "API server" })).not.toBeInTheDocument();
  });

  it("keeps a staged draft reachable after another administrator turns the feature off", () => {
    state.persistedEnabled = "false";
    state.dirtyCount = 1;
    show({});

    expect(screen.getByRole("heading", { name: "API server" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Discard" })).toBeInTheDocument();
  });
});
