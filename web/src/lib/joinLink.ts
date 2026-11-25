/**
 * The link that hands the Silo app a server address and an app code. It
 * mirrors internal/appkeys/joinlink.go: both forms carry the same fragment,
 * `v1.<base64url of the server address>.<app code>`, so the link page's host
 * never receives it. `silo://` opens the app in every build; the https form
 * opens it where the build claims that link, and otherwise reaches a page
 * that explains how to get the app.
 */
export type JoinLinkForm = "silo" | "https";

const prefixes: Record<JoinLinkForm, string> = {
  silo: "silo://join#",
  https: "https://siloserver.org/join#",
};

function base64Url(text: string): string {
  let binary = "";
  for (const byte of new TextEncoder().encode(text)) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Builds the join link. `serverAddress` is the server's answer, as given. */
export function buildJoinLink(serverAddress: string, appCode: string, form: JoinLinkForm): string {
  return `${prefixes[form]}v1.${base64Url(serverAddress)}.${appCode}`;
}
