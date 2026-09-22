/**
 * Typed API client for Halp.
 *
 * SPECIFICATION (docs/API.md):
 * - Authorization: Bearer <session> for authenticated endpoints.
 * - X-Halp-PoW: <epoch>.<nonce> on mutating requests.
 * - Rate-limited and blocked sends return the SAME 202 as accepted ones.
 * - INVARIANT: The 16-digit account key NEVER leaves the device.
 *   Only auth_secret is transmitted (to POST /v1/session).
 */

import { PoWEngine, DIFFICULTY_FLOOR, DIFFICULTY_SIGNUP, getCurrentEpoch } from './pow.js';
import { deriveAuthSecretHex } from './key.js';
import { normalizeAccountKey } from './validation.js';

export interface HalpClientOptions {
  baseUrl?: string;
  sessionToken?: string | null;
  fetchFn?: typeof fetch;
  powEngine?: PoWEngine;
  challengeProvider?: (epoch: number) => Promise<Uint8Array> | Uint8Array;
}

export interface AccountInfo {
  region: string;
  created_day: number;
  counters?: {
    received_total?: number;
  };
}

export interface HandleDTO {
  handle: string;
  label?: string;
  kind: 'personal' | 'social' | 'stream' | 'group';
  paused: boolean;
  created_day: number;
  policy?: Record<string, unknown>;
}

export interface SettingsDTO {
  digest_window_s: number;
  max_per_hour: number;
  quiet_start?: number | null;
  quiet_end?: number | null;
  tz?: string | null;
  min_count: number;
  mode: 'all' | 'groups_only' | 'paused';
}

export interface SessionResponse {
  token: string;
  expires_at: number;
}

export interface SubscriptionDTO {
  endpoint: string;
  p256dh: string;
  auth: string;
  kind?: string;
}

export interface GroupDTO {
  id: number;
  name: string;
  invite_code?: string;
}

export interface GroupMemberDTO {
  display_name: string;
  note?: string;
  handle: string;
  role?: string;
}

export class HalpApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message?: string
  ) {
    super(message || `API error ${status}: ${code}`);
    this.name = 'HalpApiError';
  }
}

export class HalpClient {
  private baseUrl: string;
  private sessionToken: string | null = null;
  private fetch: typeof fetch;
  private pow: PoWEngine;
  private challengeProvider: (epoch: number) => Promise<Uint8Array> | Uint8Array;

  constructor(options: HalpClientOptions = {}) {
    this.baseUrl = (options.baseUrl || '').replace(/\/+$/, '');
    this.sessionToken = options.sessionToken ?? null;
    this.fetch = options.fetchFn ?? globalThis.fetch.bind(globalThis);
    this.pow = options.powEngine ?? new PoWEngine();
    this.challengeProvider = options.challengeProvider ?? (() => new Uint8Array(32));
  }

  setSessionToken(token: string | null): void {
    this.sessionToken = token;
  }

  getSessionToken(): string | null {
    return this.sessionToken;
  }

  getBaseUrl(): string {
    return this.baseUrl;
  }

  private async request<T = unknown>(
    method: string,
    path: string,
    body?: unknown,
    customHeaders?: Record<string, string>
  ): Promise<T> {
    const url = `${this.baseUrl}${path.startsWith('/') ? path : `/${path}`}`;
    const headers: Record<string, string> = {
      'Accept': 'application/json',
      ...customHeaders,
    };

    if (this.sessionToken && !headers['Authorization']) {
      headers['Authorization'] = `Bearer ${this.sessionToken}`;
    }

    let requestBody: string | undefined;
    if (body !== undefined && body !== null) {
      headers['Content-Type'] = 'application/json';
      requestBody = JSON.stringify(body);
    }

    const res = await this.fetch(url, {
      method,
      headers,
      body: requestBody,
    });

    if (!res.ok) {
      let errCode = 'unknown_error';
      try {
        const json = await res.json();
        if (json && json.error) {
          errCode = json.error;
        }
      } catch {
        // Fallback for non-JSON error bodies
      }
      throw new HalpApiError(res.status, errCode);
    }

    if (res.status === 204 || res.status === 202) {
      return undefined as T;
    }

    const contentType = res.headers.get('content-type');
    if (contentType && contentType.includes('application/json')) {
      return (await res.json()) as T;
    }

    return (await res.text()) as unknown as T;
  }

