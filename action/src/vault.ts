/**
 * Minimal typed client for the handful of Vault/OpenBao endpoints this action
 * needs. Deliberately dependency-light (undici only) so the bundled runtime
 * stays reviewable.
 */
import { Agent, fetch, type Dispatcher } from 'undici';

/** Error carrying Vault's status, path and message. */
export class VaultError extends Error {
  readonly status: number;
  readonly path: string;
  readonly method: string;
  readonly errors: string[];

  constructor(method: string, path: string, status: number, errors: string[]) {
    const detail = errors.length > 0 ? errors.join('; ') : `HTTP ${status}`;
    super(`vault: ${method} ${path}: ${status}: ${detail}`);
    this.name = 'VaultError';
    this.status = status;
    this.path = path;
    this.method = method;
    this.errors = errors;
  }
}

export function isNotFound(err: unknown): boolean {
  return err instanceof VaultError && err.status === 404;
}

export function isPermissionDenied(err: unknown): boolean {
  return err instanceof VaultError && (err.status === 403 || err.status === 401);
}

export interface VaultClientOptions {
  /** Base URL, e.g. https://vault.example.com (with or without trailing slash). */
  address: string;
  /** Vault token; may be set later with setToken(). */
  token?: string | undefined;
  /** Vault Enterprise namespace. */
  namespace?: string | undefined;
  /** PEM CA bundle used to verify Vault's certificate. */
  caCert?: string | undefined;
  /** Skip TLS verification. Discouraged. */
  tlsSkipVerify?: boolean | undefined;
  /** Per-request timeout in milliseconds. Default 30s. */
  timeoutMs?: number | undefined;
}

export interface AuthResponse {
  clientToken: string;
  accessor: string;
  leaseDuration: number;
}

export interface CredsResponse {
  leaseId: string;
  leaseDuration: number;
  renewable: boolean;
  username: string;
  secret: string;
  auth: string;
  robotId: string;
  expiresAt: string;
  warnings: string[];
}

interface VaultEnvelope {
  errors?: string[];
  warnings?: string[] | null;
  lease_id?: string;
  lease_duration?: number;
  renewable?: boolean;
  data?: Record<string, unknown> | null;
  auth?: {
    client_token?: string;
    accessor?: string;
    lease_duration?: number;
  } | null;
}

const DEFAULT_TIMEOUT_MS = 30_000;

export class VaultClient {
  private readonly base: string;
  private readonly namespace: string | undefined;
  private readonly timeoutMs: number;
  private readonly dispatcher: Dispatcher | undefined;
  private currentToken: string | undefined;

  constructor(opts: VaultClientOptions) {
    const address = opts.address.trim();
    if (address === '') {
      throw new Error('vault address is required');
    }
    let parsed: URL;
    try {
      parsed = new URL(address);
    } catch {
      throw new Error(`invalid vault address: ${address}`);
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      throw new Error(
        `vault address must use http or https, got ${parsed.protocol.replace(':', '')}`,
      );
    }
    this.base = address.replace(/\/+$/, '');
    this.namespace = opts.namespace;
    this.currentToken = opts.token;
    this.timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.dispatcher = buildDispatcher(opts);
  }

  setToken(token: string): void {
    this.currentToken = token;
  }

  hasToken(): boolean {
    return this.currentToken !== undefined && this.currentToken !== '';
  }

  /** The token currently in use, if any. */
  token(): string | undefined {
    return this.currentToken;
  }

  /** Exchanges a JWT/OIDC token for a Vault token at auth/<mount>/login. */
  async loginJwt(authMount: string, role: string, jwt: string): Promise<AuthResponse> {
    const path = `/v1/auth/${trimSlashes(authMount)}/login`;
    const body = await this.request('POST', path, { role, jwt });
    const auth = body?.auth;
    if (!auth?.client_token) {
      throw new VaultError('POST', path, 200, ['login response did not contain a client token']);
    }
    return {
      clientToken: auth.client_token,
      accessor: auth.accessor ?? '',
      leaseDuration: auth.lease_duration ?? 0,
    };
  }

