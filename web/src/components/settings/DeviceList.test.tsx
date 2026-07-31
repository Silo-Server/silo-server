import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { UserDevice } from "@/api/types";
import { DeviceList, lastSeenLabel } from "@/components/settings/DeviceList";

const NOW = Date.parse("2026-07-31T12:00:00Z");

function device(overrides: Partial<UserDevice> = {}): UserDevice {
  return {
    device_id: "device-1",
    device_name: "Chrome on macOS",
    device_platform: "macOS Web",
    last_seen_at: new Date(NOW - 60 * 60 * 1000).toISOString(),
    profile_id: "profile-1",
    profile_name: "Sam",
    is_current_device: false,
    changed_count: 0,
    ...overrides,
  };
}

function renderList(devices: UserDevice[], props: Partial<{ groupByProfile: boolean }> = {}) {
  const onSelect = vi.fn();
  render(
    <DeviceList
      devices={devices}
      selectedDeviceId={null}
      onSelect={onSelect}
      search=""
      onSearchChange={vi.fn()}
      now={NOW}
      {...props}
    />,
  );
  return { onSelect };
}

describe("DeviceList", () => {
  it("groups by recency and marks the current device", () => {
    renderList([
      device({ device_id: "here", device_name: "This browser", is_current_device: true }),
      device({
        device_id: "recent",
        device_name: "Apple TV",
        last_seen_at: new Date(NOW - 2 * 24 * 60 * 60 * 1000).toISOString(),
      }),
      device({
        device_id: "old",
        device_name: "Old iPad",
        last_seen_at: new Date(NOW - 60 * 24 * 60 * 60 * 1000).toISOString(),
      }),
    ]);

    expect(screen.getByText("Using now")).toBeInTheDocument();
    expect(screen.getByText("This week")).toBeInTheDocument();
    expect(screen.getByText("Earlier")).toBeInTheDocument();
    expect(screen.getByLabelText("You're on this device")).toBeInTheDocument();
  });

  // A device with nothing changed shows a dash, not "0": the list exists to
  // answer "which one did I change?" at a glance.
  it("shows a dash rather than a zero when nothing is changed", () => {
    renderList([
      device({ device_id: "clean", device_name: "Clean", changed_count: 0 }),
      device({ device_id: "dirty", device_name: "Dirty", changed_count: 3 }),
    ]);

    expect(screen.getByLabelText("Nothing changed")).toHaveTextContent("—");
    expect(screen.getByLabelText("3 settings changed here")).toHaveTextContent("3");
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });

  it("selects a device when its row is clicked", async () => {
    const { onSelect } = renderList([device({ device_id: "tv", device_name: "Apple TV" })]);

    await userEvent.click(screen.getByRole("button", { name: /Apple TV/ }));

    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ device_id: "tv" }));
  });

  it("groups by person in the household view", () => {
    renderList(
      [
        device({ device_id: "a", device_name: "Sam's laptop", profile_name: "Sam" }),
        device({
          device_id: "b",
          device_name: "Robin's iPad",
          profile_id: "profile-2",
          profile_name: "Robin",
        }),
      ],
      { groupByProfile: true },
    );

    expect(screen.getByText("Sam")).toBeInTheDocument();
    expect(screen.getByText("Robin")).toBeInTheDocument();
    expect(screen.queryByText("Using now")).not.toBeInTheDocument();
  });

  it("stays readable with many devices", () => {
    const many = Array.from({ length: 11 }, (_, index) =>
      device({
        device_id: `device-${index}`,
        device_name: `Device ${index}`,
        last_seen_at: new Date(NOW - index * 5 * 24 * 60 * 60 * 1000).toISOString(),
      }),
    );
    renderList(many);

    const list = screen.getAllByRole("listitem");
    expect(list).toHaveLength(11);
    expect(screen.getByLabelText("Search devices")).toHaveAttribute(
      "placeholder",
      "Search 11 devices",
    );
  });

  it("filters by platform as well as name", async () => {
    const onSearchChange = vi.fn();
    const { rerender } = render(
      <DeviceList
        devices={[
          device({ device_id: "tv", device_name: "Living Room", device_platform: "tvOS" }),
          device({ device_id: "web", device_name: "Chrome", device_platform: "macOS Web" }),
        ]}
        selectedDeviceId={null}
        onSelect={vi.fn()}
        search=""
        onSearchChange={onSearchChange}
        now={NOW}
      />,
    );

    rerender(
      <DeviceList
        devices={[
          device({ device_id: "tv", device_name: "Living Room", device_platform: "tvOS" }),
          device({ device_id: "web", device_name: "Chrome", device_platform: "macOS Web" }),
        ]}
        selectedDeviceId={null}
        onSelect={vi.fn()}
        search="tv"
        onSearchChange={onSearchChange}
        now={NOW}
      />,
    );

    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(1);
    const [onlyItem] = items;
    expect(within(onlyItem!).getByText("Living Room")).toBeInTheDocument();
  });
});

describe("lastSeenLabel", () => {
  it("reads as plain language", () => {
    expect(lastSeenLabel(new Date(NOW - 30 * 60 * 1000).toISOString(), NOW)).toBe(
      "Less than an hour ago",
    );
    expect(lastSeenLabel(new Date(NOW - 3 * 60 * 60 * 1000).toISOString(), NOW)).toBe(
      "3 hours ago",
    );
    expect(lastSeenLabel(new Date(NOW - 24 * 60 * 60 * 1000).toISOString(), NOW)).toBe("Yesterday");
    expect(lastSeenLabel(new Date(NOW - 10 * 24 * 60 * 60 * 1000).toISOString(), NOW)).toBe(
      "10 days ago",
    );
    expect(lastSeenLabel("not-a-date", NOW)).toBe("Never used");
  });
});
