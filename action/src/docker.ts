/**
 * Docker CLI wrapper. Shelling out to the CLI (rather than writing
 * ~/.docker/config.json directly) keeps credential stores and helpers working.
 */
import * as core from '@actions/core';
import * as exec from '@actions/exec';

export interface CommandRunner {
  which(tool: string): Promise<string>;
  exec(
    tool: string,
    args: string[],
    options: { input?: Buffer; silent?: boolean; ignoreReturnCode?: boolean },
  ): Promise<{ exitCode: number; stdout: string; stderr: string }>;
}

export const actionsRunner: CommandRunner = {
  which: (tool) => io_which(tool),
  exec: async (tool, args, options) => {
    let stdout = '';
    let stderr = '';
    const exitCode = await exec.exec(tool, args, {
      silent: options.silent ?? true,
      ignoreReturnCode: options.ignoreReturnCode ?? true,
      ...(options.input ? { input: options.input } : {}),
      listeners: {
        stdout: (data: Buffer) => (stdout += data.toString()),
        stderr: (data: Buffer) => (stderr += data.toString()),
      },
    });
    return { exitCode, stdout, stderr };
  },
};

/** Resolves a tool on PATH; throws when absent. */
async function io_which(tool: string): Promise<string> {
  const { exitCode, stdout } = await actionsExecQuiet(tool);
  if (exitCode !== 0) {
    throw new Error(`${tool} was not found on PATH`);
  }
  return stdout.trim();
}

async function actionsExecQuiet(tool: string): Promise<{ exitCode: number; stdout: string }> {
  let stdout = '';
  const exitCode = await exec.exec(process.platform === 'win32' ? 'where' : 'which', [tool], {
    silent: true,
    ignoreReturnCode: true,
    listeners: { stdout: (data: Buffer) => (stdout += data.toString()) },
  });
  return { exitCode, stdout };
}

export async function dockerLogin(
  registry: string,
  username: string,
  secret: string,
  runner: CommandRunner | undefined = actionsRunner,
): Promise<void> {
  const run = runner ?? actionsRunner;
  try {
    await run.which('docker');
  } catch {
    throw new Error(
      'docker login was requested but no Docker CLI is available on this runner. ' +
        'Install Docker, or set "login: false" and use the action outputs instead.',
    );
  }

  const { exitCode, stderr, stdout } = await run.exec(
    'docker',
    ['login', '--username', username, '--password-stdin', registry],
    { input: Buffer.from(secret), silent: true },
  );
  if (exitCode !== 0) {
    throw new Error(
      `docker login to ${registry} failed: ${firstLine(stderr) || firstLine(stdout)}`,
    );
  }
  core.info(`logged in to ${registry} as ${username}`);
}

export async function dockerLogout(
  registry: string,
  runner: CommandRunner | undefined = actionsRunner,
): Promise<void> {
  const run = runner ?? actionsRunner;
  const { exitCode, stderr, stdout } = await run.exec('docker', ['logout', registry], {
    silent: true,
  });
  if (exitCode !== 0) {
    throw new Error(
      `docker logout from ${registry} failed: ${firstLine(stderr) || firstLine(stdout)}`,
    );
  }
  core.info(`logged out of ${registry}`);
}

function firstLine(value: string): string {
  return (
    value
      .split('\n')
      .find((line) => line.trim() !== '')
      ?.trim() ?? ''
  );
}
