# Auto-update plan

Goal: dimuda commits image bumps to the stacks git repo by itself when an update needs no
supervision, and holds the rest with a notification. One admin, minimal manual checks,
every change revertable with `git revert`.

## Decisions

- **Pin format:** exact tag plus digest, `qmcgaw/gluetun:v3.41.3@sha256:…`. The version is
  readable in the line; "stay within the major" is a dimuda policy, not a floating tag.
  A digest that moves without a tag change (re-pushed tag) is reported, never applied.
- **Window:** non-security updates are committed only between 03:00 and 05:00 local time.
  Security fixes ignore the window.
- **LLM down or invalid answer:** patch and rebuild updates keep flowing; minors are held.
- **Majors:** never applied automatically.

## Policy

| Verdict | Action |
|---|---|
| config change needed or breaking | hold, high-priority ntfy with summary and affected settings |
| relevant new feature | hold, ntfy |
| security fix only | apply immediately, skipping age and window |
| nothing notable | apply once seen for `MIN_RELEASE_AGE` (72h), inside the window |
| no verdict (LLM unavailable) | patch/rebuild as above, minors held |
| major | hold, ntfy |

Security is detected by keywords (CVE, GHSA, "security") even without the LLM.
Per-image `autoUpdate: none | patch | minor` in `overrides.yaml` caps what may be applied
(e.g. meilisearch: none, its minors change the index format).

A bump that was applied and later reverted in git is held, not re-applied, and notified once.

## LLM review (OpenAI-compatible endpoint, e.g. TiinyAI)

Input: release notes between running and target version, the stack's `compose.yaml` and other
config files in the stack folder, `.env.example`, and an optional per-image `migrationDoc` URL.
Never `.env` or `.env.age`.

Output (JSON, temperature 0): `config_change_needed`, `breaking`, `security`,
`relevant_features[]`, `summary`, `affected_settings[]`. Cached per image and version jump.

The LLM can only hold or accelerate a patch/minor; it never chooses the target or approves a
major, which bounds the effect of prompt injection through release notes.

## Deploy

After a push, dimuda calls the Komodo API (`BatchDeployStackIfChanged`) on the internal
network, so deploys follow commits within minutes without exposing Komodo to the internet.

## Phases

1. **Auto-update core:** raw image strings from compose, exact-tag+digest pins, `LatestInMajor`,
   `updater` (plan + text edit), git commit/push, Komodo trigger, state (first seen, applied,
   held), ntfy summaries, dashboard badges, `autoUpdate` overrides, false-major fixes
   (calibre-web, lidarr), tests, README.
2. **LLM review:** reviewer client, security fast lane, feature/config holds, eval that replays
   past releases with known config changes (traefik minors) to measure verdict quality.
3. **ntfy Approve / Skip actions:** signed one-time links, so the dashboard needs no login.
