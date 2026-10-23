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
  it.each([
    [
      "requests are off",
      { data: { requests_enabled: false }, isLoading: false },
      { id: "p1" },
      false,
      false,
    ],
    [
      "there is no profile",
      { data: { requests_enabled: true }, isLoading: false },
      null,
      false,
      false,
    ],
    [
      "requests are on with a profile",
      { data: { requests_enabled: true }, isLoading: false },
      { id: "p1" },
      true,
      false,
    ],
    [
      "the profile is blocked from requests",
      { data: { requests_enabled: true }, isLoading: false },
      { id: "p1", requests_allowed: false },
      false,
      false,
    ],
    [
      "the feature status is loading",
      { data: undefined, isLoading: true },
      { id: "p1" },
      false,
      true,
    ],
  ])("resolves discovery when %s", (_label, status, profile, discoveryEnabled, isResolving) => {
    mocks.useRequestFeatureStatus.mockReturnValue(status);
    mocks.useCurrentProfile.mockReturnValue({ profile });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(
      <CaptureHook
        onResult={(r) => {
          captured = r;
        }}
      />,
    );

    expect(captured).toEqual({ discoveryEnabled, isResolving, submitDisabledReason: null });
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
