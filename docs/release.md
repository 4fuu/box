# Release

box uses a calendar version, `YYYY.MDD.REVISION`, with the date in
`Asia/Hong_Kong`. The month is not padded, the day is two digits, and the
first release of a day uses revision zero. `2026.924.0` is the first release
on 2026-09-24.

## Cut a release

Write the release notes first. Rewrite [`RELEASE_NOTES.md`](../RELEASE_NOTES.md)
with the user-facing changes since the previous release, in English: what
changed, what was fixed, and anything an operator must do differently. The
workflow refuses to run while the file is empty, and it publishes the file
as the GitHub release body, ahead of the install instructions.

Then, on `main`, set the version and push both:

```bash
python3 scripts/set-version.py 2026.924.0
git add VERSION RELEASE_NOTES.md
git commit -m "release: v2026.924.0"
git push origin main
```

Dispatch the `Release` workflow on that commit. The workflow refuses a version
whose date is not today in `Asia/Hong_Kong`, and it aborts if `main` moves
before the GitHub release is created.

The workflow:

- tests the module
- publishes the release notes from `RELEASE_NOTES.md` as the GitHub release body
- publishes `box-<version>-linux-amd64.tar.gz`, `box-<version>-linux-arm64.tar.gz`, and `box-<version>-windows-amd64.zip`, plus `SHA256SUMS`

There is no container image. A Linux server or computer installs from the
release with [`scripts/install.sh`](../scripts/install.sh). The script asks
whether the machine is a server or a computer, in English or Chinese. A
Windows computer installs from the zip; see the README.
