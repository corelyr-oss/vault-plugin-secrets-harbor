/**
 * Test-only action. No dependencies: it talks to Harbor with global fetch.
 *
 * main: records that a post run is expected.
 * post: reads $RUNNER_TEMP/e2e-expect.json (written by the workflow after the
 *       harbor-login action ran) and asserts that the robot has been deleted
 *       and that its credential can no longer pull.
 */
import { appendFileSync, readFileSync } from 'node:fs';

const EXPECT_FILE = `${process.env.RUNNER_TEMP ?? '/tmp'}/e2e-expect.json`;

function log(message) {
  process.stdout.write(`${message}\n`);
}

function fail(message) {
  process.stdout.write(`::error::${message}\n`);
  process.exitCode = 1;
}

function basic(user, pass) {
  return `Basic ${Buffer.from(`${user}:${pass}`).toString('base64')}`;
}

async function main() {
  appendFileSync(process.env.GITHUB_STATE, 'isPost=true\n');
  log('assert-cleanup armed; the check runs in this action’s post step');
}

async function post() {
  let expect;
  try {
    expect = JSON.parse(readFileSync(EXPECT_FILE, 'utf8'));
  } catch {
    // The workflow writes this file only after the login action succeeded. Its
    // absence means the job failed before that, and the real error is already
    // in the log; adding a second one here would only obscure it.
    log(`assert-cleanup: no ${EXPECT_FILE}; nothing was issued, so there is nothing to assert`);
    return;
  }
  const { harborUrl, adminUser, adminPassword, projectId, username, secret, repository, tag } =
    expect;

  // 1. The robot must no longer exist in the project.
  const listUrl =
    `${harborUrl}/api/v2.0/robots?page_size=100&` +
    `q=${encodeURIComponent(`Level=project,ProjectID=${projectId}`)}`;
  const listResponse = await fetch(listUrl, {
    headers: { authorization: basic(adminUser, adminPassword) },
  });
  if (!listResponse.ok) {
    fail(`assert-cleanup: listing robots failed with HTTP ${listResponse.status}`);
    return;
  }
  const robots = await listResponse.json();
  const survivors = robots.filter((robot) => robot.name === username);
  if (survivors.length > 0) {
    fail(
      `assert-cleanup: robot ${username} still exists in Harbor after the job; ` +
        'the post step did not revoke the lease',
    );
    return;
  }
  log(`assert-cleanup: robot ${username} is gone from Harbor`);

  // 2. The credential must no longer be able to pull.
  const scope = `repository:${repository}:pull`;
  const tokenResponse = await fetch(
    `${harborUrl}/service/token?service=harbor-registry&scope=${encodeURIComponent(scope)}`,
    { headers: { authorization: basic(username, secret) } },
  );
  // Harbor answers 200 with a token that carries no access for a dead
  // credential, so the manifest fetch is the real check.
  let manifestStatus = tokenResponse.status;
  if (tokenResponse.ok) {
    const { token } = await tokenResponse.json();
    const manifest = await fetch(`${harborUrl}/v2/${repository}/manifests/${tag}`, {
      headers: {
        authorization: `Bearer ${token}`,
        accept: 'application/vnd.oci.image.manifest.v1+json',
      },
    });
    manifestStatus = manifest.status;
  }
  if (manifestStatus === 200) {
    fail(
      `assert-cleanup: the revoked credential can still pull ${repository}:${tag} ` +
        '(manifest fetch returned 200)',
    );
    return;
  }
  log(`assert-cleanup: revoked credential can no longer pull (HTTP ${manifestStatus})`);
}

const isPost = process.env.STATE_isPost === 'true';
await (isPost ? post() : main()).catch((err) => {
  fail(`assert-cleanup: ${err instanceof Error ? err.message : String(err)}`);
});
