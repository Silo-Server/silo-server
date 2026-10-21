import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { RestartKeyMatcher } from "@/hooks/useRestartKeys";

import LoggingSettings from "./LoggingSettings";

// Radix Select opens through pointer capture and scrolls its options into
// view, neither of which jsdom provides.
window.HTMLElement.prototype.hasPointerCapture ??= () => false;
window.HTMLElement.prototype.setPointerCapture ??= () => {};
window.HTMLElement.prototype.releasePointerCapture ??= () => {};
window.HTMLElement.prototype.scrollIntoView ??= () => {};

const { serverSettings, updateSettingsMock } = vi.hoisted(() => ({
  serverSettings: { current: {} as Record<string, string> },
  updateSettingsMock: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: serverSettings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({
    data: { configured: [], managed_by_env: [] },
    isError: false,
  }),
  useUpdateServerSettings: () => ({ mutateAsync: updateSettingsMock, isPending: false }),
}));

// Mirrors the server registry: nothing on this page needs a restart.
vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: (): RestartKeyMatcher => ({ has: () => false }),
}));

function group(name: string) {
  return within(screen.getByRole("group", { name }));
}

async function openAdvanced(name: string) {
  await userEvent.click(group(name).getByRole("button", { name: /Advanced/ }));
}

describe("LoggingSettings", () => {
  describe("saving to the server", () => {
    beforeEach(() => {
      localStorage.clear();
      serverSettings.current = {
        "server.log_level": "info",
        "server.log_quiet": "",
        "opslog.retention_days": "7",
        "opslog.max_rows": "1000000",
        "opslog.max_size_mb": "1024",
      };
      updateSettingsMock.mockReset();
      updateSettingsMock.mockResolvedValue({ values: {}, restart_required: false });
    });

    it("sends the edited log keys in one batch", async () => {
      render(<LoggingSettings />);

      const days = group("Log retention").getByLabelText("Delete log entries older than");
      await userEvent.clear(days);
      await userEvent.type(days, "14");
      await openAdvanced("Server log");
      await userEvent.type(group("Server log").getByLabelText("Quiet log prefixes"), "scanner");
      const size = group("Log retention").getByLabelText("Maximum log size");
      await userEvent.clear(size);
      await userEvent.type(size, "512");

      expect(screen.getByText("3 unsaved changes")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({
          "opslog.retention_days": "14",
          "server.log_quiet": "scanner",
          "opslog.max_size_mb": "512",
        }),
      );
    });
  });
});
