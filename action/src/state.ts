/** State handed from the main step to the post step. */
import * as core from '@actions/core';

export const StateKeys = {
  isPost: 'isPost',
  vaultUrl: 'vaultUrl',
  namespace: 'namespace',
  caCert: 'caCert',
  tlsSkipVerify: 'tlsSkipVerify',
  token: 'token',
  tokenActionOwned: 'tokenActionOwned',
  leaseId: 'leaseId',
  registry: 'registry',
  loggedIn: 'loggedIn',
  revoke: 'revoke',
  logout: 'logout',
} as const;

export interface PostState {
  vaultUrl: string;
  namespace: string | undefined;
  caCert: string | undefined;
  tlsSkipVerify: boolean;
  token: string | undefined;
  tokenActionOwned: boolean;
  leaseId: string | undefined;
  registry: string | undefined;
  loggedIn: boolean;
  revoke: boolean;
  logout: boolean;
}

export function isPostRun(): boolean {
  return core.getState(StateKeys.isPost) === 'true';
}

export function markPostRun(): void {
  core.saveState(StateKeys.isPost, 'true');
}

export function saveString(key: string, value: string | undefined): void {
  if (value !== undefined && value !== '') {
    core.saveState(key, value);
  }
}

export function saveBool(key: string, value: boolean): void {
  core.saveState(key, value ? 'true' : 'false');
}

export function readPostState(): PostState {
  const str = (key: string): string | undefined => {
    const value = core.getState(key);
    return value === '' ? undefined : value;
  };
  return {
    vaultUrl: core.getState(StateKeys.vaultUrl),
    namespace: str(StateKeys.namespace),
    caCert: str(StateKeys.caCert),
    tlsSkipVerify: core.getState(StateKeys.tlsSkipVerify) === 'true',
    token: str(StateKeys.token),
    tokenActionOwned: core.getState(StateKeys.tokenActionOwned) === 'true',
    leaseId: str(StateKeys.leaseId),
    registry: str(StateKeys.registry),
    loggedIn: core.getState(StateKeys.loggedIn) === 'true',
    revoke: core.getState(StateKeys.revoke) === 'true',
    logout: core.getState(StateKeys.logout) === 'true',
  };
}
