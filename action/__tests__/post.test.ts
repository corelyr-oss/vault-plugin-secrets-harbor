import { afterEach, describe, expect, it } from 'vitest';
import { run } from '../src/post.js';
import { FakeVault } from './helpers/fakeVault.js';
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

function state(address: string, overrides: Record<string, string> = {}) {
  return {
    isPost: 'true',
    vaultUrl: address,
    token: 'hvs.fake-oidc-token',
    tokenActionOwned: 'true',
    leaseId: 'harbor/creds/ci-pull/abc123',
    registry: 'harbor.example.com',
    loggedIn: 'true',
    revoke: 'true',
    logout: 'true',
    tlsSkipVerify: 'false',
    ...overrides,
  };
}

describe('post step', () => {
  it('revokes the lease synchronously, revokes its token and logs out', async () => {
    vault = await FakeVault.start();
    runner = startRunner({}, state(vault.address));
    const docker = fakeDocker();

    await run({ runner: docker });

    expect(vault.revokedLeases).toEqual(['harbor/creds/ci-pull/abc123']);
    const revokeRequest = vault.requests.find((r) => r.path === '/v1/sys/leases/revoke');
    expect(revokeRequest?.body).toMatchObject({ sync: true });
    expect(vault.revokedTokens).toEqual(['hvs.fake-oidc-token']);
    expect(docker.calls[0]?.args).toEqual(['logout', 'harbor.example.com']);
    expect(runner.warnings()).toEqual([]);
  });

  it('never revokes a caller-supplied token', async () => {
    vault = await FakeVault.start();
    runner = startRunner(
      {},
      state(vault.address, { tokenActionOwned: 'false', token: 'hvs.caller-token' }),
    );
    await run({ runner: fakeDocker() });
    expect(vault.revokedLeases).toEqual(['harbor/creds/ci-pull/abc123']);
    expect(vault.revokedTokens).toEqual([]);
  });

  it('leaves the lease alone when revoke is false and says so', async () => {
    vault = await FakeVault.start();
    runner = startRunner({}, state(vault.address, { revoke: 'false' }));
    await run({ runner: fakeDocker() });
    expect(vault.revokedLeases).toEqual([]);
    // the action still cleans up its own token
    expect(vault.revokedTokens).toEqual(['hvs.fake-oidc-token']);
  });

  it('skips docker logout when logout is false or it never logged in', async () => {
    vault = await FakeVault.start();
    runner = startRunner({}, state(vault.address, { logout: 'false' }));
    const docker = fakeDocker();
    await run({ runner: docker });
    expect(docker.calls).toHaveLength(0);

    runner.restore();
    vault.requests.length = 0;
    runner = startRunner({}, state(vault.address, { loggedIn: 'false' }));
    const docker2 = fakeDocker();
    await run({ runner: docker2 });
    expect(docker2.calls).toHaveLength(0);
  });

  it('warns but does not fail when Vault is unreachable, naming the lease', async () => {
    runner = startRunner({}, state('http://127.0.0.1:1'));
    await expect(run({ runner: fakeDocker() })).resolves.toBeUndefined();
    const warnings = runner.warnings().join(' ');
    expect(warnings).toMatch(/could not revoke lease harbor\/creds\/ci-pull\/abc123/);
    expect(warnings).toMatch(/vault lease revoke harbor\/creds\/ci-pull\/abc123/);
  });

  it('warns when Vault refuses the revocation', async () => {
    vault = await FakeVault.start({
      failNext: { 'POST /v1/sys/leases/revoke': { status: 403, errors: ['permission denied'] } },
    });
    runner = startRunner({}, state(vault.address));
    await expect(run({ runner: fakeDocker() })).resolves.toBeUndefined();
    expect(runner.warnings().join(' ')).toMatch(/could not revoke lease .*permission denied/);
  });

  it('warns when docker logout fails but still revokes', async () => {
    vault = await FakeVault.start();
    runner = startRunner({}, state(vault.address));
    await run({ runner: fakeDocker({ exitCode: 1, stderr: 'not logged in' }) });
    expect(runner.warnings().join(' ')).toMatch(/could not log out of harbor.example.com/);
    expect(vault.revokedLeases).toEqual(['harbor/creds/ci-pull/abc123']);
  });

  it('cleans up the token when the main step failed before issuing credentials', async () => {
    vault = await FakeVault.start();
    runner = startRunner(
      {},
      {
        isPost: 'true',
        vaultUrl: vault.address,
        token: 'hvs.fake-oidc-token',
        tokenActionOwned: 'true',
        revoke: 'true',
        logout: 'true',
        loggedIn: 'false',
      },
    );
    await run({ runner: fakeDocker() });
    expect(vault.revokedLeases).toEqual([]);
    expect(vault.revokedTokens).toEqual(['hvs.fake-oidc-token']);
  });

  it('does nothing when there is no state at all', async () => {
    runner = startRunner({}, {});
    const docker = fakeDocker();
    await expect(run({ runner: docker })).resolves.toBeUndefined();
    expect(docker.calls).toHaveLength(0);
    expect(runner.warnings()).toEqual([]);
  });
});
