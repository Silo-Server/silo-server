import {
  captureProfileRequestContext,
  fetchWithSession,
  isCapturedProfileAuthorityActive,
  reportProfileUnverified,
  StaleApiRequestContextError,
} from "../client";
import { V2_CLIENT_HEADERS, V2ProblemError, problemId, type Problem } from "./request";
import { v2Operations } from "./operations";

/** Raw gzip download through the same captured session boundary as v2 JSON. */
export async function fetchAdminDiagnosticReportBundle(id: string): Promise<Blob> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const operationID = v2Operations["GET /api/v2/admin/diagnostics/reports/{id}/download"];
  const { res, requestProfileId, requestProfileToken } = await fetchWithSession(
    `/api/v2/admin/diagnostics/reports/${encodeURIComponent(id)}/download`,
    {
      method: "GET",
      headers: {
        ...V2_CLIENT_HEADERS,
        Accept: "application/gzip, application/problem+json",
        Authorization: `Bearer ${profileContext.accessToken}`,
        "X-Profile-Id": profileContext.profileId,
        "X-Profile-Token": profileContext.profileToken ?? "",
      },
    },
    profileContext,
  );
  if (!res.ok) {
    const body: Partial<Problem> | null = await res.json().catch(() => null);
    if (
      body &&
      typeof body.type === "string" &&
      typeof body.title === "string" &&
      body.status === res.status
    ) {
      if (res.status === 403 && problemId({ type: body.type }) === "profile_verification_required")
        reportProfileUnverified(requestProfileId, requestProfileToken, profileContext);
      throw new V2ProblemError(operationID, body as Problem);
    }
    throw new Error(`Diagnostic report download failed (${res.status}).`);
  }
  if (res.headers.get("Content-Type")?.split(";")[0]?.trim().toLowerCase() !== "application/gzip")
    throw new Error("The server returned an unexpected diagnostic report format.");
  const blob = await res.blob();
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return blob;
}
