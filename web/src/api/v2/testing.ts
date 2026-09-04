/**
 * Test-only helpers for the v2 boundary. Production code receives branded
 * values from `v2(...)` and never needs to fabricate one.
 */
import type { V2, V2OperationKey, V2Response } from "./request";

type Unbranded<T> = T extends V2<infer U> ? U : T;

/** Marks a hand-built body as a decoded v2 response so a test can seed a cache or mock with it. */
export function v2Fixture<K extends V2OperationKey>(body: Unbranded<V2Response<K>>): V2Response<K> {
  return body as V2Response<K>;
}
