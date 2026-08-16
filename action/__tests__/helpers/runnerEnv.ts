/**
 * Drives the real @actions/core against a simulated runner: `INPUT_` and
 * `STATE_` environment variables, GITHUB_OUTPUT/GITHUB_STATE files, and
 * captured stdout workflow commands. Testing through the real mechanism means
 * masking and output plumbing are exercised, not mocked away.
 */
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export interface FakeRunner {
  /** Workflow commands written to stdout, e.g. "::add-mask::secret". */
  commands: string[];
  masks(): string[];
  outputs(): Record<string, string>;
  savedState(): Record<string, string>;
  warnings(): string[];
  restore(): void;
}

export function startRunner(
  inputs: Record<string, string> = {},
  state: Record<string, string> = {},
  extraEnv: Record<string, string> = {},
): FakeRunner {
  const dir = mkdtempSync(join(tmpdir(), 'harbor-action-test-'));
  const outputFile = join(dir, 'output');
  const stateFile = join(dir, 'state');
  writeFileSync(outputFile, '');
  writeFileSync(stateFile, '');

  const savedEnv = { ...process.env };
  // Start from a clean slate: a developer's own VAULT_ADDR/VAULT_TOKEN must not
  // leak into a test run, or the action would talk to their real Vault.
  for (const key of Object.keys(process.env)) {
    if (
      key.startsWith('INPUT_') ||
      key.startsWith('STATE_') ||
      key.startsWith('VAULT_') ||
      key.startsWith('ACTIONS_ID_TOKEN_')
    ) {
      delete process.env[key];
    }
  }
  for (const [name, value] of Object.entries(inputs)) {
    process.env[`INPUT_${name.replace(/ /g, '_').toUpperCase()}`] = value;
  }
  for (const [name, value] of Object.entries(state)) {
    process.env[`STATE_${name}`] = value;
  }
  process.env.GITHUB_OUTPUT = outputFile;
  process.env.GITHUB_STATE = stateFile;
  Object.assign(process.env, extraEnv);

  const commands: string[] = [];
  const originalWrite = process.stdout.write.bind(process.stdout);
  process.stdout.write = ((chunk: string | Uint8Array, ...rest: unknown[]): boolean => {
    const text = typeof chunk === 'string' ? chunk : Buffer.from(chunk).toString();
    for (const line of text.split('\n')) {
      if (line.startsWith('::')) {
        commands.push(line.trim());
      }
    }
    void rest;
    return true;
  }) as typeof process.stdout.write;

  const parseFile = (file: string): Record<string, string> => {
    // Heredoc format: NAME<<DELIM\nvalue\nDELIM
    const content = readFileSync(file, 'utf8');
    const result: Record<string, string> = {};
    const lines = content.split('\n');
    for (let i = 0; i < lines.length; i++) {
      const match = /^(.+?)<<(ghadelimiter_[0-9a-f-]+)$/.exec(lines[i] ?? '');
      if (!match) continue;
      const [, name, delim] = match;
      const value: string[] = [];
      i++;
      while (i < lines.length && lines[i] !== delim) {
        value.push(lines[i] ?? '');
        i++;
      }
      result[name ?? ''] = value.join('\n');
    }
    return result;
  };

  return {
    commands,
    masks: () =>
      commands
        .filter((c) => c.startsWith('::add-mask::'))
        .map((c) => c.slice('::add-mask::'.length)),
    warnings: () => commands.filter((c) => c.startsWith('::warning::')).map((c) => c.slice(11)),
    outputs: () => parseFile(outputFile),
    savedState: () => parseFile(stateFile),
    restore: () => {
      process.stdout.write = originalWrite;
      for (const key of Object.keys(process.env)) {
        if (!(key in savedEnv)) delete process.env[key];
      }
      Object.assign(process.env, savedEnv);
    },
  };
}
