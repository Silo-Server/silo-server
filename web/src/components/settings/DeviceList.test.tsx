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

function renderList(
  devices: UserDevice[],
  props: Partial<{
    groupByProfile: boolean;
    profileFilter: string | null;
    onProfileFilterChange: (id: string | null) => void;
    ownProfileId: string;
  }> = {},
) {
  const onSelect = vi.fn();
  const onProfileFilterChange = props.onProfileFilterChange ?? vi.fn();
  render(
    <DeviceList
      devices={devices}
      selectedDeviceId={null}
      onSelect={onSelect}
      search=""
      onSearchChange={vi.fn()}
      now={NOW}
      onProfileFilterChange={onProfileFilterChange}
      {...props}
    />,
  );
  return { onSelect, onProfileFilterChange };
}

const HOUSEHOLD: UserDevice[] = [
  device({ device_id: "a", device_name: "Sam's laptop", profile_name: "Sam" }),
  device({ device_id: "b", device_name: "Sam's TV", profile_name: "Sam" }),
  device({
    device_id: "c",
    device_name: "Robin's iPad",
    profile_id: "profile-2",
    profile_name: "Robin",
  }),
];

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

    // Scoped to headings: the profile-filter chips carry these names too.
    expect(screen.getByRole("heading", { name: "Sam" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Robin" })).toBeInTheDocument();
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

describe("DeviceList at scale", () => {
  const OLD = new Date(NOW - 200 * 24 * 60 * 60 * 1000).toISOString();

  // A real account carried 260 devices, over half of them one-off sessions
  // that had never changed a setting. Listing them all produced a
  // thirteen-thousand-pixel page, so the settings never came into view.
  function bigFleet(): UserDevice[] {
    return [
      device({ device_id: "here", device_name: "This browser", is_current_device: true }),
      device({ device_id: "tv", device_name: "Apple TV", changed_count: 5 }),
      // Old but configured: still worth showing, since someone set it up.
      device({
        device_id: "kept",
        device_name: "Old but configured",
        last_seen_at: OLD,
        changed_count: 2,
      }),
      ...Array.from({ length: 40 }, (_, i) =>
        device({ device_id: `junk-${i}`, device_name: `Silo-PR111-build-${i}`, last_seen_at: OLD }),
      ),
    ];
  }

  it("hides devices nobody has used and that carry no settings", () => {
    renderList(bigFleet());

    expect(screen.getByText("This browser")).toBeInTheDocument();
    expect(screen.getByText("Apple TV")).toBeInTheDocument();
    expect(screen.getByText("Old but configured")).toBeInTheDocument();
    expect(screen.queryByText("Silo-PR111-build-0")).not.toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
  });

  it("says how many it is holding back, and reveals them on request", async () => {
    renderList(bigFleet());

    const toggle = screen.getByRole("button", { name: "Show 40 unused devices" });
    await userEvent.click(toggle);

    expect(screen.getByText("Silo-PR111-build-0")).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(43);
    expect(screen.getByRole("button", { name: "Hide unused devices" })).toBeInTheDocument();
  });

  // Searching means looking for something specific; hiding a device from its
  // own name would read as the device having disappeared.
  it("searches across hidden devices without expanding them", () => {
    render(
      <DeviceList
        devices={bigFleet()}
        selectedDeviceId={null}
        onSelect={vi.fn()}
        search="build-7"
        onSearchChange={vi.fn()}
        now={NOW}
      />,
    );

    expect(screen.getByText("Silo-PR111-build-7")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /unused devices/ })).not.toBeInTheDocument();
  });

  it("keeps the current device visible even when it is old and unconfigured", () => {
    renderList([
      device({
        device_id: "here",
        device_name: "This browser",
        is_current_device: true,
        last_seen_at: OLD,
      }),
      device({ device_id: "junk", device_name: "Forgotten", last_seen_at: OLD }),
    ]);

    expect(screen.getByText("This browser")).toBeInTheDocument();
    expect(screen.queryByText("Forgotten")).not.toBeInTheDocument();
  });

  it("offers no toggle when nothing is dormant", () => {
    renderList([device({ device_id: "a", device_name: "Recent", changed_count: 1 })]);
    expect(screen.queryByRole("button", { name: /unused devices/ })).not.toBeInTheDocument();
  });
});

describe("DeviceList profile filter", () => {
  it("offers one chip per profile, plus everyone, with counts", () => {
    renderList(HOUSEHOLD, { groupByProfile: true });

    expect(screen.getByRole("button", { name: "Everyone, 3 devices" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sam, 2 devices" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Robin, 1 device" })).toBeInTheDocument();
  });

  // At eight profiles, arrival order buried the person actually using the
  // screen; the rest sort by name so a chip does not move as devices are used.
  it("leads with the viewer's own profile, then sorts by name", () => {
    renderList(
      [
        device({ device_id: "z", profile_id: "profile-9", profile_name: "Zoe" }),
        device({ device_id: "c", profile_id: "profile-3", profile_name: "Casey" }),
        device({ device_id: "a", profile_id: "profile-1", profile_name: "Sam" }),
      ],
      { groupByProfile: true, ownProfileId: "profile-1" },
    );

    const chips = screen
      .getByRole("group", { name: "Filter by profile" })
      .querySelectorAll("button");
    const names = [...chips].map((chip) => chip.getAttribute("aria-label")?.split(",")[0]);
    expect(names).toEqual(["Everyone", "Sam", "Casey", "Zoe"]);
  });

  // Someone looking at their own devices has exactly one profile, so a filter
  // with a single option would be chrome that explains nothing.
  it("stays hidden outside the household view", () => {
    renderList(HOUSEHOLD, { groupByProfile: false });
    expect(screen.queryByRole("group", { name: "Filter by profile" })).not.toBeInTheDocument();
  });

  it("stays hidden when the household has only one profile", () => {
    renderList([HOUSEHOLD[0]!, HOUSEHOLD[1]!], { groupByProfile: true });
    expect(screen.queryByRole("group", { name: "Filter by profile" })).not.toBeInTheDocument();
  });

  it("reports the chosen profile", async () => {
    const { onProfileFilterChange } = renderList(HOUSEHOLD, { groupByProfile: true });

    await userEvent.click(screen.getByRole("button", { name: "Robin, 1 device" }));

    expect(onProfileFilterChange).toHaveBeenCalledWith("profile-2");
  });

  it("clears the filter when the active chip is clicked again", async () => {
    const { onProfileFilterChange } = renderList(HOUSEHOLD, {
      groupByProfile: true,
      profileFilter: "profile-2",
    });

    await userEvent.click(screen.getByRole("button", { name: "Robin, 1 device" }));

    expect(onProfileFilterChange).toHaveBeenCalledWith(null);
  });

  it("shows only the filtered profile's devices", () => {
    renderList(HOUSEHOLD, { groupByProfile: true, profileFilter: "profile-2" });

    expect(screen.getByText("Robin's iPad")).toBeInTheDocument();
    expect(screen.queryByText("Sam's laptop")).not.toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
  });

  // With one person selected, a person heading would just repeat the chip.
  it("falls back to recency headings once a profile is chosen", () => {
    renderList(HOUSEHOLD, { groupByProfile: true, profileFilter: "profile-2" });

    expect(screen.getByText("This week")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Robin" })).not.toBeInTheDocument();
  });

  it("keeps chip counts stable while a filter is active", () => {
    renderList(HOUSEHOLD, { groupByProfile: true, profileFilter: "profile-2" });

    // Sam's chip still says 2 even though none of Sam's devices are listed.
    expect(screen.getByRole("button", { name: "Sam, 2 devices" })).toBeInTheDocument();
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
