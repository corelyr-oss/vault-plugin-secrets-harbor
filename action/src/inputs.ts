/** Input parsing and validation for the action. */
import * as core from '@actions/core';

export interface ActionInputs {
  registry: string;
  role: string;
  vaultUrl: string;
  mount: string;
  vaultToken: string | undefined;
  authMount: string;
  authRole: string;
  audience: string | undefined;
  namespace: string | undefined;
  caCert: string | undefined;
  tlsSkipVerify: boolean;
  login: boolean;
  revoke: boolean;
  logout: boolean;
}

/** Raw accessors, injectable so tests do not need the runner's env layout. */
export interface InputSource {
  getInput(name: string): string;
  getEnv(name: string): string | undefined;
}

export const actionsInputSource: InputSource = {
  getInput: (name) => core.getInput(name),
  getEnv: (name) => process.env[name],
};

/** Thrown for user-fixable configuration problems. */
export class InputError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'InputError';
  }
}

export function parseInputs(source: InputSource = actionsInputSource): ActionInputs {
  const registry = required(source, 'registry');
  const role = required(source, 'role');

  const vaultUrl = trim(source.getInput('vault-url')) || trim(source.getEnv('VAULT_ADDR') ?? '');
  if (vaultUrl === '') {
    throw new InputError(
      'missing required input: vault-url (or set the VAULT_ADDR environment variable)',
    );
  }

  const mount = stripSlashes(source.getInput('mount')) || 'harbor';
  const authMount = stripSlashes(source.getInput('auth-mount')) || 'jwt';
  const vaultToken =
    trim(source.getInput('vault-token')) || trim(source.getEnv('VAULT_TOKEN') ?? '');
  const authRole = trim(source.getInput('auth-role')) || role;

  return {
    registry,
    role,
    vaultUrl,
    mount,
    vaultToken: vaultToken === '' ? undefined : vaultToken,
    authMount,
    authRole,
    audience: optional(source, 'audience'),
    namespace: optional(source, 'namespace'),
    caCert: optional(source, 'ca-cert'),
    tlsSkipVerify: bool(source, 'tls-skip-verify', false),
    login: bool(source, 'login', true),
    revoke: bool(source, 'revoke', true),
    logout: bool(source, 'logout', true),
  };
}

function required(source: InputSource, name: string): string {
  const value = trim(source.getInput(name));
  if (value === '') {
    throw new InputError(`missing required input: ${name}`);
  }
  return value;
}

function optional(source: InputSource, name: string): string | undefined {
  const value = trim(source.getInput(name));
  return value === '' ? undefined : value;
}

function bool(source: InputSource, name: string, fallback: boolean): boolean {
  const value = trim(source.getInput(name)).toLowerCase();
  if (value === '') {
    return fallback;
  }
  if (['true', 'yes', '1'].includes(value)) {
    return true;
  }
  if (['false', 'no', '0'].includes(value)) {
    return false;
  }
  throw new InputError(`input ${name} must be true or false, got "${value}"`);
}

function trim(value: string): string {
  return value.trim();
}

function stripSlashes(value: string): string {
  return trim(value).replace(/^\/+|\/+$/g, '');
}
