import { describe, expect, it } from 'vitest';
import { InputError, parseInputs, type InputSource } from '../src/inputs.js';

function source(inputs: Record<string, string>, env: Record<string, string> = {}): InputSource {
  return {
    getInput: (name) => inputs[name] ?? '',
    getEnv: (name) => env[name],
  };
}

const minimal = { registry: 'harbor.example.com', role: 'ci-pull', 'vault-url': 'https://vault' };

describe('parseInputs', () => {
  it('applies documented defaults', () => {
    const inputs = parseInputs(source(minimal));
    expect(inputs).toMatchObject({
      registry: 'harbor.example.com',
      role: 'ci-pull',
      vaultUrl: 'https://vault',
      mount: 'harbor',
      authMount: 'jwt',
      authRole: 'ci-pull',
      tlsSkipVerify: false,
      login: true,
      revoke: true,
      logout: true,
    });
    expect(inputs.vaultToken).toBeUndefined();
    expect(inputs.audience).toBeUndefined();
    expect(inputs.namespace).toBeUndefined();
    expect(inputs.caCert).toBeUndefined();
  });

  it('names the missing required input', () => {
    expect(() => parseInputs(source({ role: 'ci' }))).toThrow(/missing required input: registry/);
    expect(() => parseInputs(source({ registry: 'h' }))).toThrow(/missing required input: role/);
  });

  it('falls back to VAULT_ADDR and VAULT_TOKEN', () => {
    const inputs = parseInputs(
      source(
        { registry: 'h', role: 'r' },
        { VAULT_ADDR: 'https://vault.env', VAULT_TOKEN: 'hvs.env' },
      ),
    );
    expect(inputs.vaultUrl).toBe('https://vault.env');
    expect(inputs.vaultToken).toBe('hvs.env');
  });

  it('prefers explicit inputs over the environment', () => {
    const inputs = parseInputs(
      source(
        { ...minimal, 'vault-token': 'hvs.input' },
        { VAULT_ADDR: 'https://vault.env', VAULT_TOKEN: 'hvs.env' },
      ),
    );
    expect(inputs.vaultUrl).toBe('https://vault');
    expect(inputs.vaultToken).toBe('hvs.input');
  });

  it('requires a vault url from somewhere', () => {
    expect(() => parseInputs(source({ registry: 'h', role: 'r' }))).toThrow(
      /missing required input: vault-url .*VAULT_ADDR/,
    );
  });

  it('defaults auth-role to role and honours an override', () => {
    expect(parseInputs(source(minimal)).authRole).toBe('ci-pull');
    expect(parseInputs(source({ ...minimal, 'auth-role': 'gha' })).authRole).toBe('gha');
  });

  it('strips slashes from mount paths', () => {
    const inputs = parseInputs(source({ ...minimal, mount: '/harbor/', 'auth-mount': '/gha/' }));
    expect(inputs.mount).toBe('harbor');
    expect(inputs.authMount).toBe('gha');
  });

  it('parses booleans and rejects nonsense', () => {
    expect(parseInputs(source({ ...minimal, login: 'FALSE' })).login).toBe(false);
    expect(parseInputs(source({ ...minimal, revoke: 'no' })).revoke).toBe(false);
    expect(parseInputs(source({ ...minimal, logout: '0' })).logout).toBe(false);
    expect(parseInputs(source({ ...minimal, 'tls-skip-verify': 'yes' })).tlsSkipVerify).toBe(true);
    expect(() => parseInputs(source({ ...minimal, login: 'maybe' }))).toThrow(
      /input login must be true or false, got "maybe"/,
    );
  });

  it('treats whitespace-only values as unset', () => {
    const inputs = parseInputs(source({ ...minimal, audience: '   ', 'vault-token': '  ' }));
    expect(inputs.audience).toBeUndefined();
    expect(inputs.vaultToken).toBeUndefined();
  });

  it('throws InputError for user-fixable problems', () => {
    expect(() => parseInputs(source({}))).toThrowError(InputError);
  });
});
