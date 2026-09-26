import type { AutoscanSource } from "@/api/types";

// Local ordering only: list reads reserve before dispatch; mutation readback
// reserves after decoding. This is neither a server revision nor a write receipt.
let observationOrder = 0;
export function nextAutoscanSourceObservation(): number {
  return ++observationOrder;
}
export function observedAutoscanSource(source: AutoscanSource, order: number): AutoscanSource {
  return { ...source, autoscanObservationOrder: order } as AutoscanSource;
}
export function autoscanSourceObservation(source: AutoscanSource | null | undefined): number {
  return (
    (source as (AutoscanSource & { autoscanObservationOrder?: number }) | null | undefined)
      ?.autoscanObservationOrder ?? 0
  );
}