  /** Reads dynamic credentials from <mount>/creds/<role>. */
  async readCreds(mount: string, role: string): Promise<CredsResponse> {
    const path = `/v1/${trimSlashes(mount)}/creds/${encodeURIComponent(role)}`;
    const body = await this.request('GET', path);
    const data = body?.data;
    if (!data) {
      throw new VaultError('GET', path, 200, ['credential response did not contain data']);
    }
    const username = stringField(data, 'username');
    const secret = stringField(data, 'secret');
    if (username === '' || secret === '') {
      throw new VaultError('GET', path, 200, [
        'credential response is missing username or secret; is this mount a harbor secrets engine?',
      ]);
    }
    return {
      leaseId: body.lease_id ?? '',
      leaseDuration: body.lease_duration ?? 0,
      renewable: body.renewable ?? false,
      username,
      secret,
      auth: stringField(data, 'auth'),
      robotId: stringField(data, 'robot_id'),
      expiresAt: stringField(data, 'expires_at'),
      warnings: body.warnings ?? [],
    };
  }

  /**
   * Revokes a lease. sync=true makes Vault complete the revocation before
   * responding, so the Harbor robot is gone before the job ends.
   */
  async revokeLease(leaseId: string, sync = true): Promise<void> {
    await this.request('POST', '/v1/sys/leases/revoke', { lease_id: leaseId, sync });
  }

  /** Revokes the token this client is using. */
  async revokeSelfToken(): Promise<void> {
    await this.request('POST', '/v1/auth/token/revoke-self');
  }

  private async request(
    method: string,
    path: string,
    payload?: unknown,
  ): Promise<VaultEnvelope | undefined> {
    const headers: Record<string, string> = { accept: 'application/json' };
    if (this.currentToken) {
      headers['x-vault-token'] = this.currentToken;
    }
    if (this.namespace) {
      headers['x-vault-namespace'] = this.namespace;
    }
    if (payload !== undefined) {
      headers['content-type'] = 'application/json';
    }

    let response;
    try {
      response = await fetch(`${this.base}${path}`, {
        method,
        headers,
        ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
        signal: AbortSignal.timeout(this.timeoutMs),
        ...(this.dispatcher ? { dispatcher: this.dispatcher } : {}),
      });
    } catch (err) {
      throw new Error(`vault: ${method} ${path}: ${describeNetworkError(err)}`);
    }

    const text = await response.text();
    const envelope = parseEnvelope(text);
    if (response.status < 200 || response.status > 299) {
      throw new VaultError(method, path, response.status, envelope?.errors ?? plainErrors(text));
    }
    return envelope;
  }
}

function buildDispatcher(opts: VaultClientOptions): Dispatcher | undefined {
  const wantsTls = opts.caCert !== undefined && opts.caCert !== '';
  if (!wantsTls && !opts.tlsSkipVerify) {
    return undefined;
  }
  return new Agent({
    connect: {
      ...(wantsTls ? { ca: opts.caCert } : {}),
      ...(opts.tlsSkipVerify ? { rejectUnauthorized: false } : {}),
    },
  });
}

function parseEnvelope(text: string): VaultEnvelope | undefined {
  if (text.trim() === '') {
    return undefined;
  }
  try {
    return JSON.parse(text) as VaultEnvelope;
  } catch {
    return undefined;
  }
}

/** Falls back to the raw body when Vault (or a proxy) returns non-JSON. */
function plainErrors(text: string): string[] {
  const trimmed = text.trim();
  return trimmed === '' ? [] : [trimmed.slice(0, 500)];
}

/**
 * Vault returns JSON, so a field may arrive as a string, a number (robot_id) or
 * a boolean. Anything structured is serialised rather than stringified, so a
 * surprise never reaches a log as "[object Object]".
 */
function stringField(data: Record<string, unknown>, key: string): string {
  const value = data[key];
  if (value === undefined || value === null) {
    return '';
  }
  if (typeof value === 'string') {
    return value;
  }
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    return value.toString();
  }
  return JSON.stringify(value);
}

function trimSlashes(value: string): string {
  return value.replace(/^\/+|\/+$/g, '');
}

function describeNetworkError(err: unknown): string {
  if (err instanceof Error) {
    if (err.name === 'TimeoutError' || err.name === 'AbortError') {
      return 'request timed out';
    }
    const cause = (err as { cause?: unknown }).cause;
    if (cause instanceof Error && cause.message !== '') {
      return `${err.message} (${cause.message})`;
    }
    return err.message;
  }
  return String(err);
}
