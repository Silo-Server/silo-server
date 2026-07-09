import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import StorageSettings from "./StorageSettings";

const useSettingsFormMock = vi.fn();
const useCheckAdminSettingsConnectionMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: (...args: unknown[]) =>
    useCheckAdminSettingsConnectionMock(...args),
}));

describe("StorageSettings", () => {
  it("shows public and private storage sections with connection checks", () => {
    useCheckAdminSettingsConnectionMock.mockReturnValue({
      isPending: false,
      mutateAsync: vi.fn(),
    });
    useSettingsFormMock.mockReturnValue({
      isLoading: false,
      getValue: (key: string) => {
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
      setValue: vi.fn(),
      dirtyCount: 0,
      save: vi.fn(),
      discard: vi.fn(),
      isSaving: false,
      restartRequired: false,
      sensitiveConfigured: [],
      buildConnectionCheckRequest: vi.fn(),
      isDirty: () => false,
    });

    const markup = renderToStaticMarkup(<StorageSettings />);

    expect(markup).toContain("Public Assets");
    expect(markup).toContain("Private Internal");
    expect(markup).toContain("Check Connection");
    expect(markup).not.toContain("Storage location change");
  });

  it("warns about the artwork cache when a public storage identity field is edited", () => {
    useCheckAdminSettingsConnectionMock.mockReturnValue({
      isPending: false,
      mutateAsync: vi.fn(),
    });
    useSettingsFormMock.mockReturnValue({
      isLoading: false,
      getValue: (key: string) => {
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
      setValue: vi.fn(),
      dirtyCount: 1,
      save: vi.fn(),
      discard: vi.fn(),
      isSaving: false,
      restartRequired: false,
      sensitiveConfigured: [],
      buildConnectionCheckRequest: vi.fn(),
      isDirty: (key: string) => key === "s3.public_bucket",
    });

    const markup = renderToStaticMarkup(<StorageSettings />);

    expect(markup).toContain("Storage location change");
    expect(markup).toContain("re-caches anything missing");
  });
});
