/** Fake Docker CLI for exercising the login/logout paths without Docker. */
import type { CommandRunner } from '../../src/docker.js';

export interface FakeDocker extends CommandRunner {
  calls: { args: string[]; input?: string | undefined }[];
}

export function fakeDocker(
  opts: { present?: boolean; exitCode?: number; stderr?: string } = {},
): FakeDocker {
  const calls: { args: string[]; input?: string | undefined }[] = [];
  return {
    calls,
    which: (tool: string) => {
      if (opts.present === false) {
        return Promise.reject(new Error(`${tool} was not found on PATH`));
      }
      return Promise.resolve(`/usr/bin/${tool}`);
    },
    exec: (_tool, args, options) => {
      calls.push({ args, input: options.input?.toString() });
      return Promise.resolve({
        exitCode: opts.exitCode ?? 0,
        stdout: '',
        stderr: opts.stderr ?? '',
      });
    },
  };
}
