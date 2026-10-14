# Releasing

Releases are cut by pushing a `v*` tag. The `Release` workflow
(`.github/workflows/release.yml`) runs GoReleaser, which builds the
binaries, signs the checksums, and publishes a GitHub Release. The
Terraform Registry reads from that release.

## Steps

1. **Make sure `main` is ready.** Pull the latest `main` and run the same
   checks CI runs:

   ```bash
   git switch main && git pull
   mise run check
   ```

   If the schema changed, run `mise run tfdocs` and commit the regenerated
   `docs/` first. `tfdocs-check` fails otherwise.

2. **Pick the version.** Use full semver with a `v` prefix, such as
   `v0.2.0`. Do not use `v0.2`. The Registry expects three parts, and the
   existing tags (`v0.1.0`, `v0.1.1`) follow that format.

3. **Tag and push.** Use an annotated tag on the commit you want to ship:

   ```bash
   git tag -a v0.2.0 -m "v0.2.0"
   git push origin v0.2.0
   ```

4. **Approve the run.** The workflow runs in the `release` environment, which
   requires reviewer approval before the `GPG_PRIVATE_KEY` secret is exposed.
   Approve the "Release" run for the tag in GitHub Actions.

5. **Check the release.** The GitHub Release should contain:

   - one zip per OS and architecture, named
     `terraform-provider-firezone_<version>_<os>_<arch>.zip`
   - `terraform-provider-firezone_<version>_SHA256SUMS`
   - the detached signature of that checksum file (`.sig`). The Registry
     rejects a release without it.
   - `terraform-provider-firezone_<version>_manifest.json`

6. **Write the release notes.** `.goreleaser.yml` sets `changelog: disable`,
   so the GitHub Release body is not generated from commits. Edit the notes by
   hand.

## Notes

- Only tag commits that are on `main`. The workflow does not check which
  branch a tag points to.
- The workflow does not run the test suite. Tagging is the last point where a
  broken commit is caught, so run `mise run check` before step 3.
- A tag that has been pushed should not be moved or reused. If a release is
  broken, cut a new patch version.
