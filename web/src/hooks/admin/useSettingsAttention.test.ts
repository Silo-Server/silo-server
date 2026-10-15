import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useSettingsAttention } from "./useSettingsHealth";

const brandingMock = vi.fn();

vi.mock("@/hooks/useBranding", () => ({ useBranding: () => brandingMock() }));

vi.mock("@/hooks/queries/admin/settings", () => ({
  // Nothing configured: no storage, no mail, no public URL.
  useAdminServerSettings: () => ({ data: {} }),
  useAdminSensitiveStatus: () => ({ data: { configured: [] } }),
  useAdminServerStatus: () => ({ data: undefined }),
  useCatalogSearchStatus: () => ({ data: undefined }),
}));

const ids = () => renderHook(() => useSettingsAttention()).result.current.map((item) => item.id);

beforeEach(() => brandingMock.mockReset());

describe("useSettingsAttention", () => {
  it("leaves storage out until branding reports whether it is available", () => {
    brandingMock.mockReturnValue({ storageAvailable: false, loaded: false });

    expect(ids()).not.toContain("storage");
    expect(ids()).toContain("email");
  });

  it("lists storage once branding says it is not available", () => {
    brandingMock.mockReturnValue({ storageAvailable: false, loaded: true });

    expect(ids()).toContain("storage");
  });
});
