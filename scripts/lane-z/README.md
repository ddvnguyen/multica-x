# Lane z auto-deploy (M-151, M-556 unified release)

Pull-based CD for lane z (`dsh-z`). CI never reaches the lane and nothing is
exposed inbound: a `systemd --user` timer on the lane host asks GitHub for the
SHA the **`production` tag** points at — moved by `deploy-main.yml` only after
production is green on that SHA — and installs it when that pinned SHA differs
from the release the lane runs. The lane never follows the branch head, so a
SHA production does not have is never installed and the two sides cannot drift.

One release = one SHA (M-556): Lead dispatches `Deploy Multica` with
`source_ref` set to the full 40-char SHA (which must already be merged on
`multica-moct` with a green CI push run). The workflow deploys production
first, then moves the `production` tag; the lane picks the pin up within ~60s.
A lane z failure is reported and retried on its own timer — it never fails or
rolls back production. Rollback is a new release run with the previous SHA
(see below); there is deliberately no per-merge production deploy, because a
production deploy cancels running agent tasks after the drain timeout.

Every tick is one script, one lock, one log file:

| File | Role |
| --- | --- |
| `autodeploy.sh` | one tick: resolve → download → verify → back up → install → migrate → switch → health → plugin → cleanup → evidence |
| `rollback.sh` | put the lane back on a previous release (binaries only) |
| `lane-lib.sh` | shared: render the backend unit, read the commit, wait on HTTP |
| `install-units.sh` | install the scripts and units on the host (idempotent, no sudo) |
| `units/` | the timer and oneshot service |

## Install (once per host)

```bash
bash scripts/lane-z/install-units.sh
bash /mnt/workspace/clouds/dsh-z/bin/autodeploy.sh --dry-run
```

`install-units.sh` copies the scripts to `AUTODEPLOY_BIN_DIR`
(`/mnt/workspace/clouds/dsh-z/bin`), so the timer never depends on a task
checkout that gets cleaned up, then enables `dsh-z-autodeploy.timer`
(`OnBootSec=2min`, `OnUnitActiveSec=60s`).

## Evidence comments

The lane does not comment itself. This host holds the Owner's Multica token, so
a comment posted from here would carry the Owner's name — evidence comments must
carry an agent identity instead.

Instead each tick writes `$AUTODEPLOY_REPORT_DIR/latest.md`:

```
card: M-151
sha: 5f675ff62e79e6d1a4e0ae92f54e43da5a1efafc
generated: 2026-11-01T05:27:57Z
---
**[autodeploy]** lane z is on `5f675ff62`
...
```

and POSTs the `lane-z deploy reporter` autopilot's webhook. That run reads the
file and posts the body after the `---` on the named card. The webhook URL is a
credential: it lives in `$AUTODEPLOY_WEBHOOK_FILE` (mode `0600`), never in the
repo, a log, or a comment. `install-units.sh` refuses to run without it.

If the webhook cannot be reached after three attempts the evidence stays in
`latest.md`, the deploy is **not** rolled back (the lane is healthy), and the
tick exits non-zero so `systemctl --user status dsh-z-autodeploy.service` shows
it. A failed notification must never undo a good deploy.

## Run it

```bash
autodeploy.sh                       # one tick (what the timer runs)
autodeploy.sh --dry-run             # print the plan; changes nothing
autodeploy.sh --sha <sha>           # deploy a specific green commit
autodeploy.sh --drill-fail-health   # real deploy, forced health failure: proves
                                    # the rollback and prints the red report
```

```bash
rollback.sh                         # the release recorded by the last deploy
rollback.sh --to <sha|path>         # an explicit release
rollback.sh --list                  # releases on disk, newest first
```

## Invariants

* **No rebuild.** Binaries, migrations and the plugin bundle come from the CI
  run's artifacts. A missing or mismatched artifact is a red report, never a
  local build.
* **One deploy at a time.** `flock` on `data/autodeploy.lock`; a tick that
  loses the lock exits 0 without touching anything.
* **The production pin is the only target.** The tick resolves
  `repos/<repo>/git/ref/tags/<AUTODEPLOY_PIN_REF>` (`production`) and deploys
  exactly that SHA. When the pin is unreachable the tick skips without touching
  anything; when the pinned SHA has no green CI push run (no artifacts) yet it
  also skips and retries next tick. `--sha` still overrides the pin for drills.
* **Pinned SHA wins.** The pin is re-read after the download, so a SHA
  superseded by a newer release mid-download is dropped for the next tick
  instead of deployed.
