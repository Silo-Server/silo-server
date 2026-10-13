import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useRequestFeatureStatus: vi.fn(),
  useCurrentProfile: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: (...args: unknown[]) => mocks.useRequestFeatureStatus(...args),
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => mocks.useCurrentProfile(),
}));

import { useCanRequest, useMissingSeasonsRequestable } from "./useCanRequest";

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
    mocks.useRequestFeatureStatus.mockReturnValue({
      data: { requests_enabled: false },
      isLoading: false,
    });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
  });

  it("returns discoveryEnabled=false when there is no profile", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({
      data: { requests_enabled: true },
      isLoading: false,
    });
    mocks.useCurrentProfile.mockReturnValue({ profile: null });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
  });

  it("returns discoveryEnabled=true when requests are enabled and there is a profile", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({
      data: { requests_enabled: true },
      isLoading: false,
    });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
  });

  it("returns isResolving=true and discoveryEnabled=false while the feature status is still loading", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: undefined, isLoading: true });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({
      discoveryEnabled: false,
      isResolving: true,
      submitDisabledReason: null,
    });
  });
});

describe("useMissingSeasonsRequestable", () => {
  function Capture({ enabled, onResult }: { enabled: boolean; onResult: (r: boolean) => void }) {
    onResult(useMissingSeasonsRequestable(enabled));
    return null;
  }

  function capture(enabled: boolean): boolean | null {
    let captured: boolean | null = null;
    render(
      <Capture
        enabled={enabled}
        onResult={(r) => {
          captured = r;
        }}
      />,
    );
    return captured;
  }

  it("needs requests on, the viewer allowed, and a library-only setup", () => {
    const status = { requests_enabled: true, allowed: true, missing_seasons_requestable: true };
    mocks.useRequestFeatureStatus.mockReturnValue({ data: status });
    expect(capture(true)).toBe(true);
    expect(mocks.useRequestFeatureStatus).toHaveBeenLastCalledWith({
      enabled: true,
      refetchOnMount: false,
    });

    mocks.useRequestFeatureStatus.mockReturnValue({
      data: { ...status, missing_seasons_requestable: false },
    });
    expect(capture(true)).toBe(false);
    mocks.useRequestFeatureStatus.mockReturnValue({ data: { ...status, allowed: false } });
    expect(capture(true)).toBe(false);
  });

  it("stays off, without reading the status, when disabled", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: undefined });
    expect(capture(false)).toBe(false);
    expect(mocks.useRequestFeatureStatus).toHaveBeenLastCalledWith({
      enabled: false,
      refetchOnMount: false,
    });
  });
});
