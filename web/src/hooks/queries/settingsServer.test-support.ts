import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { vi } from "vitest";

import { v2Problem } from "@/api/v2/problems.test-support";
import { SETTINGS_API_VERSION } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";

export function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client: queryClient }, children);
  return { queryClient, wrapper };
}

/**
 * A server stand-in for one setting key, answering through `v2Mock` (the
 * caller's mock of `v2`). It resolves through the shared client resolver,
 * which mirrors internal/settingsresolve. Using it rather than a canned answer
 * is what makes scope-precedence assertions mean something: a profile_device
 * row really does outrank a profile row here. Pass `manifestRevision` to also
 * answer the settings contract capabilities read.
 */
export function fakeSettingsServer(
  v2Mock: ReturnType<typeof vi.fn>,
  key: string,
  initial: StoredSettingRow[] = [],
  manifestRevision?: number,
) {
  const rows = [...initial];
  v2Mock.mockImplementation(
    (
      operation: string,
      options?: { path?: { key: string }; query?: { scope: string }; body?: { value: unknown } },
    ) => {
      if (
        manifestRevision !== undefined &&
        operation === "GET /api/v2/settings/contract/capabilities"
      ) {
        return Promise.resolve({
          api_version: SETTINGS_API_VERSION,
          manifest_revision: manifestRevision,
          contract_etag: "etag",
        });
      }
      if (operation === "GET /api/v2/settings/values/effective") {
        const resolved = resolveSettingValues([key], rows, {
          profileId: "profile-1",
          deviceId: "device-1",
        })[0]!;
        return Promise.resolve({
          revision: 1,
          items: [{ key, value: resolved.value, source: resolved.source, definition_revision: 1 }],
        });
      }
      const rowKey = options?.path?.key;
      const scope = options?.query?.scope;
      if (!rowKey || !scope) return Promise.resolve(undefined);
      const index = rows.findIndex((row) => row.key === rowKey && row.scope === scope);
      if (operation.startsWith("DELETE ")) {
        if (index < 0) {
          return Promise.reject(v2Problem(404, "not_found", "No value is set at this scope"));
        }
        rows.splice(index, 1);
        return Promise.resolve(undefined);
      }
      const row: StoredSettingRow = {
        key: rowKey,
        scope: scope as StoredSettingRow["scope"],
        profileId: "profile-1",
        deviceId: scope === "profile_device" ? "device-1" : undefined,
        value: options?.body?.value,
      };
      if (index < 0) rows.push(row);
      else rows[index] = row;
      return Promise.resolve(undefined);
    },
  );
  return rows;
}