  // ---------------------------------------------------------------------------
  // Account endpoints (docs/API.md § Account)
  // ---------------------------------------------------------------------------

  /**
   * Registers a fresh account.
   * INVARIANT: The server returns the 16-digit account_key once.
   * The client must store it securely and never transmit it again.
   */
  async createAccount(powToken?: string): Promise<{ account_key: string }> {
    let token = powToken;
    if (!token) {
      const epoch = getCurrentEpoch();
      const challenge = await this.challengeProvider(epoch);
      const solved = await this.pow.getOrSolveToken('signup', epoch, challenge, DIFFICULTY_SIGNUP);
      token = solved.headerValue;
    }

    return this.request<{ account_key: string }>('POST', '/v1/accounts', undefined, {
      'X-Halp-PoW': token,
    });
  }

  /**
   * Authenticates with auth_secret derived from the 16-digit account key.
   * INVARIANT: The account key is NEVER sent. Only auth_secret is transmitted.
   */
  async loginWithAccountKey(accountKey: string): Promise<SessionResponse> {
    const authSecretHex = await deriveAuthSecretHex(accountKey);
    return this.createSession(authSecretHex);
  }

  /**
   * Directly creates a session with a hex auth_secret.
   */
  async createSession(authSecretHex: string): Promise<SessionResponse> {
    const res = await this.request<SessionResponse>('POST', '/v1/session', {
      auth_secret: authSecretHex,
    });
    this.sessionToken = res.token;
    return res;
  }

  /**
   * Terminates the current session.
   */
  async deleteSession(): Promise<void> {
    await this.request('DELETE', '/v1/session');
    this.sessionToken = null;
  }

  /**
   * Retrieves account metadata and total received counters.
   */
  async getAccount(): Promise<AccountInfo> {
    return this.request<AccountInfo>('GET', '/v1/account');
  }

  /**
   * GDPR full export of account rows.
   */
  async exportAccount(): Promise<Record<string, unknown>> {
    return this.request<Record<string, unknown>>('GET', '/v1/account/export');
  }

  /**
   * Synchronously and permanently deletes the account.
   */
  async deleteAccount(): Promise<void> {
    await this.request('DELETE', '/v1/account');
    this.sessionToken = null;
  }

  // ---------------------------------------------------------------------------
  // Handles and alias (docs/API.md § Handles and alias)
  // ---------------------------------------------------------------------------

  /**
   * Lists all handles owned by the account.
   */
  async listHandles(): Promise<HandleDTO[]> {
    const res = await this.request<{ handles: HandleDTO[] }>('GET', '/v1/handles');
    return res.handles || [];
  }

  /**
   * Creates a fresh handle with the given label and kind.
   */
  async createHandle(label: string = '', kind: 'personal' | 'social' | 'stream' = 'personal'): Promise<HandleDTO> {
    return this.request<HandleDTO>('POST', '/v1/handles', { label, kind });
  }

  /**
   * Updates handle label or paused state.
   */
  async updateHandle(handle: string, update: { label?: string; paused?: boolean; policy?: Record<string, unknown> }): Promise<void> {
    await this.request('PATCH', `/v1/handles/${encodeURIComponent(handle)}`, update);
  }

  /**
   * Burns a handle permanently.
   */
  async deleteHandle(handle: string): Promise<void> {
    await this.request('DELETE', `/v1/handles/${encodeURIComponent(handle)}`);
  }

  /**
   * Sets vanity alias pointing to an owned handle.
   */
  async setAlias(alias: string, handle: string): Promise<void> {
    await this.request('PUT', '/v1/alias', { alias, handle });
  }

  /**
   * Removes vanity alias.
   */
  async deleteAlias(): Promise<void> {
    await this.request('DELETE', '/v1/alias');
  }

  // ---------------------------------------------------------------------------
  // Sending (docs/API.md § Sending)
  // ---------------------------------------------------------------------------

