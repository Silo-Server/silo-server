import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { useWizardSteps } from "./useWizardSteps";
import { createEmptySetupWizardFlags } from "./setupStorage";

const useWizardContextMock = vi.fn();

vi.mock("./WizardContext", () => ({
  useWizardContext: (...args: unknown[]) => useWizardContextMock(...args),
}));

function context(overrides: Partial<Parameters<typeof useWizardContextMock>[0]> = {}) {
  useWizardContextMock.mockReturnValue({
    user: { id: 1, role: "admin" },
    profile: { id: "p1" },
    settingsReady: true,
    stepDone: createEmptySetupWizardFlags(),
    visiting: null,
    ...overrides,
  });
}

describe("useWizardSteps", () => {
  it("lands on the account step until an account exists", () => {
    context({ user: null });
    const { result } = renderHook(() => useWizardSteps());
    expect(result.current.currentStep).toBe("account");
    expect(result.current.steps.map((s) => s.id)).toEqual([
      "account",
      "server",
      "playback",
      "storage",
      "library",
      "subtitles",
      "connect",
      "features",
      "done",
    ]);
  });

  it("holds the account step until the settings snapshot is ready", () => {
    context({ settingsReady: false });
    const { result } = renderHook(() => useWizardSteps());
    expect(result.current.currentStep).toBe("account");
  });

  it("holds the account step until a profile is selected", () => {
    context({ profile: null });
    const { result } = renderHook(() => useWizardSteps());
    expect(result.current.currentStep).toBe("account");
  });

  it("advances to the first unfinished step in order", () => {
    context({ stepDone: { ...createEmptySetupWizardFlags(), server: true, playback: true } });
    const { result } = renderHook(() => useWizardSteps());
    expect(result.current.currentStep).toBe("storage");
    expect(result.current.steps.find((s) => s.id === "server")?.reachable).toBe(true);
    expect(result.current.steps.find((s) => s.id === "storage")?.reachable).toBe(false);
  });

  it("reopens a finished step the admin jumped back to", () => {
    context({
      stepDone: { ...createEmptySetupWizardFlags(), server: true, playback: true },
      visiting: "server",
    });
    const { result } = renderHook(() => useWizardSteps());
    expect(result.current.currentStep).toBe("server");
  });

  it("ignores a visit to a step that is not finished yet", () => {
    context({ stepDone: { ...createEmptySetupWizardFlags(), server: true }, visiting: "library" });
    const { result } = renderHook(() => useWizardSteps());
    expect(result.current.currentStep).toBe("playback");
  });

  it("ends on done once every optional step is complete", () => {
    const all = createEmptySetupWizardFlags();
    for (const key of Object.keys(all) as (keyof typeof all)[]) all[key] = true;
    context({ stepDone: all });
    const { result } = renderHook(() => useWizardSteps());
    expect(result.current.currentStep).toBe("done");
  });
});
