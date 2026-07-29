import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { DiagnosticAvailabilityStatus, DiagnosticStatus } from "@/api/types";

const mocks = vi.hoisted(() => ({
  mutateUploadsEnabled: vi.fn(),
  useDiagnosticsStatus: vi.fn(),
  useUpdateDiagnosticsUploadsEnabled: vi.fn(),
}));

vi.mock("@/hooks/useDateTimeFormat", () => ({
  useDateTimeFormat: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/diagnostics", () => ({
  downloadDiagnosticReport: vi.fn(),
  useDeleteDiagnosticReport: () => ({ isPending: false, mutate: vi.fn() }),
  useDiagnosticReport: () => ({ data: undefined, isError: false, isLoading: false }),
  useDiagnosticReports: () => ({
    data: { reports: [] },
    isError: false,
    isFetching: false,
    isLoading: false,
  }),
  useDiagnosticsStatus: () => mocks.useDiagnosticsStatus(),
  useUpdateDiagnosticsUploadsEnabled: () => mocks.useUpdateDiagnosticsUploadsEnabled(),
}));

import AdminDiagnostics from "./AdminDiagnostics";

function diagnosticStatus(status: DiagnosticAvailabilityStatus): DiagnosticStatus {
  return {
    status,
    server_instance_id: "server-id",
    accepted_schema_versions: [1],
    max_bundle_bytes: 10 * 1024 * 1024,
    max_manifest_bytes: 64 * 1024,
    retention_days: 30,
    consent_notice_version: 1,
  };
}

function renderPage() {
  return render(
    <MemoryRouter>
      <AdminDiagnostics />
    </MemoryRouter>,
  );
}

describe("AdminDiagnostics uploads toggle", () => {
  beforeEach(() => {
    mocks.mutateUploadsEnabled.mockReset();
    mocks.useDiagnosticsStatus.mockReset();
    mocks.useUpdateDiagnosticsUploadsEnabled.mockReset();
    mocks.useDiagnosticsStatus.mockReturnValue({
      data: diagnosticStatus("disabled"),
      isError: false,
      isLoading: false,
    });
    mocks.useUpdateDiagnosticsUploadsEnabled.mockReturnValue({
      isPending: false,
      mutate: mocks.mutateUploadsEnabled,
    });
  });

  it("renders disabled status off and submits enablement without changing optimistically", async () => {
    const user = userEvent.setup();
    renderPage();
    const toggle = screen.getByRole("switch", { name: "Client uploads" });

    expect(toggle).not.toBeChecked();
    expect(screen.getByText(/Use the Client uploads toggle above to enable them/)).toBeVisible();

    await user.click(toggle);

    expect(mocks.mutateUploadsEnabled).toHaveBeenCalledWith(true);
    expect(toggle).not.toBeChecked();
  });

  it("renders available status on and submits disablement", async () => {
    mocks.useDiagnosticsStatus.mockReturnValue({
      data: diagnosticStatus("available"),
      isError: false,
      isLoading: false,
    });
    const user = userEvent.setup();
    renderPage();
    const toggle = screen.getByRole("switch", { name: "Client uploads" });

    expect(toggle).toBeChecked();

    await user.click(toggle);

    expect(mocks.mutateUploadsEnabled).toHaveBeenCalledWith(false);
  });

  it("keeps storage-unavailable status on while showing the warning", () => {
    mocks.useDiagnosticsStatus.mockReturnValue({
      data: diagnosticStatus("storage_unavailable"),
      isError: false,
      isLoading: false,
    });
    renderPage();

    expect(screen.getByRole("switch", { name: "Client uploads" })).toBeChecked();
    expect(screen.getByText(/Client diagnostic storage is currently unavailable/)).toBeVisible();
  });

  it("disables the toggle while an update is pending", () => {
    mocks.useUpdateDiagnosticsUploadsEnabled.mockReturnValue({
      isPending: true,
      mutate: mocks.mutateUploadsEnabled,
    });
    renderPage();

    expect(screen.getByRole("switch", { name: "Client uploads" })).toBeDisabled();
  });

  it("disables the toggle when status is unavailable", () => {
    mocks.useDiagnosticsStatus.mockReturnValue({
      data: undefined,
      isError: true,
      isLoading: false,
    });
    renderPage();

    expect(screen.getByRole("switch", { name: "Client uploads" })).toBeDisabled();
  });
});
