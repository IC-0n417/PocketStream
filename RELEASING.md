# Release checklist

## One-time repository setup

- Enable two-factor authentication or a passkey on the maintainer account.
- Use GitHub's `noreply` commit email if the maintainer does not want a personal
  email embedded in Git history.
- In **Settings → Code security**, enable Private vulnerability reporting.
- Protect `main`: require the CI workflow and block force-pushes.
- Do not commit the `work/`, `build/`, or `outputs/` directories.

## Version release

1. Update `VERSION`, `appVersion` in `cmd/pocketstream/main.go`, and `userAgent`
   in `internal/invidious/client.go` to the same semantic version.
2. Confirm all third-party hashes in `THIRD_PARTY_NOTICES.md` and the CI workflow.
3. Run:

   ```sh
   sh scripts/build-armv7.sh
   sh scripts/package-release.sh "$(cat VERSION)"
   ```

4. Inspect the ZIP and confirm it contains no `*.log`, history, local paths,
   credentials, or development diagnostics.
5. Test search, recommendations, every quality option, video exit, history clear,
   offline startup, and a second launch on a clean OnionOS SD card.
6. Commit the release, create an annotated tag such as `v0.1.0`, and push the tag.
7. Let `.github/workflows/release.yml` build and publish the GitHub Release.
8. Download the published archive, verify its SHA-256, and install that exact
   artifact once before announcing it.

Never upload the old `outputs/PocketStream-OnionOS-prototype.zip` or
`outputs/PocketStream-source.zip`; they predate the hardened release process.
