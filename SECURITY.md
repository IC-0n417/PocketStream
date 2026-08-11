# Security Policy

## Supported version

Only the latest PocketStream release is supported. This is a small experimental
client for OnionOS, not a security boundary or an anonymity tool.

## Reporting a vulnerability

Please do not publish an exploitable vulnerability or private user data in a
public issue. Use GitHub's **Report a vulnerability** button on the repository's
Security tab. Repository maintainers should enable **Private vulnerability
reporting** before the first public release.

Include the PocketStream version, OnionOS version, reproducible steps, and the
smallest possible sanitized log excerpt. Remove search terms, video IDs, IP
addresses, MAC addresses, Wi-Fi configuration, usernames, and filesystem paths.

## Security boundaries

- PocketStream has no Google login and never asks for credentials.
- Public Invidious instances are independent third parties and are not trusted.
- The local media relay listens on loopback and uses a per-playback random token.
- URLs resolving to loopback, private, link-local, or reserved networks are
  rejected before assets or media are fetched.
- FFmpeg and FFplay parse untrusted media. Keep OnionOS and PocketStream updated
  and do not install modified binaries from untrusted mirrors.
- The bundled `tpws` process is a local compatibility helper, not a VPN or an
  anonymity service. It does not hide the user's public IP from providers.

## Release integrity

Official release assets include `SHA256SUMS`. Verify the archive before copying
it to the SD card. Releases are built from a tagged commit by GitHub Actions.
