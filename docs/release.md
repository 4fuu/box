# Release

box uses a calendar version, `YYYY.MDD.REVISION`, with the date in
`Asia/Hong_Kong`. The month is not padded, the day is two digits, and the
first release of a day uses revision zero. `2026.924.0` is the first release
on 2026-09-24.

## Cut a release

On `main`, set the version and push it:

```bash
python3 scripts/set-version.py 2026.924.0
git add VERSION
git commit -m "release: v2026.924.0"
git push origin main
```

Dispatch the `Release` workflow on that commit. The workflow refuses a version
whose date is not today in `Asia/Hong_Kong`, and it aborts if `main` moves
before the GitHub release is created.

The workflow:

- tests the module
- publishes `box-<version>-linux-amd64.tar.gz` and `box-<version>-linux-arm64.tar.gz`, plus `SHA256SUMS`
- builds the Fedora base computer image with that `box` binary copied in, and pushes a multi-arch image to `ghcr.io/<owner>/box:<version>` and `:latest`

The image does not compile `box`. The Dockerfile copies the release binary.

A Linux server or deploy node installs from the release with
[`scripts/install.sh`](../scripts/install.sh). The script asks whether the
machine is a server or a deploy node, in English or Chinese.
