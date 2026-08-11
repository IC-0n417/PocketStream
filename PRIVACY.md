# Privacy

PocketStream has no accounts, analytics, advertising SDK, telemetry endpoint,
or crash-reporting service. It does not ask for names, email addresses,
passwords, cookies, or payment information.

## Data sent over the network

Search terms, selected video IDs, thumbnail requests, and media requests are
sent to the configured public Invidious provider. The provider and its hosting
network can observe the user's public IP address and request metadata. Media may
also be served through infrastructure selected by that provider. PocketStream's
local network helper does not anonymize these requests.

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

On the first launch of version 0.1.0, pre-release diagnostic logs are deleted
once because older prototypes recorded more network detail.
