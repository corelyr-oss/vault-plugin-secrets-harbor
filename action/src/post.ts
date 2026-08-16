/**
 * Post step: revoke the credential lease (deleting the Harbor robot), revoke a
 * token this action minted, and log the Docker CLI out.
 *
 * Cleanup problems are warnings: they must not turn a green job red, and the
 * credential is bounded by both the Vault lease TTL and the robot's
 * Harbor-side expiry.
 */
import * as core from '@actions/core';
import { dockerLogout, type CommandRunner } from './docker.js';
import { readPostState } from './state.js';
import { VaultClient } from './vault.js';

/** Seams for tests; production callers pass nothing. */
export interface PostDeps {
  runner?: CommandRunner;
}

export async function run(deps: PostDeps = {}): Promise<void> {
  const state = readPostState();

  if (state.registry !== undefined && state.loggedIn && state.logout) {
    try {
      await dockerLogout(state.registry, deps.runner);
    } catch (err) {
      core.warning(`could not log out of ${state.registry}: ${message(err)}`);
    }
  }

  if (state.token === undefined || state.vaultUrl === '') {
    return;
  }

  const client = new VaultClient({
    address: state.vaultUrl,
    token: state.token,
    namespace: state.namespace,
    caCert: state.caCert,
    tlsSkipVerify: state.tlsSkipVerify,
  });

  if (state.leaseId !== undefined) {
    if (!state.revoke) {
      core.info(
        `revoke is false: leaving lease ${state.leaseId} in place. The Harbor robot stays ` +
          'usable until the lease TTL expires.',
      );
    } else {
      try {
        await client.revokeLease(state.leaseId, true);
        core.info(`revoked lease ${state.leaseId}; the Harbor robot has been deleted`);
      } catch (err) {
        core.warning(
          `could not revoke lease ${state.leaseId}: ${message(err)}. The Harbor robot stays ` +
            'usable until the lease TTL expires; revoke it manually with ' +
            `"vault lease revoke ${state.leaseId}".`,
        );
      }
    }
  }

  if (state.tokenActionOwned) {
    try {
      await client.revokeSelfToken();
      core.debug('revoked the Vault token obtained by this action');
    } catch (err) {
      core.warning(`could not revoke the Vault token obtained by this action: ${message(err)}`);
    }
  }
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
