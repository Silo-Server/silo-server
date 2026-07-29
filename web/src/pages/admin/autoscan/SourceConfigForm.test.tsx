/**
 * Regression coverage for React error #185 (maximum update depth) seen in
 * production on /admin/autoscan.
 *
 * SchemaForm reports validity from an effect keyed on `[valid, onValidityChange]`.
 * The call site passed an inline arrow and a descriptor object rebuilt on every
 * render, so the effect re-fired each pass and its setState re-rendered the
 * parent — an unbroken loop. Two things fix it and both are asserted here: the
 * callback must not write when the value is unchanged, and the descriptor must
 * keep a stable identity.
 */
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import type { AutoscanScanSourceDescriptor } from "@/api/types";
import { SourceConfigForm } from "./SourceConfigForm";
import { useState } from "react";

const descriptor: AutoscanScanSourceDescriptor = {
  delivery_modes: ["poll"],
  connection: "none",
  config_form: {
    fields: [
      {
        key: "root",
        label: "Root",
        control: "TEXT",
        required: true,
        secret: false,
        multiline: false,
      },
    ],
  },
};

/**
 * Mirrors the real call site: state stored in an object, updated through an
 * inline arrow, with a descriptor rebuilt on every render. That combination is
 * what looped in production.
 */
function Harness() {
  const [form, setForm] = useState<{ values: Record<string, unknown>; valid: boolean }>({
    values: {},
    valid: true,
  });
  const rebuilt = {
    ...descriptor,
    config_form: { ...descriptor.config_form!, fields: descriptor.config_form!.fields },
  };
  return (
    <div>
      <span data-testid="valid">{String(form.valid)}</span>
      <SourceConfigForm
        descriptor={rebuilt}
        values={form.values}
        onChange={(values) => setForm((f) => ({ ...f, values }))}
        onValidityChange={(valid) => setForm((f) => (f.valid === valid ? f : { ...f, valid }))}
      />
    </div>
  );
}

describe("SourceConfigForm validity reporting", () => {
  it("settles without an infinite update loop", () => {
    const client = new QueryClient({
      defaultOptions: { queries: { enabled: false, retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <Harness />
      </QueryClientProvider>,
    );
    // A required-but-empty field must report invalid, and rendering must settle.
    expect(screen.getByTestId("valid").textContent).toBe("false");
  });
});
