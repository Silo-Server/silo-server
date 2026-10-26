/// <reference types="@webgpu/types" />

/**
 * A software (fallback) adapter cannot tone-map 4K in time, so the engine
 * treats it as no adapter. Shared by the page's capability probe and the
 * presenter, which load separately.
 */
export function isSoftwareAdapter(adapter: GPUAdapter): boolean {
  return (
    (adapter.info as GPUAdapterInfo | undefined)?.isFallbackAdapter ??
    (adapter as GPUAdapter & { isFallbackAdapter?: boolean }).isFallbackAdapter ??
    false
  );
}
