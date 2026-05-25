import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useRequestFeatureStatus: vi.fn(),
  useCurrentProfile: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => mocks.useRequestFeatureStatus(),
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => mocks.useCurrentProfile(),
}));

import { useCanRequest } from "./useCanRequest";

function CaptureHook({ onResult }: { onResult: (r: ReturnType<typeof useCanRequest>) => void }) {
  const result = useCanRequest();
  onResult(result);
  return null;
}

function render(child: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(<QueryClientProvider client={client}>{child}</QueryClientProvider>);
}

describe("useCanRequest", () => {
  it("returns discoveryEnabled=false when requests_enabled is false", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: { requests_enabled: false } });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({ discoveryEnabled: false, submitDisabledReason: null });
  });

  it("returns discoveryEnabled=false when there is no profile", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: { requests_enabled: true } });
    mocks.useCurrentProfile.mockReturnValue({ profile: null });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({ discoveryEnabled: false, submitDisabledReason: null });
  });

  it("returns discoveryEnabled=true when requests are enabled and there is a profile", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: { requests_enabled: true } });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({ discoveryEnabled: true, submitDisabledReason: null });
  });

  it("returns discoveryEnabled=false while the feature status is still loading", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: undefined });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({ discoveryEnabled: false, submitDisabledReason: null });
  });
});
