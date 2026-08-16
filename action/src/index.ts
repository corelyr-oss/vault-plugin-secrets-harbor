/**
 * Single bundle entry point. action.yml points both `main` and `post` here; the
 * saved state decides which half runs.
 */
import * as core from '@actions/core';
import { run as runMain } from './main.js';
import { run as runPost } from './post.js';
import { isPostRun } from './state.js';

async function bootstrap(): Promise<void> {
  if (isPostRun()) {
    await runPost();
    return;
  }
  await runMain();
}

bootstrap().catch((err: unknown) => {
  core.setFailed(err instanceof Error ? err.message : String(err));
});
