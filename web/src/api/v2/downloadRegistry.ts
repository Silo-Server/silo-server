import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";

export async function fetchDownloadCapability() {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const result = await v2("GET /api/v2/capabilities/downloads", { profileContext });
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return result;
}
export async function deleteDownloadEntry(id: string) {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  await v2("DELETE /api/v2/downloads/{id}", {
    path: { id },
    profileContext,
    retryAuthentication: false,
  });
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
}
