/**
 * In-memory fake of the Vault endpoints the action uses. Mirrors Vault's real
 * response envelopes and status codes so the client's error mapping is tested
 * against the shapes it will actually see.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';

export interface FakeVaultOptions {
  /** JWT auth roles that exist, mapped to the token handed out. */
  jwtRoles?: Record<string, string>;
  /** Valid tokens. */
  tokens?: Set<string>;
  /** Credentials returned for "<mount>/creds/<role>". */
  creds?: Record<string, Record<string, unknown>>;
  /** Force the next matching "METHOD /path" request to fail. */
  failNext?: Record<string, { status: number; errors: string[] }>;
  /** Extra latency in ms before responding, to exercise timeouts. */
  delayMs?: number;
}

export interface RecordedRequest {
  method: string;
  path: string;
  token?: string | undefined;
  namespace?: string | undefined;
  body?: unknown;
}

export class FakeVault {
  readonly requests: RecordedRequest[] = [];
  readonly revokedLeases: string[] = [];
  readonly revokedTokens: string[] = [];
  private readonly server: Server;
  private readonly jwtRoles: Record<string, string>;
  private readonly tokens: Set<string>;
  private readonly creds: Record<string, Record<string, unknown>>;
  private readonly failNext: Record<string, { status: number; errors: string[] }>;
  private readonly delayMs: number;

  private constructor(server: Server, opts: FakeVaultOptions) {
    this.server = server;
    this.jwtRoles = opts.jwtRoles ?? { 'ci-pull': 'hvs.fake-oidc-token' };
    this.tokens = opts.tokens ?? new Set(['hvs.fake-oidc-token', 'hvs.caller-token']);
    this.creds = opts.creds ?? { 'harbor/creds/ci-pull': defaultCreds() };
    this.failNext = opts.failNext ?? {};
    this.delayMs = opts.delayMs ?? 0;
  }

  static async start(opts: FakeVaultOptions = {}): Promise<FakeVault> {
    const server = createServer();
    const fake = new FakeVault(server, opts);
    server.on('request', (req, res) => void fake.handle(req, res));
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    return fake;
  }

  get address(): string {
    const addr = this.server.address() as AddressInfo;
    return `http://127.0.0.1:${addr.port}`;
  }

  async stop(): Promise<void> {
    await new Promise<void>((resolve, reject) =>
      this.server.close((err) => (err ? reject(err) : resolve())),
    );
  }

  private async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const chunks: Buffer[] = [];
    for await (const chunk of req) {
      chunks.push(chunk as Buffer);
    }
    const raw = Buffer.concat(chunks).toString('utf8');
    const body: unknown = raw === '' ? undefined : JSON.parse(raw);
    const path = req.url ?? '';
    const method = req.method ?? 'GET';
    const token = header(req, 'x-vault-token');
    this.requests.push({ method, path, token, namespace: header(req, 'x-vault-namespace'), body });

    if (this.delayMs > 0) {
      await new Promise((resolve) => setTimeout(resolve, this.delayMs));
    }

    const failKey = `${method} ${path}`;
    const failure = this.failNext[failKey];
    if (failure) {
      delete this.failNext[failKey];
      send(res, failure.status, { errors: failure.errors });
      return;
    }

    // Unauthenticated: JWT login.
    const loginMatch = /^\/v1\/auth\/([^/]+)\/login$/.exec(path);
    if (method === 'POST' && loginMatch) {
      const payload = body as { role?: string; jwt?: string } | undefined;
      if (loginMatch[1] !== 'jwt' && loginMatch[1] !== 'gha') {
        send(res, 404, { errors: [] });
        return;
      }
      if (!payload?.jwt) {
        send(res, 400, { errors: ['missing jwt'] });
        return;
      }
      const clientToken = payload.role ? this.jwtRoles[payload.role] : undefined;
      if (!clientToken) {
        send(res, 400, { errors: [`role "${payload.role ?? ''}" could not be found`] });
        return;
      }
      send(res, 200, {
        auth: { client_token: clientToken, accessor: 'accessor-1', lease_duration: 3600 },
      });
      return;
    }

    // Everything below requires a valid token.
    if (!token || !this.tokens.has(token)) {
      send(res, 403, { errors: ['permission denied'] });
      return;
    }

    const credsMatch = /^\/v1\/(.+)\/creds\/([^/]+)$/.exec(path);
    if (method === 'GET' && credsMatch) {
      const key = `${credsMatch[1] ?? ''}/creds/${credsMatch[2] ?? ''}`;
      const data = this.creds[key];
      if (!data) {
        send(res, 400, { errors: [`role "${credsMatch[2] ?? ''}" does not exist`] });
        return;
      }
      send(res, 200, {
        lease_id: `${key}/abc123`,
        lease_duration: 3600,
        renewable: true,
        data,
        warnings: null,
      });
      return;
    }

    if (method === 'POST' && path === '/v1/sys/leases/revoke') {
      const payload = body as { lease_id?: string } | undefined;
      if (!payload?.lease_id) {
        send(res, 400, { errors: ['lease_id must be specified'] });
        return;
      }
      this.revokedLeases.push(payload.lease_id);
      sendEmpty(res, 204);
      return;
    }

    if (method === 'POST' && path === '/v1/auth/token/revoke-self') {
      this.revokedTokens.push(token);
      this.tokens.delete(token);
      sendEmpty(res, 204);
      return;
    }

    send(res, 404, { errors: [] });
  }
}

export function defaultCreds(): Record<string, unknown> {
  return {
    username: 'robot$library+vault-ci-pull-1a2b3c4d',
    secret: 'Aa1supersecretvalue',
    auth: Buffer.from('robot$library+vault-ci-pull-1a2b3c4d:Aa1supersecretvalue').toString(
      'base64',
    ),
    robot_id: 42,
    expires_at: '2026-08-17T21:43:40Z',
  };
}

function header(req: IncomingMessage, name: string): string | undefined {
  const value = req.headers[name];
  return Array.isArray(value) ? value[0] : value;
}

function send(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'content-type': 'application/json' });
  res.end(JSON.stringify(body));
}

function sendEmpty(res: ServerResponse, status: number): void {
  res.writeHead(status);
  res.end();
}
