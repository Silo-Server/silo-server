import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useHasUnsavedChanges, useReportUnsavedChanges } from "./useUnsavedChanges";

function fireBeforeUnload(): boolean {
  const event = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

describe("useUnsavedChanges registry", () => {
  it("arms the reload prompt while anything is dirty and disarms when clean", () => {
    expect(fireBeforeUnload()).toBe(false);

    const report = renderHook(({ dirty }) => useReportUnsavedChanges(dirty), {
      initialProps: { dirty: true },
    });
    const read = renderHook(() => useHasUnsavedChanges());

    expect(read.result.current).toBe(true);
    // The router guard cannot see reload/close; the registry's own
    // beforeunload handler covers that path for every reporter.
    expect(fireBeforeUnload()).toBe(true);

    report.rerender({ dirty: false });
    read.rerender();
    expect(read.result.current).toBe(false);
    expect(fireBeforeUnload()).toBe(false);

    report.unmount();
    read.unmount();
  });

  it("drops a reporter's claim on unmount", () => {
    const report = renderHook(() => useReportUnsavedChanges(true));
    expect(fireBeforeUnload()).toBe(true);

    report.unmount();
    expect(fireBeforeUnload()).toBe(false);
  });
});
