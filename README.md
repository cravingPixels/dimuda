# dimuda

**D**ocker **IM**age **U**pdate **DA**shboard.

A self-hosted dashboard that answers, for every image in your Docker Compose stacks:

- **Is there an update, and is it the real app or just a repackaging?** LinuxServer.io (`4.0.15-ls290`) and hotio (`release-4.0.20.3014`) tags are split into upstream version and build number. An lsio rebuild shows as `rebuild`; a new Sonarr shows as `patch`/`minor`/`major`.
- **What changed?** Upstream GitHub release notes between what you run and what is available, inline.
- **Which stacks use it?** Every image lists the stacks and services that reference it (`redis` → immich, linkwarden, nextcloud).
- **What does `latest` actually contain?** Floating tags (`latest`, `stable`, `8-alpine`, `v3`) are resolved by digest to the concrete version they point to, so you know what to pin to.
- **Has upstream released something the image publisher hasn't packaged yet?** Flagged as "not packaged yet".
- **Optionally, apply the safe ones itself.** With `AUTO_UPDATE=true`, dimuda pins every image to an exact tag plus digest and commits patch and rebuild updates to your git repo, then asks Komodo to deploy them. Everything else waits for you. See [Auto-update](#auto-update).

It reads compose files from mounted folders (Dockge, Komodo) and/or git repos, matches them with running containers through a read-only docker-socket-proxy, and queries registries anonymously (Docker Hub, ghcr.io, lscr.io, quay.io, any OCI registry). Optional ntfy alerts fire only for upstream releases, never for lsio rebuilds.

## Run it

1. Copy `compose.yaml` and `.env.example` into a stack folder, rename `.env.example` to `.env` and fill it in.
2. Make sure `docker-socket-proxy` allows `CONTAINERS=1` and `IMAGES=1`.
   Create a host user and group for dimuda (`PUID`/`PGID`) and a group (`STACKS_READ_GID`) with a recursive, inherited read ACL on the mounted stacks folders. dimuda starts as root only to create and chown `${APP_DIR}/data`, then permanently switches to `PUID:PGID` with `STACKS_READ_GID` as a supplementary group, the way LinuxServer.io images handle `PUID`/`PGID`.
3. The image is private on ghcr.io: run `docker login ghcr.io` on the host once (PAT with `read:packages`), or build it locally with `docker build -t ghcr.io/cravingpixels/dimuda:latest .`.
4. `docker compose up -d`, then open `https://dimuda.<your domain>`.

The first scan takes about a minute for ~60 images. After that it rescans every `SCAN_INTERVAL` (default `6h`), or on demand with **Scan now**.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `COMPOSE_DIRS` | – | Comma-separated `name=/path` folders to scan for `compose.yaml`/`docker-compose.yml`. |
| `GIT_REPOS` | – | Comma-separated `name=https://…` repos to clone and scan. At least one of the two is required. |
| `DOCKER_HOST` | – | `tcp://dockerproxy:2375` or `unix:///var/run/docker.sock`. Without it, running versions are unknown. |
| `GITHUB_TOKEN` | – | Clones and pushes `GIT_REPOS` (sent as a header, never stored in `.git/config`) and raises the GitHub API limit for release notes from 60 to 5000 requests/hour. One fine-grained token covers both: it reads public releases whatever repo it is scoped to. |
| `SCAN_INTERVAL` | `6h` | Minimum `5m`. |
| `PUID`, `PGID` | `65532` | User the app switches to after preparing `/data`. `0` is rejected. |
| `STACKS_READ_GID` | – | Comma-separated supplementary GIDs, e.g. the group that can read your stacks folders. |
| `OVERRIDES_FILE` | `/app/overrides.yaml` | Per-image overrides bundled in the image, see below. |
| `NTFY_URL`, `NTFY_TOPIC`, `NTFY_TOKEN` | – | ntfy alerts for new upstream releases. The first scan after a fresh install only records state, so it does not alert on everything at once. |
| `PUBLIC_URL` | – | Link opened when tapping an ntfy notification. |
| `DATA_DIR` | `/data` | State (last report, what was already notified) and git clones. |
| `LISTEN_ADDR` | `:8080` | |
| `AUTO_UPDATE` | `false` | Commit allowed updates to the `GIT_REPOS` sources. Needs `GITHUB_TOKEN` with Contents: write. |
| `MIN_RELEASE_AGE` | `72h` | How long an update must have been offered before it is applied. Security fixes skip it. |
| `UPDATE_WINDOW` | `03:00-05:00` | Daily local-time range (`TZ`) in which updates are committed. Security fixes skip it. |
| `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL` | `dimuda`, `dimuda@localhost` | Author of the update commits. |
| `KOMODO_URL`, `KOMODO_API_KEY`, `KOMODO_API_SECRET` | – | After a push, run Komodo's `BatchDeployStackIfChanged` so the change is deployed right away. |
| `KOMODO_STACKS` | `*` | Which Komodo stacks that call may deploy (name, wildcard or regex). |

A stack that must be deployed by a Komodo procedure instead, e.g. gluetun when other stacks share
its network, says so with a label on any of its services:

```yaml
labels:
  dimuda.komodo.procedure: gluetun-redeploy
```

dimuda runs that procedure, waits for it to finish, then runs the batch deploy. The label is
read on every scan, so changing it needs no restart.

`GITHUB_TOKEN` is checked on every scan: ntfy warns 30, 7 and 1 day before it expires, and right
away if GitHub rejects it.

## Overrides

`overrides.yaml` (bundled in the image) holds what registries don't expose. To add an image, edit it in this repo:

```yaml
images:
  lscr.io/linuxserver/sonarr:
    changelog: Sonarr/Sonarr          # upstream repo; the image label points at linuxserver/docker-sonarr
  lscr.io/linuxserver/qbittorrent:
    tagRegex: '^(?P<version>\d+\.\d+\.\d+)_v[\d.]+-ls(?P<build>\d+)$'
  komodo-mongo:
    ignore: true
    note: Built locally
```

Without an override, the changelog repo comes from the image's `org.opencontainers.image.source` label (read from the running container).

## Auto-update

Every compose line is kept as an exact tag plus the digest it serves, e.g.
`qmcgaw/gluetun:v3.41.3@sha256:…`: the version is readable, and a re-pushed tag cannot change
what runs. On each scan, for every image:

| Situation | What dimuda does |
|---|---|
| floating or digest-less tag (`v3`, `latest`, `8-alpine`, `2.1.7`) | **pin** it to the release it serves, inside the window; pins of a stack share one commit |
| lsio/hotio rebuild or patch release in the same major | **apply** once offered for `MIN_RELEASE_AGE`, inside the window |
| release notes mention a CVE, GHSA advisory or "security" | **apply** right away |
| minor release | **hold** and notify, unless the image's override says `autoUpdate: minor` |
| new major | **hold** and notify; dimuda never crosses a major |
| tag re-pushed with different content | **hold** and notify |
| an update dimuda applied was reverted in git | **hold**: it is never re-applied by itself |

Each bump is one commit, `<stack folder>: bump <image> <from> → <to> (<kind>)`, so `git revert`
undoes exactly one update. Only `GIT_REPOS` sources are changed; mounted `COMPOSE_DIRS` are
read-only, and images whose tag comes from a `${VAR}` are held because the value lives in `.env`.

ntfy gets one message for what was applied, one high-priority message for what needs review
(each held update once per reason), and one for failures such as a rejected push. A rejected
push is retried on the next scan on top of the new history.

Per-image limits live in `overrides.yaml`: `autoUpdate: none` for images whose minors break
data (Meilisearch), `autoUpdate: minor` where minors are fix releases (Postgres).

## How updates are classified

| Badge | Meaning |
|---|---|
| `major` / `minor` / `patch` | A newer upstream application version exists in the same tag family (same prefix, variant suffix such as `-alpine`, and precision). For floating tags, the running container's version label is compared with what the tag resolves to now. |
| `rebuild` | Same application version, newer packaging build (`-ls291` after `-ls290`). |
| `digest` | A floating tag moved, but the version behind it can't be determined; for a digest pin, the tag was re-pushed. |
| `pin` | Nothing newer, but the line is not yet an exact tag plus digest. |
| `up to date` | Nothing newer. |

## Development

```sh
make test     # unit tests
make lint     # gofmt + go vet
make run COMPOSE_DIRS=stacks=../docker-compose-stacks/stacks
make image
```

CI (GitHub Actions) runs the tests and pushes `linux/amd64` images to `ghcr.io/cravingpixels/dimuda` on every push to `main` and on `v*` tags.
