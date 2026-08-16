import { afterEach, describe, expect, it, vi } from 'vitest';
import { run } from '../src/main.js';
import { actionsInputSource } from '../src/inputs.js';
import { FakeVault, defaultCreds } from './helpers/fakeVault.js';
import { fakeDocker } from './helpers/fakeDocker.js';
import { startRunner, type FakeRunner } from './helpers/runnerEnv.js';

let vault: FakeVault | undefined;
let runner: FakeRunner | undefined;

afterEach(async () => {
  runner?.restore();
  runner = undefined;
  await vault?.stop();
  vault = undefined;
});

const oidcEnv = {
  ACTIONS_ID_TOKEN_REQUEST_URL: 'https://token.actions.githubusercontent.com',
  ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'runner-token',
} as NodeJS.ProcessEnv;

function baseInputs(address: string, extra: Record<string, string> = {}) {
  return { registry: 'harbor.example.com', role: 'ci-pull', 'vault-url': address, ...extra };
}

describe('main step', () => {
  it('issues credentials, masks them, sets outputs and logs in', async () => {
    vault = await FakeVault.start();
    runner = startRunner(baseInputs(vault.address));
    const docker = fakeDocker();

    await run({
      inputSource: actionsInputSource,
      getIdToken: vi.fn().mockResolvedValue('jwt'),
      env: oidcEnv,
      runner: docker,
    });

    const creds = defaultCreds();
    expect(runner.outputs()).toEqual({
      username: creds.username,
      secret: creds.secret,
      auth: creds.auth,
      registry: 'harbor.example.com',
      'robot-id': '42',
      'expires-at': '2026-08-17T21:43:40Z',
      'lease-id': 'harbor/creds/ci-pull/abc123',
    });
    // secret, auth and the Vault token are all masked
    expect(runner.masks()).toEqual(
      expect.arrayContaining([creds.secret as string, creds.auth as string, 'hvs.fake-oidc-token']),
    );
    expect(docker.calls).toHaveLength(1);
    expect(docker.calls[0]?.args).toEqual([
      'login',
      '--username',
      creds.username,
      '--password-stdin',
      'harbor.example.com',
    ]);
    // the secret goes over stdin, never as an argument
    expect(docker.calls[0]?.input).toBe(creds.secret);
    expect(docker.calls[0]?.args.join(' ')).not.toContain(creds.secret as string);
  });

  it('saves the state the post step needs', async () => {
    vault = await FakeVault.start();
    runner = startRunner(baseInputs(vault.address, { namespace: 'team-a' }));
    await run({
      inputSource: actionsInputSource,
      getIdToken: vi.fn().mockResolvedValue('jwt'),
      env: oidcEnv,
      runner: fakeDocker(),
    });
    expect(runner.savedState()).toMatchObject({
      isPost: 'true',
      vaultUrl: vault.address,
      namespace: 'team-a',
      registry: 'harbor.example.com',
      leaseId: 'harbor/creds/ci-pull/abc123',
      token: 'hvs.fake-oidc-token',
      tokenActionOwned: 'true',
      loggedIn: 'true',
      revoke: 'true',
      logout: 'true',
    });
  });

  it('marks a caller-supplied token as not action-owned', async () => {
    vault = await FakeVault.start();
    runner = startRunner(baseInputs(vault.address, { 'vault-token': 'hvs.caller-token' }));
    await run({ inputSource: actionsInputSource, env: oidcEnv, runner: fakeDocker() });
    expect(runner.savedState()).toMatchObject({
      tokenActionOwned: 'false',
      token: 'hvs.caller-token',
    });
  });

  it('skips docker login when login is false but still sets outputs', async () => {
    vault = await FakeVault.start();
    runner = startRunner(baseInputs(vault.address, { login: 'false' }));
    const docker = fakeDocker();
    await run({
      inputSource: actionsInputSource,
      getIdToken: vi.fn().mockResolvedValue('jwt'),
      env: oidcEnv,
      runner: docker,
    });
    expect(docker.calls).toHaveLength(0);
    expect(runner.outputs().username).toBe(defaultCreds().username);
    expect(runner.savedState().loggedIn).toBe('false');
  });

  it('still records state when issuance fails, so the post step can clean up', async () => {
    vault = await FakeVault.start({
      failNext: {
        'GET /v1/harbor/creds/ci-pull': { status: 500, errors: ['vault is sealed'] },
      },
    });
    runner = startRunner(baseInputs(vault.address));
    await expect(
      run({
        inputSource: actionsInputSource,
        getIdToken: vi.fn().mockResolvedValue('jwt'),
        env: oidcEnv,
        runner: fakeDocker(),
      }),
    ).rejects.toThrow(/vault is sealed/);
    // the OIDC token was minted before the failure and must still be revoked
    expect(runner.savedState()).toMatchObject({
      isPost: 'true',
      token: 'hvs.fake-oidc-token',
      tokenActionOwned: 'true',
    });
    expect(runner.savedState().leaseId).toBeUndefined();
  });

  it('surfaces a Harbor scope error verbatim', async () => {
    vault = await FakeVault.start({
      failNext: {
        'GET /v1/harbor/creds/ci-pull': {
          status: 400,
          errors: [
            'harbor rejected robot creation: harbor: POST /api/v2.0/robots: 403 DENIED: denied',
          ],
        },
      },
    });
    runner = startRunner(baseInputs(vault.address));
    await expect(
      run({
        inputSource: actionsInputSource,
        getIdToken: vi.fn().mockResolvedValue('jwt'),
        env: oidcEnv,
        runner: fakeDocker(),
      }),
    ).rejects.toThrow(/harbor rejected robot creation.*403 DENIED/);
  });

  it('fails with guidance when no Docker CLI is present', async () => {
    vault = await FakeVault.start();
    runner = startRunner(baseInputs(vault.address));
    await expect(
      run({
        inputSource: actionsInputSource,
        getIdToken: vi.fn().mockResolvedValue('jwt'),
        env: oidcEnv,
        runner: fakeDocker({ present: false }),
      }),
    ).rejects.toThrow(/no Docker CLI is available.*login: false/s);
  });

  it('reports a docker login failure with docker’s message', async () => {
    vault = await FakeVault.start();
    runner = startRunner(baseInputs(vault.address));
    await expect(
      run({
        inputSource: actionsInputSource,
        getIdToken: vi.fn().mockResolvedValue('jwt'),
        env: oidcEnv,
        runner: fakeDocker({ exitCode: 1, stderr: 'unauthorized: authentication required' }),
      }),
    ).rejects.toThrow(/docker login to harbor.example.com failed: unauthorized/);
  });

  it('warns when TLS verification is disabled', async () => {
    vault = await FakeVault.start();
    runner = startRunner(baseInputs(vault.address, { 'tls-skip-verify': 'true' }));
    await run({
      inputSource: actionsInputSource,
      getIdToken: vi.fn().mockResolvedValue('jwt'),
      env: oidcEnv,
      runner: fakeDocker(),
    });
    expect(runner.warnings().join(' ')).toMatch(/tls-skip-verify is enabled/);
  });
});
