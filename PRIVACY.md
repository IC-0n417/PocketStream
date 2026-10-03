# Privacy

PocketStream has no accounts, analytics, advertising SDK, telemetry endpoint,
or crash-reporting service. It does not ask for names, email addresses,
passwords, cookies, or payment information.

## Data sent over the network

Search terms, selected video IDs, thumbnail requests, and media requests are
sent to the configured public Piped or Invidious provider and, when needed, to
YouTube's public player endpoint. The provider and its hosting
network can observe the user's public IP address and request metadata. Media may
also be served through infrastructure selected by that provider. PocketStream's
local network helper does not anonymize these requests.

When the console's system DNS cannot reliably reach YouTube infrastructure,
PocketStream resolves only YouTube, thumbnail, and Google Video hostnames with
Cloudflare's encrypted DNS-over-HTTPS service at `1.1.1.1`. Cloudflare can
observe those hostname lookups and the user's public IP address. Search text,
video titles, and media contents are not included in DNS queries.

## Data stored on the SD card

`search-history.txt` stores at most ten recent searches so the History screen
works. In History, press **Y** to delete it. Removing the application directory
also removes the history.

Bounded diagnostic logs may be created in the PocketStream application folder.
Each log is rotated at 256 KiB and one previous copy is retained. PocketStream
does not record exact search text, video IDs, MAC addresses, local IP addresses,
or Wi-Fi credentials in the current log format.

The SD card normally uses FAT, so Unix permission bits do not provide meaningful
confidentiality. Anyone who can read the card can read its files. Do not upload
raw logs or the complete application directory when requesting support.

On the first launch of version 1.0.0, pre-release diagnostic logs are deleted
once because older prototypes recorded more network detail.
