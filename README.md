# dimuda

**D**ocker **IM**age **U**pdate **DA**shboard.

A self-hosted dashboard that answers, for every image in your Docker Compose stacks:

- **Is there an update, and is it the real app or just a repackaging?** LinuxServer.io (`4.0.15-ls290`) and hotio (`release-4.0.20.3014`) tags are split into upstream version and build number. An lsio rebuild shows as `rebuild`; a new Sonarr shows as `patch`/`minor`/`major`.
- **What changed?** Upstream GitHub release notes between what you run and what is available, inline.
- **Which stacks use it?** Every image lists the stacks and services that reference it (`redis` → immich, linkwarden, nextcloud).
- **What does `latest` actually contain?** Floating tags (`latest`, `stable`, `8-alpine`, `v3`) are resolved by digest to the concrete version they point to, so you know what to pin to.
- **Has upstream released something the image publisher hasn't packaged yet?** Flagged as "not packaged yet".

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
| `GIT_TOKEN` | – | Token for private git repos; sent as a header, never stored in `.git/config`. |
| `DOCKER_HOST` | – | `tcp://dockerproxy:2375` or `unix:///var/run/docker.sock`. Without it, running versions are unknown. |
| `GITHUB_TOKEN` | – | Raises the GitHub API limit for release notes from 60 to 5000 requests/hour. |
| `SCAN_INTERVAL` | `6h` | Minimum `5m`. |
| `PUID`, `PGID` | `65532` | User the app switches to after preparing `/data`. `0` is rejected. |
| `STACKS_READ_GID` | – | Comma-separated supplementary GIDs, e.g. the group that can read your stacks folders. |
| `OVERRIDES_FILE` | `/app/overrides.yaml` | Per-image overrides bundled in the image, see below. |
| `NTFY_URL`, `NTFY_TOPIC`, `NTFY_TOKEN` | – | ntfy alerts for new upstream releases. The first scan after a fresh install only records state, so it does not alert on everything at once. |
| `PUBLIC_URL` | – | Link opened when tapping an ntfy notification. |
| `DATA_DIR` | `/data` | State (last report, what was already notified) and git clones. |
| `LISTEN_ADDR` | `:8080` | |

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

## How updates are classified

| Badge | Meaning |
|---|---|
| `major` / `minor` / `patch` | A newer upstream application version exists in the same tag family (same prefix, variant suffix such as `-alpine`, and precision). For floating tags, the running container's version label is compared with what the tag resolves to now. |
| `rebuild` | Same application version, newer packaging build (`-ls291` after `-ls290`). |
| `digest` | A floating tag moved, but the version behind it can't be determined. |
| `up to date` | Nothing newer. |

## Development

```sh
make test     # unit tests
make lint     # gofmt + go vet
make run COMPOSE_DIRS=stacks=../docker-compose-stacks/stacks
make image
```

CI (GitHub Actions) runs the tests and pushes `linux/amd64` images to `ghcr.io/cravingpixels/dimuda` on every push to `main` and on `v*` tags.
