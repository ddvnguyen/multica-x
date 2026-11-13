# Repository setup for agent-driven auto-merge

How `ddvnguyen/multica-x` is configured so agents land changes through pull
requests with CI as the hard gate, and a human (or the Lead/Architect) only for
the verdict. Adapted from the MOCT guide
(`ddvnguyen/Knowledge_Documents`, `moct/repository-setup.md`); M-324 is the
worked change. The full reference guide also covers the optional
`integration` branch and stack-PR patterns — this fork does not use them.

## Why no `integration` branch here

`multica-moct` already is the rolling integration lane and the deploy source
(`deploy-main.yml`, lane z autodeploy), and every PR targets it. The stack-PR
pattern assumes `integration -> default branch`, and the default branch here is
the lane itself, so adding both would change the delivery flow for no gain.
`main` stays the unprotected upstream mirror and receives no PRs.

## GitHub settings (as configured by M-324)

1. **Default branch**: `multica-moct`.
2. **Allow auto-merge**: enabled. Without it `gh pr merge --auto` is refused.
3. **Branch protection on `multica-moct`**: required status check `ci` only
   (`strict` off, no required reviews, admins not enforced, force-push and
   deletion blocked). Keep the required-checks list to `ci` alone; other
   checks are informational or skipped by path scope. `main` is unprotected.
4. **Workflow permissions**: read-only default. CI itself needs only
   `contents: read`; nothing in this repo pushes with `GITHUB_TOKEN`.

## The `ci` check

`.github/workflows/ci.yml` defines a top-level aggregate job `ci` that needs
`[changes, frontend, backend, mmx-firewall]` and reuses
`scripts/ci-scope.mjs gate`. Semantics: a path-selected job must succeed, an
unselected job must be skipped, and any failure, cancellation, or `changes`
failure fails the check. Docs-only changes still report `ci` because `changes`
always runs. `mmx-firewall` is accepted as `skipped` when the run is not
self-hosted.

## Agent credentials and identity

- Agents authenticate as one shared GitHub identity (`ddvnguyen`), which holds
  admin. Because GitHub refuses an approving review from a PR's own author,
  `reviewDecision` is never `APPROVED`; treat GitHub review state as
  informational only.
- The review of record is the Lead/Architect verdict on the Multica card.

## Merge procedure for implementation PRs

1. Open a PR from the feature branch into `multica-moct`.
2. Wait for the `ci` check to pass (a queued macOS leg can be cancelled by
   GitHub-hosted capacity; rerun the failed jobs, it is infrastructure).
3. Obtain the Architect/Lead verdict on the Multica card.
4. Merge the current head, pinned so nothing landed unseen:
   `gh pr merge <PR> --merge --match-head-commit <head-sha>`.
5. After a protection change, prefer auto-merge for routine PRs:
   `gh pr merge <PR> --auto --merge` — GitHub merges once `ci` is green.
   Request it only after the verdict, because it merges on green `ci` without
   waiting for anyone.

## Test PR walkthrough (M-324)

This document is the walkthrough: a docs-only PR into `multica-moct` whose
`ci` check runs and passes, merged with `gh pr merge --auto --merge` to prove
the auto-merge setup end to end.

## Rollback

Delete the branch protection on `multica-moct` and set
`allow_auto_merge=false` (two API calls). The `ci` job stays in the workflow
but is inert when nothing requires it; direct pushes to the lane resume.
