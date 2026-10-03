# PocketStream for OnionOS

PocketStream is an experimental, unofficial YouTube client for Miyoo Mini Plus
running OnionOS. It provides a non-personalized home feed, video search, quality
selection, and playback without a Google login.

> **Provider status (October 2026):** PocketStream uses Piped for discovery and
> YouTube's public Android player response for playback. Public Piped stream
> endpoints are retained only as a fallback because they currently return 5xx
> for many ordinary videos. Provider failover, response parsing, TLS
> verification, and the local media relay are covered by tests.

This repository contains an early public MVP. It does not provide a
server, store videos, support downloads, log in to Google, or bypass private,
Premium, age, regional, or DRM restrictions.

## Current controls

- `D-pad`: select a result or move around the virtual keyboard
- `A`: open quality selection / confirm / type a character
- `B`: return to Home / erase a keyboard character / leave a video
- `X`: open the virtual keyboard
- `X` on the virtual keyboard: search
- `Y` on the home screen: open search history
- `Y`: type a space while the virtual keyboard is open
- `Y` in History: clear all locally stored searches
- `L1` / `R1` on the keyboard: switch EN, RU, FR, ES, or DE layout
- `L1` / `R1`: previous / next results page
- `START`: search again
- `MENU`: cancel a dialog / leave a video / exit the app

## Install on Miyoo Mini Plus

1. Enable OnionOS's **Video Player (FFplay)** package.
2. Download `PocketStream-OnionOS-<version>.zip` and its `.sha256` file from
   GitHub Releases and verify the checksum.
3. Extract the archive to the SD-card root so the final path is
   `/mnt/SDCARD/App/PocketStream`.
4. Refresh the Apps list and launch PocketStream while Wi-Fi is connected.

PocketStream does not change the console's global clock. If the Miyoo clock is
behind a server certificate by no more than 90 days, PocketStream adjusts only
that connection's validation time while still checking the CA chain, hostname,
signature, and expiry.

## Known limitations

- Playback is best-effort, and some videos may not start.
- Live streams, premieres, DRM-protected media, and videos that require sign-in
  are not supported.
- Public instances can become unavailable or change without notice.
- Hardware testing currently covers Miyoo Mini Plus with OnionOS.

## Build

Release builds require Go 1.26.x and use CGO-disabled ARMv7 output:

```sh
sh scripts/build-armv7.sh
sh scripts/package-release.sh 1.0.22
```

Git tags matching `v*` run the same tests/build in GitHub Actions and create a
release archive plus a SHA-256 file. Do not upload hand-built archives from
`outputs/`; those are ignored by Git.

For a local API check without framebuffer access:

```sh
go run ./cmd/pocketstream --api-smoke "retro gaming"
```

## Privacy and project status

Public Piped and Invidious instances are third-party services. Their operators can
potentially observe an IP address, search terms, and requested video IDs. Do not
enter credentials into PocketStream. Edit `piped-providers.txt` or
`providers.txt` to choose different instances.

Before publishing a fork or binary, read [SECURITY.md](SECURITY.md),
[PRIVACY.md](PRIVACY.md), and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
The local relay is protected by a random per-playback token and rejects private,
loopback, link-local, and reserved network targets. Thumbnail dimensions and
network response sizes are bounded before full processing.

PocketStream is not affiliated with or endorsed by Google, YouTube, Piped,
Invidious, Miyoo, or OnionUI. Availability is not guaranteed because provider and
undocumented YouTube playback behavior can change.

## Support

If you enjoy PocketStream, you can [support Miyoo Mini Ports on Boosty](https://boosty.to/miyoominiports/donate).
