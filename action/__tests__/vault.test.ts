import { afterEach, describe, expect, it } from 'vitest';
import { FakeVault, defaultCreds } from './helpers/fakeVault.js';
import { VaultClient, isNotFound, isPermissionDenied } from '../src/vault.js';
import type { VaultError } from '../src/vault.js';

let vault: FakeVault | undefined;

afterEach(async () => {
  await vault?.stop();
  vault = undefined;
});

describe('VaultClient construction', () => {
  it('rejects an empty or invalid address', () => {
    expect(() => new VaultClient({ address: '' })).toThrow(/address is required/);
    expect(() => new VaultClient({ address: 'not a url' })).toThrow(/invalid vault address/);
    expect(() => new VaultClient({ address: 'ftp://vault' })).toThrow(/http or https/);
  });

  it('accepts a trailing slash', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: `${vault.address}/`, token: 'hvs.caller-token' });
    await expect(client.readCreds('harbor', 'ci-pull')).resolves.toMatchObject({ robotId: '42' });
  });
});

describe('loginJwt', () => {
  it('exchanges a JWT for a client token', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    const auth = await client.loginJwt('jwt', 'ci-pull', 'header.payload.sig');
    expect(auth.clientToken).toBe('hvs.fake-oidc-token');
    expect(auth.leaseDuration).toBe(3600);
    expect(vault.requests.at(-1)).toMatchObject({
      method: 'POST',
      path: '/v1/auth/jwt/login',
      body: { role: 'ci-pull', jwt: 'header.payload.sig' },
    });
  });

  it('reports an unknown role with Vault’s message', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    await expect(client.loginJwt('jwt', 'nope', 'jwt')).rejects.toThrow(
      /role "nope" could not be found/,
    );
  });

  it('reports an unknown auth mount as 404', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    const err = await client.loginJwt('missing', 'ci-pull', 'jwt').catch((e: unknown) => e);
    expect(isNotFound(err)).toBe(true);
    expect((err as VaultError).path).toBe('/v1/auth/missing/login');
  });

  it('normalises slashes in the mount path', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    await client.loginJwt('/jwt/', 'ci-pull', 'jwt');
    expect(vault.requests.at(-1)?.path).toBe('/v1/auth/jwt/login');
  });
});

describe('readCreds', () => {
  it('returns the credential fields and lease', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address, token: 'hvs.caller-token' });
    const creds = await client.readCreds('harbor', 'ci-pull');
    expect(creds).toMatchObject({
      username: 'robot$library+vault-ci-pull-1a2b3c4d',
      secret: 'Aa1supersecretvalue',
      robotId: '42',
      expiresAt: '2026-08-17T21:43:40Z',
      leaseId: 'harbor/creds/ci-pull/abc123',
      leaseDuration: 3600,
      renewable: true,
    });
    expect(creds.auth).toBe(defaultCreds().auth);
  });

  it('sends the token and namespace headers', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({
      address: vault.address,
      token: 'hvs.caller-token',
      namespace: 'team-a',
    });
    await client.readCreds('harbor', 'ci-pull');
    expect(vault.requests.at(-1)).toMatchObject({
      token: 'hvs.caller-token',
      namespace: 'team-a',
    });
  });

  it('surfaces a missing role', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address, token: 'hvs.caller-token' });
    await expect(client.readCreds('harbor', 'ghost')).rejects.toThrow(
      /role "ghost" does not exist/,
    );
  });

  it('surfaces permission denied', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address, token: 'hvs.wrong' });
    const err = await client.readCreds('harbor', 'ci-pull').catch((e: unknown) => e);
    expect(isPermissionDenied(err)).toBe(true);
    expect((err as Error).message).toMatch(/permission denied/);
  });

  it('surfaces Harbor errors passed through by the engine', async () => {
    vault = await FakeVault.start({
      failNext: {
        'GET /v1/harbor/creds/ci-pull': {
          status: 400,
          errors: [
            'harbor rejected robot creation: harbor: POST /api/v2.0/robots: 403 DENIED: permission scope is invalid. It must be equal to or more restrictive than the creator robot’s permissions',
          ],
        },
      },
    });
    const client = new VaultClient({ address: vault.address, token: 'hvs.caller-token' });
    await expect(client.readCreds('harbor', 'ci-pull')).rejects.toThrow(
      /permission scope is invalid/,
    );
  });

  it('rejects a response that is not a harbor credential', async () => {
    vault = await FakeVault.start({ creds: { 'kv/creds/ci-pull': { foo: 'bar' } } });
    const client = new VaultClient({ address: vault.address, token: 'hvs.caller-token' });
    await expect(client.readCreds('kv', 'ci-pull')).rejects.toThrow(/missing username or secret/);
  });
});

describe('revocation', () => {
  it('revokes a lease synchronously', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address, token: 'hvs.caller-token' });
    await client.revokeLease('harbor/creds/ci-pull/abc123');
    expect(vault.revokedLeases).toEqual(['harbor/creds/ci-pull/abc123']);
    expect(vault.requests.at(-1)?.body).toMatchObject({
      lease_id: 'harbor/creds/ci-pull/abc123',
      sync: true,
    });
  });

  it('revokes its own token', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address, token: 'hvs.fake-oidc-token' });
    await client.revokeSelfToken();
    expect(vault.revokedTokens).toEqual(['hvs.fake-oidc-token']);
  });

  it('propagates revocation failures', async () => {
    vault = await FakeVault.start({
      failNext: { 'POST /v1/sys/leases/revoke': { status: 500, errors: ['internal error'] } },
    });
    const client = new VaultClient({ address: vault.address, token: 'hvs.caller-token' });
    await expect(client.revokeLease('lease/1')).rejects.toThrow(/500: internal error/);
  });
});

describe('transport', () => {
  it('reports an unreachable host without leaking a stack', async () => {
    const client = new VaultClient({ address: 'http://127.0.0.1:1', timeoutMs: 2000 });
    await expect(client.readCreds('harbor', 'ci-pull')).rejects.toThrow(/vault: GET .*: /);
  });

  it('times out slow responses', async () => {
    vault = await FakeVault.start({ delayMs: 300 });
    const client = new VaultClient({
      address: vault.address,
      token: 'hvs.caller-token',
      timeoutMs: 50,
    });
    await expect(client.readCreds('harbor', 'ci-pull')).rejects.toThrow(/timed out/);
  });

  it('maps a non-JSON error body', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: `${vault.address}`, token: 'hvs.caller-token' });
    await expect(client.readCreds('harbor', 'does/not/exist')).rejects.toThrow(/vault: GET/);
  });

  it('hasToken reflects the current token', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    expect(client.hasToken()).toBe(false);
    client.setToken('hvs.caller-token');
    expect(client.hasToken()).toBe(true);
  });
});
