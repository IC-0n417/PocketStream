# PocketStream for OnionOS

PocketStream is an experimental, unofficial streaming client for Miyoo Mini Plus.
It shows a non-personalized trending home feed, searches public videos through a
configurable Invidious instance, and plays H.264 video through OnionOS's FFplay.

> **MVP status (August 2026):** official public Invidious instances have
> disabled their JSON APIs. PocketStream therefore reads the normal, JavaScript-free
> Invidious search/watch pages. Provider failover, HTML parsing, TLS verification,
> and the local media relay are covered by tests, but public-instance availability
> can still change without notice.

This repository contains an early public MVP. It does not provide a
server, store videos, support downloads, log in to Google, or bypass private,
Premium, age, regional, or DRM restrictions.

## Current controls

- `D-pad`: select a result or move around the virtual keyboard
- `D-pad Down` from the last video row: enter the bottom navigation bar
- `D-pad Left/Right` + `A`: activate Home, Search, History, or Exit
- `A`: open quality selection / confirm / type a character
- `B`: return to Home / erase a keyboard character / leave a video
- `X`: open the virtual keyboard
- `Y`: type a space while the virtual keyboard is open
- `Y` in History: clear all locally stored searches
- `L1` / `R1` on the keyboard: switch EN, DE, RU, ES, or FR layout
- `L1` / `R1`: previous / next results page
- `START`: search again
- `MENU`: cancel a dialog / leave a video / exit the app

The color UI uses a YouTube-inspired 3x2 grid with 16:9 thumbnails, a full-width
search bar, and a controller-friendly bottom navigation bar. Home loads a real
trending feed; it is not personalized because PocketStream has no Google login.
Press `X` to enter a query, then `START` to search. The virtual keyboard has a
space in its normal grid, and physical `Y` inserts a space immediately. Recent
queries are stored locally in `search-history.txt`, can be opened from the
History tab, and can be deleted there with `Y`. Search combines two Invidious
result pages, while `L1` and `R1`
move through the local six-card pages. English, German, Russian, Spanish, and
French keyboard layouts are included. Cyrillic and common accented European
characters are rendered directly with the built-in pixel font. Letter keys use
even language-specific rows; the shared `1-0` number row and symbol/space row
are kept in a separate lower block. A short local startup animation runs before
the home feed is loaded.

## Install on Miyoo Mini Plus

1. Enable OnionOS's **Video Player (FFplay)** package.
2. Download `PocketStream-OnionOS-<version>.zip` and its `.sha256` file from
   GitHub Releases and verify the checksum.
3. Extract the archive to the SD-card root so the final path is
   `/mnt/SDCARD/App/PocketStream`.
4. Refresh the Apps list and launch PocketStream while Wi-Fi is connected.

The console clock must already be reasonably correct. PocketStream deliberately
does not change global system time from an unauthenticated network response.

Bounded diagnostics are written beside the application. Current logs omit exact
queries, video IDs, MAC addresses, local IP addresses, and Wi-Fi configuration.
See [PRIVACY.md](PRIVACY.md) before sharing logs.

## Network compatibility

The package includes the official static ARM build of `tpws` from the upstream
network-helper project. The Miyoo kernel does not expose NFQUEUE
and OnionOS does not ship iptables, so PocketStream uses `tpws` as a local SOCKS5 proxy. Search,
metadata, thumbnail, and video HTTPS requests are dialed through `127.0.0.1:987`
using remote hostnames. PocketStream lets the user select H.264 video from 144p
through 480p (using the nearest compatible stream when an exact size is absent)
and AAC audio from DASH, exposes both tracks through temporary loopback relays,
and Onion's FFmpeg remuxes them without transcoding before FFplay displays them.
This avoids the stock FFplay 2.4 build's lack of modern DASH support. No global
firewall rules are installed and other OnionOS apps are unaffected.

The package includes a GPLv3 static ARMHF build of FFmpeg 7.0.2 from the Linux
static-build provider linked by ffmpeg.org. It is used only to remux modern
fragmented MP4 tracks into a Matroska pipe. Onion's existing FFplay still owns
video/audio output and framebuffer integration. Build provenance and the GPLv3
license are included under `ffmpeg/`.

## Known limitations

- Playback is best-effort. Some videos do not expose a compatible H.264/AAC
  stream through the selected public Invidious instance and will not start.
- Live streams, premieres, DRM-protected media, and videos that require sign-in
  are not supported.
- Public instances can fail, throttle requests, or change their HTML and DASH
  output independently of PocketStream.
- Hardware testing currently covers Miyoo Mini Plus with OnionOS. If a public
  video fails, open a bug report and attach the bounded logs after checking that
  they contain no information you consider private.

If Onion reports Wi-Fi as enabled but has no default route, `launch.sh` makes one
bounded DHCP recovery attempt using Onion's own `udhcpc` script. Diagnostics are
size-limited and record only coarse success/failure states. The helper retains
its upstream MIT license and source notice in its third-party directory.

## Build

Release builds require Go 1.26.x, have no third-party Go dependencies, and use
CGO-disabled ARMv7 output:

```sh
sh scripts/build-armv7.sh
sh scripts/package-release.sh 0.1.0
```

Git tags matching `v*` run the same tests/build in GitHub Actions and create a
release archive plus a SHA-256 file. Do not upload hand-built archives from
`outputs/`; those are ignored by Git.

For a local API check without framebuffer access:

```sh
go run ./cmd/pocketstream --api-smoke "retro gaming"
```

## Privacy and project status

Public Invidious instances are third-party services. Their operators can
potentially observe an IP address, search terms, and requested video IDs. Do not
enter credentials into PocketStream. Edit `providers.txt` to choose a different
instance; only use instances from the official Invidious list.

Before publishing a fork or binary, read [SECURITY.md](SECURITY.md),
[PRIVACY.md](PRIVACY.md), and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
The local relay is protected by a random per-playback token and rejects private,
loopback, link-local, and reserved network targets. Thumbnail dimensions and
network response sizes are bounded before full processing.

PocketStream is not affiliated with or endorsed by Google, YouTube, Invidious,
Miyoo, or OnionUI. Availability is not guaranteed because Invidious and
undocumented YouTube playback behavior can change.
