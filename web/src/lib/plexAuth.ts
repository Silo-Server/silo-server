import { randomUUID } from "./uuid";

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
  /** Every address Plex advertised, in the order it advertised them. */
  connectionURLs: string[];
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

  const generated = randomUUID();
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
        connectionURLs: entry.connections.map((connection) => connection.uri),
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

/** A member of the signed-in account's Plex Home, including managed users. */
export interface PlexHomeUser {
  uuid: string;
  title: string;
  /** Switching to this user needs their Plex Home PIN. */
  protected: boolean;
  /** A managed user: a Home member without a plex.tv login of their own. */
  restricted: boolean;
}

interface PlexHomeUsersResponse {
  users?: Array<{
    uuid?: string;
    title?: string;
    username?: string;
    protected?: boolean;
    restricted?: boolean;
  }>;
}

/**
 * The Plex Home members the signed-in account can switch to. An account outside
 * a Plex Home, or a plex.tv error, yields an empty list: Home users are an
 * option on top of importing the signed-in account, never a requirement.
 */
async function listPlexHomeUsers(token: string): Promise<PlexHomeUser[]> {
  try {
    const response = await fetch(`${PLEX_TV_BASE_URL}/api/v2/home/users`, {
      method: "GET",
      headers: buildPlexHeaders(token),
    });
    const home = await readJSON<PlexHomeUsersResponse>(response);
    return (home.users ?? [])
      .filter((user) => !!user.uuid)
      .map((user) => ({
        uuid: user.uuid as string,
        title: user.title || user.username || "Plex user",
        protected: user.protected === true,
        restricted: user.restricted === true,
      }));
  } catch {
    return [];
  }
}

async function getPlexAccountUUID(token: string): Promise<string> {
  const response = await fetch(`${PLEX_TV_BASE_URL}/api/v2/user`, {
    method: "GET",
    headers: buildPlexHeaders(token),
  });
  const account = await readJSON<{ uuid?: string }>(response);
  return account.uuid ?? "";
}

/**
 * Switches from the signed-in account to another Plex Home member and returns
 * that member's plex.tv token. plex.tv checks the PIN for a protected member.
 */
async function switchPlexHomeUser(token: string, userUUID: string, pin?: string): Promise<string> {
  const url = new URL(
    `${PLEX_TV_BASE_URL}/api/v2/home/users/${encodeURIComponent(userUUID)}/switch`,
  );
  if (pin) {
    url.searchParams.set("pin", pin);
  }
  const response = await fetch(url.toString(), {
    method: "POST",
    headers: buildPlexHeaders(token),
  });
  if (response.status === 401 || response.status === 403) {
    throw new Error(
      pin
        ? "Plex did not accept that PIN. Check the PIN for this Plex Home user and try again."
        : "This Plex Home user is protected by a PIN. Enter their PIN and try again.",
    );
  }
  const user = await readJSON<{ authToken?: string }>(response);
  if (!user.authToken) {
    throw new Error("Plex did not return a sign-in for this Plex Home user.");
  }
  return user.authToken;
}

/**
 * Signs in as a Plex Home member and finds the chosen server as they see it,
 * so the import uses that member's own server token and watch state.
 */
export async function resolvePlexHomeUserServer(
  token: string,
  user: PlexHomeUser,
  pin: string,
  serverIdentifier: string,
): Promise<{ accountToken: string; server: BrowserPlexServer }> {
  const userToken = await switchPlexHomeUser(token, user.uuid, pin || undefined);
  const servers = await listPlexResources(userToken);
  const server = servers.find((candidate) => candidate.clientIdentifier === serverIdentifier);
  if (!server) {
    throw new Error(
      `${user.title} does not have access to this Plex server. Share the server with them in Plex, or choose another server.`,
    );
  }
  return { accountToken: userToken, server };
}

export interface PlexHome {
  /** The signed-in account's plex.tv UUID, to tell it apart in users. */
  accountUUID: string;
  /** Plex Home members, the signed-in account included; empty outside a Home. */
  users: PlexHomeUser[];
}

/** Looks up the signed-in account's Plex Home; never fails the sign-in. */
export async function loadPlexHome(token: string): Promise<PlexHome> {
  const [users, accountUUID] = await Promise.all([
    listPlexHomeUsers(token),
    getPlexAccountUUID(token).catch(() => ""),
  ]);
  // Without the account's UUID every member looks like someone else, so
  // treat that as no Home.
  return accountUUID ? { accountUUID, users } : { accountUUID: "", users: [] };
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

/**
 * The most `plex_base_urls` entries createHistoryImportRun accepts. The server
 * filters the list by its address policy before choosing which to race, so the
 * client sends everything up to this bound.
 */
export const MAX_PLEX_FALLBACK_URLS = 31;

/**
 * The other addresses Plex advertised for a server, for a run's
 * `plex_base_urls`; the preferred one goes in `plex_base_url`. HTTPS addresses
 * come first so the request limit never cuts one in favor of a cleartext
 * address, which the server drops whenever an HTTPS one exists.
 */
export function getPlexFallbackURLs(server: BrowserPlexServer): string[] {
  const preferred = getPreferredPlexServerURL(server).trim();
  const fallbacks = [
    ...new Set(server.connectionURLs.map((candidate) => candidate.trim()).filter(Boolean)),
  ].filter((candidate) => candidate !== preferred);
  const isSecure = (candidate: string) => candidate.toLowerCase().startsWith("https://");
  return [
    ...fallbacks.filter(isSecure),
    ...fallbacks.filter((candidate) => !isSecure(candidate)),
  ].slice(0, MAX_PLEX_FALLBACK_URLS);
}
