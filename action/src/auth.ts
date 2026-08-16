/** Resolves a Vault token: explicit input, ambient env, or GitHub OIDC. */
import * as core from '@actions/core';
import type { ActionInputs } from './inputs.js';
import { InputError } from './inputs.js';
import type { VaultClient } from './vault.js';
import { VaultError } from './vault.js';

export interface AuthResult {
  /** True when this action minted the token and is therefore responsible for revoking it. */
  actionOwned: boolean;
}

/** Obtains a GitHub OIDC token. Injectable for tests. */
export type IdTokenGetter = (audience?: string) => Promise<string>;

const defaultIdTokenGetter: IdTokenGetter = (audience) => core.getIDToken(audience);

export function oidcAvailable(env: NodeJS.ProcessEnv = process.env): boolean {
  return (
    (env.ACTIONS_ID_TOKEN_REQUEST_URL ?? '') !== '' &&
    (env.ACTIONS_ID_TOKEN_REQUEST_TOKEN ?? '') !== ''
  );
}

export async function authenticate(
  client: VaultClient,
  inputs: ActionInputs,
  options: { getIdToken?: IdTokenGetter; env?: NodeJS.ProcessEnv } = {},
): Promise<AuthResult> {
  if (inputs.vaultToken !== undefined) {
    client.setToken(inputs.vaultToken);
    core.debug('using the supplied Vault token; OIDC is not used');
    return { actionOwned: false };
  }

  const env = options.env ?? process.env;
  if (!oidcAvailable(env)) {
    throw new InputError(
      'no Vault token was supplied and no GitHub OIDC token is available. ' +
        'Add "permissions: id-token: write" to the job, or pass the vault-token input ' +
        '(or set VAULT_TOKEN).',
    );
  }

  const getIdToken = options.getIdToken ?? defaultIdTokenGetter;
  let jwt: string;
  try {
    jwt = await getIdToken(inputs.audience);
  } catch (err) {
    throw new Error(
      `failed to obtain a GitHub OIDC token${
        inputs.audience === undefined ? '' : ` for audience "${inputs.audience}"`
      }: ${err instanceof Error ? err.message : String(err)}`,
    );
  }

  core.info(
    `authenticating to Vault: auth mount "${inputs.authMount}", role "${inputs.authRole}"` +
      `${inputs.audience === undefined ? ' (default audience)' : `, audience "${inputs.audience}"`}`,
  );

  try {
    const auth = await client.loginJwt(inputs.authMount, inputs.authRole, jwt);
    core.setSecret(auth.clientToken);
    client.setToken(auth.clientToken);
    return { actionOwned: true };
  } catch (err) {
    throw new Error(describeLoginFailure(err, inputs));
  }
}

function describeLoginFailure(err: unknown, inputs: ActionInputs): string {
  const audience =
    inputs.audience === undefined ? 'the repository default audience' : `"${inputs.audience}"`;
  if (err instanceof VaultError && err.status === 404) {
    return (
      `Vault has no auth method mounted at "${inputs.authMount}" (${err.message}). ` +
      'Check the auth-mount input.'
    );
  }
  if (err instanceof VaultError) {
    return (
      `${err.message}. Check that the JWT role "${inputs.authRole}" exists on auth mount ` +
      `"${inputs.authMount}", that its bound_audiences include ${audience}, and that its ` +
      'bound_subject/bound_claims match this workflow.'
    );
  }
  return err instanceof Error ? err.message : String(err);
}
