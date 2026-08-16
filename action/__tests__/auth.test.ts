import { afterEach, describe, expect, it, vi } from 'vitest';
import { authenticate, oidcAvailable } from '../src/auth.js';
import { parseInputs, type InputSource } from '../src/inputs.js';
import { VaultClient } from '../src/vault.js';
import { FakeVault } from './helpers/fakeVault.js';

let vault: FakeVault | undefined;

afterEach(async () => {
  await vault?.stop();
  vault = undefined;
});

const oidcEnv = {
  ACTIONS_ID_TOKEN_REQUEST_URL: 'https://token.actions.githubusercontent.com',
  ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'runner-token',
} as NodeJS.ProcessEnv;

function inputsFrom(overrides: Record<string, string>, env: Record<string, string> = {}) {
  const source: InputSource = {
    getInput: (name) =>
      ({
        registry: 'harbor.example.com',
        role: 'ci-pull',
        'vault-url': 'https://vault',
        ...overrides,
      })[name] ?? '',
    getEnv: (name) => env[name],
  };
  return parseInputs(source);
}

describe('oidcAvailable', () => {
  it('detects the runner OIDC variables', () => {
    expect(oidcAvailable(oidcEnv)).toBe(true);
    expect(oidcAvailable({} as NodeJS.ProcessEnv)).toBe(false);
    expect(oidcAvailable({ ACTIONS_ID_TOKEN_REQUEST_URL: 'x' } as NodeJS.ProcessEnv)).toBe(false);
  });
});

describe('authenticate', () => {
  it('uses a supplied token and does not touch OIDC', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    const getIdToken = vi.fn();
    const result = await authenticate(
      client,
      inputsFrom({ 'vault-url': vault.address, 'vault-token': 'hvs.caller-token' }),
      { getIdToken, env: oidcEnv },
    );
    expect(result.actionOwned).toBe(false);
    expect(getIdToken).not.toHaveBeenCalled();
    expect(client.token()).toBe('hvs.caller-token');
  });

  it('exchanges an OIDC token and claims ownership', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    const getIdToken = vi.fn().mockResolvedValue('header.payload.sig');
    const result = await authenticate(client, inputsFrom({ 'vault-url': vault.address }), {
      getIdToken,
      env: oidcEnv,
    });
    expect(result.actionOwned).toBe(true);
    expect(client.token()).toBe('hvs.fake-oidc-token');
    expect(getIdToken).toHaveBeenCalledWith(undefined);
  });

  it('passes the configured audience through', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    const getIdToken = vi.fn().mockResolvedValue('jwt');
    await authenticate(
      client,
      inputsFrom({ 'vault-url': vault.address, audience: 'https://vault.example.com' }),
      { getIdToken, env: oidcEnv },
    );
    expect(getIdToken).toHaveBeenCalledWith('https://vault.example.com');
  });

  it('uses auth-role rather than role when set', async () => {
    vault = await FakeVault.start({ jwtRoles: { gha: 'hvs.fake-oidc-token' } });
    const client = new VaultClient({ address: vault.address });
    await authenticate(client, inputsFrom({ 'vault-url': vault.address, 'auth-role': 'gha' }), {
      getIdToken: vi.fn().mockResolvedValue('jwt'),
      env: oidcEnv,
    });
    expect(vault.requests.at(-1)?.body).toMatchObject({ role: 'gha' });
  });

  it('explains how to enable OIDC when it is unavailable', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    await expect(
      authenticate(client, inputsFrom({ 'vault-url': vault.address }), {
        getIdToken: vi.fn(),
        env: {} as NodeJS.ProcessEnv,
      }),
    ).rejects.toThrow(/permissions: id-token: write.*vault-token/s);
  });

  it('explains an unknown JWT role, naming role, mount and audience', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    await expect(
      authenticate(client, inputsFrom({ 'vault-url': vault.address, 'auth-role': 'ghost' }), {
        getIdToken: vi.fn().mockResolvedValue('jwt'),
        env: oidcEnv,
      }),
    ).rejects.toThrow(/role "ghost".*bound_audiences.*default audience/s);
  });

  it('explains a missing auth mount', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    await expect(
      authenticate(client, inputsFrom({ 'vault-url': vault.address, 'auth-mount': 'missing' }), {
        getIdToken: vi.fn().mockResolvedValue('jwt'),
        env: oidcEnv,
      }),
    ).rejects.toThrow(/no auth method mounted at "missing".*auth-mount input/s);
  });

  it('reports a failure to obtain the OIDC token', async () => {
    vault = await FakeVault.start();
    const client = new VaultClient({ address: vault.address });
    await expect(
      authenticate(client, inputsFrom({ 'vault-url': vault.address, audience: 'aud' }), {
        getIdToken: vi.fn().mockRejectedValue(new Error('runner said no')),
        env: oidcEnv,
      }),
    ).rejects.toThrow(/failed to obtain a GitHub OIDC token for audience "aud": runner said no/);
  });
});
