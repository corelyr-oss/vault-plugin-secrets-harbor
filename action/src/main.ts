/** Main step: authenticate, issue credentials, log in, publish outputs. */
import * as core from '@actions/core';
import { authenticate, type IdTokenGetter } from './auth.js';
import { dockerLogin, type CommandRunner } from './docker.js';
import { parseInputs, type InputSource } from './inputs.js';
import { StateKeys, markPostRun, saveBool, saveString } from './state.js';
import { VaultClient } from './vault.js';

/** Seams for tests; production callers pass nothing. */
export interface MainDeps {
  inputSource?: InputSource;
  getIdToken?: IdTokenGetter;
  env?: NodeJS.ProcessEnv;
  runner?: CommandRunner;
}

export async function run(deps: MainDeps = {}): Promise<void> {
  const inputs = deps.inputSource ? parseInputs(deps.inputSource) : parseInputs();

  // Anything the post step needs must be saved before a failure can occur, so
  // that partial work is still cleaned up.
  markPostRun();
  saveString(StateKeys.vaultUrl, inputs.vaultUrl);
  saveString(StateKeys.namespace, inputs.namespace);
  saveString(StateKeys.caCert, inputs.caCert);
  saveBool(StateKeys.tlsSkipVerify, inputs.tlsSkipVerify);
  saveString(StateKeys.registry, inputs.registry);
  saveBool(StateKeys.revoke, inputs.revoke);
  saveBool(StateKeys.logout, inputs.logout);
  saveBool(StateKeys.loggedIn, false);

  if (inputs.tlsSkipVerify) {
    core.warning('tls-skip-verify is enabled: the Vault TLS certificate is not verified');
  }

  const client = new VaultClient({
    address: inputs.vaultUrl,
    namespace: inputs.namespace,
    caCert: inputs.caCert,
    tlsSkipVerify: inputs.tlsSkipVerify,
  });

  const auth = await authenticate(client, inputs, {
    ...(deps.getIdToken ? { getIdToken: deps.getIdToken } : {}),
    ...(deps.env ? { env: deps.env } : {}),
  });
  saveBool(StateKeys.tokenActionOwned, auth.actionOwned);
  saveString(StateKeys.token, client.token());

  core.info(`requesting Harbor credentials from ${inputs.mount}/creds/${inputs.role}`);
  const creds = await client.readCreds(inputs.mount, inputs.role);

  // Mask before anything else can echo these values.
  core.setSecret(creds.secret);
  if (creds.auth !== '') {
    core.setSecret(creds.auth);
  }
  saveString(StateKeys.leaseId, creds.leaseId);

  for (const warning of creds.warnings) {
    core.warning(`vault: ${warning}`);
  }

  core.setOutput('username', creds.username);
  core.setOutput('secret', creds.secret);
  core.setOutput('auth', creds.auth);
  core.setOutput('registry', inputs.registry);
  core.setOutput('robot-id', creds.robotId);
  core.setOutput('expires-at', creds.expiresAt);
  core.setOutput('lease-id', creds.leaseId);

  core.info(
    `issued Harbor robot ${creds.username} (id ${creds.robotId}), ` +
      `harbor expiry ${creds.expiresAt}, lease TTL ${creds.leaseDuration}s`,
  );

  if (!inputs.login) {
    core.info('login is false: skipping docker login');
    return;
  }

  await dockerLogin(inputs.registry, creds.username, creds.secret, deps.runner);
  saveBool(StateKeys.loggedIn, true);
}
