import { v2 } from "./request";
import type { components } from "./schema";
import type {
  CreateWebhookSyncConnectionRequest,
  UpdateWebhookSyncConnectionRequest,
  UpdateWebhookSyncProfileMappingsRequest,
} from "@/api/types";
type Schemas = components["schemas"];
const connection = (r: Schemas["WebhookConnection"]) => ({
  ...r,
  webhook_url: new URL(r.webhook_url, window.location.origin).href,
});
const mapping = (r: Schemas["WebhookMapping"]) => ({ ...r, id: Number(r.id) });
export async function listWebhookConnections() {
  const out: ReturnType<typeof connection>[] = [];
  let cursor: string | undefined;
  do {
    const page = await v2("GET /api/v2/webhook-sync/connections", { query: { limit: 50, cursor } });
    out.push(...page.items.map(connection));
    cursor = page.page?.has_more ? page.page.next_cursor : undefined;
  } while (cursor);
  return out;
}
export async function createWebhookConnection(body: CreateWebhookSyncConnectionRequest) {
  const r = await v2("POST /api/v2/webhook-sync/connections", { body });
  return {
    ...r,
    connection: connection(r.connection),
    webhook_url: new URL(r.webhook_url, window.location.origin).href,
  };
}
export async function updateWebhookConnection(
  id: string,
  body: UpdateWebhookSyncConnectionRequest,
) {
  return connection(await v2("PUT /api/v2/webhook-sync/connections/{id}", { path: { id }, body }));
}
export const deleteWebhookConnection = (id: string) =>
  v2("DELETE /api/v2/webhook-sync/connections/{id}", { path: { id } });
export async function rotateWebhookConnection(id: string) {
  const r = await v2("POST /api/v2/webhook-sync/connections/{id}/webhook/rotate", { path: { id } });
  return { ...r, webhook_url: new URL(r.webhook_url, window.location.origin).href };
}
export async function getWebhookMappings(id: string) {
  const r = await v2("GET /api/v2/webhook-sync/connections/{id}/profile-mappings", {
    path: { id },
  });
  return { ...r, mappings: r.mappings.map(mapping) };
}
export const updateWebhookMappings = (id: string, body: UpdateWebhookSyncProfileMappingsRequest) =>
  v2("PUT /api/v2/webhook-sync/connections/{id}/profile-mappings", { path: { id }, body });
export async function listWebhookEvents(id: string) {
  const page = await v2("GET /api/v2/webhook-sync/connections/{id}/events", {
    path: { id },
    query: { limit: 200 },
  });
  return page.items.map((r) => ({ ...r, id: Number(r.id) }));
}