* **Targets only move forward.** Before inspecting artifacts, the target is
  compared with the running release through GitHub's compare API. Only
  `ahead` or `identical` is accepted; a behind, diverged, unknown, or failed
  comparison skips the tick without a dump or report. A target SHA that fails
  deployment is recorded in `data/autodeploy.failed` and is not retried on
  every tick. A successful deploy clears the marker.
* **The database is backed up, never restored automatically.** `pg_dump -Fc`
  runs before any migration and its path goes into the evidence comment.
  Rolling back flips binaries only: `pg_restore` over a live database silently
  discards every write since the dump, so the schema is a human's decision.
* **The unit follows `multica-be/current`.** `deploy/dsh-z/deploy.sh` pins one
  release directory per install, and systemd does not re-resolve a symlink for a
  running unit — a switch that only flips `current` restarts the *old* binary
  while `current/release.json` advertises the new one. `lane-lib.sh` renders the
  unit with `WorkingDirectory`/`ExecStart` at `current`, so flipping the symlink
  and restarting is enough.
* **`dsh-z.service` is only restarted when the plugin bundle changed.** A push
  that touches nothing under `packages/` produces no `dsh-plugin-<sha>`
  artifact, and then the installed bundle is already correct.
* **A failed deploy rolls back and reports.** Every failure goes through
  `fail()` so the `ERR` trap runs: flip to the previous release, restart,
  health-check, then deliver the red report (with the dump path) through the
  reporter autopilot.
* **Disk floor and backups.** A tick refuses to start below
  `AUTODEPLOY_MIN_FREE_GB` (3G), keeps `AUTODEPLOY_KEEP_RELEASES` (5) releases,
  and prunes pre-deploy dumps to the newest `AUTODEPLOY_BACKUP_KEEP_COUNT`
  (default 10), always preserving the dump for the running release. Dry-run
  reports which backups it would prune without deleting them. The artifact
  directory is deleted after install.

## What a tick prints

```
[autodeploy 15:47:06Z] step: switch
[autodeploy 15:47:07Z] multica-be-z.service -> current -> releases/360497511; restarting
[autodeploy 15:47:09Z] health ok: /multica/health commit=3604975
[autodeploy 15:47:10Z] evidence written to .../logs/autodeploy/latest.md (card M-169)
[autodeploy 15:47:10Z] reporter autopilot notified for M-169
[autodeploy 15:47:10Z] done in 38s; /mnt/workspace 9G free (was 9G)
```

A CI-built release answers `/multica/health` with the short sha (the Makefile
stamps `git rev-parse --short HEAD`) while `deploy.sh` stamps the full one, so
the health assertion accepts either form of the same commit.

## Troubleshooting

* Logs: `$AUTODEPLOY_LOG_DIR/YYYYMMDDTHHMMSSZ.log`, one per tick, including the
  skipped ones.
* `production pin unreachable; skipping` — the tag is missing or the API call
  failed. The lane keeps running its current release; fix the tag (re-run the
  release workflow) instead of forcing a SHA.
* `no successful push run ... for pinned <sha> yet` — the pin moved before CI
  published artifacts. The tick retries on its own; do not force it.
* `lane z already runs <sha>; nothing to do` — normal, the pinned SHA is
  already deployed.
* `no reporter webhook at ...` — `$AUTODEPLOY_WEBHOOK_FILE` is missing or empty.
  Rewrite the URL from `multica autopilot get <id> --show-secrets` (it is a
  credential: `chmod 600`, never commit it).
* `reporter webhook failed after 3 attempts` — the control plane was
  unreachable. The evidence is in `latest.md`; post it by hand if it matters.
* `no merged PR with an M-NNN reference` — the commit came from a push with no
  PR; the deploy is still valid, there is just no card to report on.
* Stop the timer while debugging: `systemctl --user stop dsh-z-autodeploy.timer`.

## Rollback

* **Production** (unchanged): `deploy-main.sh` restores `.env` and the old
  daemon binary automatically when a deploy fails (`database.dump` is retained,
  never restored automatically). To roll back a good deploy, re-run the
  `Deploy Multica` workflow with `source_ref` set to the previous SHA — that
  moves the `production` pin too, so lane z follows.
* **Lane z**: binaries only, never the database —
  `rollback.sh` / `rollback.sh --to <sha|path>` / `rollback.sh --list`.
  The DB dump from the failed deploy is for a human to inspect, not to restore
  automatically.