  /**
   * Sends an appreciation ping to target handle or alias.
   * Attaches X-Halp-PoW header and returns 202 in < 3 ms.
   *
   * INVARIANT 7: Rate-limited, deduped, blocked and delivered pings all return identical 202.
   */
  async sendPing(target: string, powToken?: string): Promise<void> {
    let token = powToken;
    if (!token) {
      const epoch = getCurrentEpoch();
      const challenge = await this.challengeProvider(epoch);
      const solved = await this.pow.getOrSolveToken(target, epoch, challenge, DIFFICULTY_FLOOR);
      token = solved.headerValue;
    }

    await this.request('POST', `/v1/ping/${encodeURIComponent(target)}`, undefined, {
      'X-Halp-PoW': token,
    });
  }

  // ---------------------------------------------------------------------------
  // Receiving & Settings (docs/API.md § Receiving)
  // ---------------------------------------------------------------------------

  /**
   * Registers a Web Push subscription.
   */
  async createSubscription(sub: SubscriptionDTO): Promise<{ id: number }> {
    return this.request<{ id: number }>('POST', '/v1/subscriptions', sub);
  }

  /**
   * Deletes a subscription.
   */
  async deleteSubscription(id: number): Promise<void> {
    await this.request('DELETE', `/v1/subscriptions/${id}`);
  }

  /**
   * Queries pending coalesced ping count (for cold starts and service workers).
   */
  async getPending(): Promise<{ n: number }> {
    return this.request<{ n: number }>('GET', '/v1/pending');
  }

  /**
   * Gets account delivery settings.
   */
  async getSettings(): Promise<SettingsDTO> {
    return this.request<SettingsDTO>('GET', '/v1/settings');
  }

  /**
   * Updates delivery settings presets.
   */
  async updateSettings(settings: Partial<SettingsDTO>): Promise<void> {
    await this.request('PUT', '/v1/settings', settings);
  }

  /**
   * Reports abuse on a handle to mute high-frequency senders.
   */
  async reportAbuse(handle: string): Promise<{ status: string }> {
    return this.request<{ status: string }>('POST', `/v1/handles/${encodeURIComponent(handle)}/report-abuse`);
  }

  // ---------------------------------------------------------------------------
  // Groups (docs/API.md § Groups)
  // ---------------------------------------------------------------------------

  async createGroup(name: string): Promise<{ group_id: number; invite_code: string }> {
    return this.request('POST', '/v1/groups', { name });
  }

  async listGroups(): Promise<GroupDTO[]> {
    const res = await this.request<{ groups: GroupDTO[] }>('GET', '/v1/groups');
    return res.groups || [];
  }

  async listGroupMembers(groupId: number): Promise<GroupMemberDTO[]> {
    const res = await this.request<{ members: GroupMemberDTO[] }>('GET', `/v1/groups/${groupId}/members`);
    return res.members || [];
  }

  async joinGroup(inviteCode: string, displayName: string): Promise<{ group_id: number; my_handle: string }> {
    return this.request('POST', '/v1/groups/join', { invite_code: inviteCode, display_name: displayName });
  }

  async leaveGroup(groupId: number): Promise<void> {
    await this.request('DELETE', `/v1/groups/${groupId}/membership`);
  }

  async removeGroupMember(groupId: number, memberHandle: string): Promise<void> {
    await this.request('DELETE', `/v1/groups/${groupId}/members/${encodeURIComponent(memberHandle)}`);
  }

  async rotateGroupInvite(groupId: number): Promise<{ invite_code: string }> {
    return this.request('POST', `/v1/groups/${groupId}/invite`);
  }

  async updateGroup(groupId: number, patch: { name?: string; policy?: Record<string, unknown> }): Promise<void> {
    await this.request('PATCH', `/v1/groups/${groupId}`, patch);
  }

  async deleteGroup(groupId: number): Promise<void> {
    await this.request('DELETE', `/v1/groups/${groupId}`);
  }

  // ---------------------------------------------------------------------------
  // Contacts (docs/API.md § Contacts)
  // ---------------------------------------------------------------------------

  async getContacts(): Promise<{ version: number; blob: string }> {
    return this.request('GET', '/v1/contacts');
  }

  async updateContacts(blob: string, version: number): Promise<void> {
    await this.request('PUT', '/v1/contacts', { blob }, {
      'If-Match': version.toString(),
    });
  }

  async deleteContacts(): Promise<void> {
    await this.request('DELETE', '/v1/contacts');
  }
}
