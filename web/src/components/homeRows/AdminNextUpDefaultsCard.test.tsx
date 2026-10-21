import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AdminNextUpDefaultsCard } from "./AdminNextUpDefaultsCard";

// Radix Select reads pointer capture and scrolls options, which jsdom lacks.
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const mocks = vi.hoisted(() => ({
  settings: undefined as Record<string, string> | undefined,
  mutate: vi.fn(),
  isPending: false,
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: mocks.settings }),
  useUpdateServerSetting: () => ({ mutate: mocks.mutate, isPending: mocks.isPending }),
}));

describe("AdminNextUpDefaultsCard", () => {
  beforeEach(() => {
    mocks.settings = {};
    mocks.mutate.mockReset();
    mocks.isPending = false;
  });

  // The admin API validates a default against the setting's value schema, so
  // both controls write JSON text to the defaults.<key> server settings.
  it("saves each default as JSON text under its defaults key", async () => {
    render(<AdminNextUpDefaultsCard />);

    await userEvent.click(screen.getByRole("combobox", { name: "Next up episodes" }));
    await userEvent.click(await screen.findByRole("option", { name: "Separate row" }));
    await userEvent.click(screen.getByRole("switch", { name: "Show new episodes first" }));

    expect(mocks.mutate).toHaveBeenNthCalledWith(
      1,
      { key: "defaults.ui.next_up_mode", value: '"separate"' },
      expect.anything(),
    );
    expect(mocks.mutate).toHaveBeenNthCalledWith(
      2,
      { key: "defaults.ui.next_up_new_releases_first", value: "true" },
      expect.anything(),
    );
  });

  // Each write carries the settings version it read, so a second change waits
  // for the first, and nothing is editable before the settings load.
  it("locks both controls while settings load or a save runs", () => {
    mocks.settings = undefined;
    const { rerender } = render(<AdminNextUpDefaultsCard />);
    expect(screen.getByRole("switch", { name: "Show new episodes first" })).toBeDisabled();

    mocks.settings = {};
    mocks.isPending = true;
    rerender(<AdminNextUpDefaultsCard />);
    expect(screen.getByRole("combobox", { name: "Next up episodes" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Show new episodes first" })).toBeDisabled();
  });
});
