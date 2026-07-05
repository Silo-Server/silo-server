const PLEX_TV_BASE_URL = "https://plex.tv";
const PLEX_AUTH_BASE_URL = "https://app.plex.tv/auth#?";
const PLEX_PRODUCT = "Silo";
const PLEX_VERSION = "1.0.0";
const PLEX_CLIENT_ID_STORAGE_KEY = "silo-plex-history-import-client-id";

interface PlexPinResponse {
  id: number;
  code: string;
}

interface PlexPinCheckResponse {
  authToken: string | null;
}

interface PlexResourceEntry {
  name: string;
  product: string;
  clientIdentifier: string;
  provides: string;
  ownerId?: number;
  owned: boolean;
  accessToken: string;
  connections: Array<{
    protocol: string;
    address: string;
    port: number;
    uri: string;
    local: boolean;
  }>;
}

export interface BrowserPlexServer {
  name: string;
  clientIdentifier: string;
  accessToken: string;
  remoteURL: string;
  localURL: string;
  owned: boolean;
  hasRemoteURL: boolean;
  hasLocalURL: boolean;
}

function getStoredPlexClientIdentifier(): string | null {
  try {
    return localStorage.getItem(PLEX_CLIENT_ID_STORAGE_KEY);
  } catch {
    return null;
  }
}

function setStoredPlexClientIdentifier(value: string) {
  try {
    localStorage.setItem(PLEX_CLIENT_ID_STORAGE_KEY, value);
  } catch {
    // Ignore storage failures and fall back to an in-memory identifier for this session.
  }
}

export function getPlexClientIdentifier(): string {
  const stored = getStoredPlexClientIdentifier();
  if (stored) {
    return stored;
  }

  const generated =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `silo-${Date.now()}`;
  setStoredPlexClientIdentifier(generated);
  return generated;
}

function buildPlexHeaders(token?: string): HeadersInit {
  const headers: Record<string, string> = {
    Accept: "application/json",
    "X-Plex-Client-Identifier": getPlexClientIdentifier(),
    "X-Plex-Product": PLEX_PRODUCT,
    "X-Plex-Version": PLEX_VERSION,
    "X-Plex-Platform": navigator.platform || "Web",
    "X-Plex-Device": "Browser",
    "X-Plex-Device-Name": "Silo Web",
  };
  if (token) {
    headers["X-Plex-Token"] = token;
  }
  return headers;
}

async function readJSON<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const body = (await response.text()).trim();
    throw new Error(body || `Plex request failed with status ${response.status}`);
  }
  return (await response.json()) as T;
}

export async function createPlexPin(): Promise<PlexPinResponse> {
  const response = await fetch(`${PLEX_TV_BASE_URL}/api/v2/pins`, {
    method: "POST",
    headers: {
      ...buildPlexHeaders(),
      "Content-Type": "application/x-www-form-urlencoded",
    },
    body: new URLSearchParams({ strong: "true" }).toString(),
  });
  const pin = await readJSON<PlexPinResponse>(response);
  if (!pin.id || !pin.code) {
    throw new Error("Plex sign-in returned an invalid PIN response.");
  }
  return pin;
}

export function buildPlexAuthURL(pinCode: string, forwardURL: string): string {
  const params = new URLSearchParams({
    clientID: getPlexClientIdentifier(),
    code: pinCode,
    forwardUrl: forwardURL,
  });
  params.set("context[device][product]", PLEX_PRODUCT);
  return `${PLEX_AUTH_BASE_URL}${params.toString()}`;
}

export async function checkPlexPin(pinID: number, pinCode: string): Promise<string | null> {
  const url = new URL(`${PLEX_TV_BASE_URL}/api/v2/pins/${pinID}`);
  url.searchParams.set("code", pinCode);
  const response = await fetch(url.toString(), {
    method: "GET",
    headers: buildPlexHeaders(),
  });
  const result = await readJSON<PlexPinCheckResponse>(response);
  return result.authToken;
}

export async function listPlexResources(token: string): Promise<BrowserPlexServer[]> {
  const response = await fetch(
    `${PLEX_TV_BASE_URL}/api/v2/resources?includeHttps=1&includeRelay=1`,
    {
      method: "GET",
      headers: buildPlexHeaders(token),
    },
  );
  const resources = await readJSON<PlexResourceEntry[]>(response);
  return resources
    .filter((entry) => entry.provides.includes("server"))
    .map((entry) => {
      const server: BrowserPlexServer = {
        name: entry.name,
        clientIdentifier: entry.clientIdentifier,
        accessToken: entry.accessToken,
        remoteURL: "",
        localURL: "",
        owned: entry.owned,
        hasRemoteURL: false,
        hasLocalURL: false,
      };

      for (const connection of entry.connections) {
        if (connection.local) {
          server.localURL = connection.uri;
          server.hasLocalURL = true;
        } else {
          server.remoteURL = connection.uri;
          server.hasRemoteURL = true;
        }
      }

      return server;
    });
}

export interface PlexAuthenticationResult {
  /** The plex.tv account token; needed for account-level APIs like the watchlist. */
  accountToken: string;
  servers: BrowserPlexServer[];
}

export async function completePlexAuthentication(
  pinID: number,
  pinCode: string,
): Promise<PlexAuthenticationResult> {
  const authToken = await checkPlexPin(pinID, pinCode);
  if (!authToken) {
    throw new Error("Plex sign-in was not completed. Please try again.");
  }
  const servers = await listPlexResources(authToken);
  return { accountToken: authToken, servers };
}

export function getPreferredPlexServerURL(server: BrowserPlexServer): string {
  return server.remoteURL || server.localURL;
}
