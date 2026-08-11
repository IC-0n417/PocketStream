# Third-party notices

PocketStream's own Go source is licensed under the MIT License in `LICENSE`.
The release package also contains the following separately licensed components.

## FFmpeg 7.0.2 static ARMHF build

- Purpose: remux H.264/AAC DASH tracks to a Matroska pipe without transcoding.
- Binary provider: https://www.johnvansickle.com/ffmpeg/
- Upstream source: https://ffmpeg.org/releases/ffmpeg-7.0.2.tar.xz
- Builder source index: https://johnvansickle.com/ffmpeg/release-source/
- License: GNU GPL version 3; see `ffmpeg/GPLv3.txt`.
- Binary SHA-256: `0afba4a11110e6e402053e0fc14c33a7eb207d7a588688ae87dba471a0f06c71`.
- Exact build inventory: `ffmpeg/BUILD-INFO.txt`.

FFmpeg is a separate executable invoked through pipes. PocketStream's MIT
license does not replace or narrow FFmpeg's GPL terms.

## zapret tpws v72.13

- Purpose: local SOCKS5 network-compatibility process used only by PocketStream.
- Source and release: https://github.com/bol-van/zapret/releases/tag/v72.13
- License: MIT; see `zapret/LICENSE.txt`.
- Binary SHA-256: `412032484525f7fef8ea7d69e7eefc2c972098aee8879a5f93c70dcfb75b1438`.

The unused `nfqws` binary and the pre-release capability diagnostic are not
included in release packages.

## Mozilla CA certificate bundle

- Distribution source: https://curl.se/docs/caextract.html
- Snapshot: 2026-07-16, 119 certificates.
- License: Mozilla Public License 2.0.
- SHA-256: `3ff344e30b9b1ed2971044eabb438a08f2e2245ddb5f8ab1a3ad8b63ab4eaf91`.

See `CA-BUNDLE-SOURCE.txt` in the package for the original notice.
