# Lane z auto-deploy (M-151)

Pull-based CD for lane z (`dsh-z`). CI never reaches the lane and nothing is
exposed inbound: a `systemd --user` timer on the lane host asks GitHub for the
newest **successful** push run of `CI` on `multica-moct`, and installs it when
that head SHA differs from the release the lane is running.

Every tick is one script, one lock, one log file:

| File | Role |
| --- | --- |
| `autodeploy.sh` | one tick: resolve → download → verify → back up → install → migrate → switch → health → plugin → cleanup → comment |
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

The evidence comment needs a Multica CLI identity of its own: the config is
seeded from `~/.multica/config.json` into `AUTODEPLOY_MULTICA_CONFIG` and
switched to the MOCT workspace. That identity is the host user's, so every
comment this bot posts starts with `**[autodeploy]**`.

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
* **Newest SHA wins.** The newest green run is re-read after the download, so a
  commit superseded mid-download is dropped for the next tick instead of
  deployed. A SHA that already failed is not retried every minute
  (`data/autodeploy.failed`).
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
  health-check, then post the red report (with the dump path) on the card.
* **Disk floor.** A tick refuses to start below `AUTODEPLOY_MIN_FREE_GB` (3G)
  and keeps `AUTODEPLOY_KEEP_RELEASES` (5) releases; the artifact directory is
  deleted after install.

## What a tick prints

```
[autodeploy 15:47:06Z] step: switch
[autodeploy 15:47:07Z] multica-be-z.service -> current -> releases/360497511; restarting
[autodeploy 15:47:09Z] health ok: /multica/health commit=3604975
[autodeploy 15:47:10Z] evidence comment posted on M-169
[autodeploy 15:47:10Z] done in 38s; /mnt/workspace 9G free (was 9G)
```

A CI-built release answers `/multica/health` with the short sha (the Makefile
stamps `git rev-parse --short HEAD`) while `deploy.sh` stamps the full one, so
the health assertion accepts either form of the same commit.

## Troubleshooting

* Logs: `$AUTODEPLOY_LOG_DIR/YYYYMMDDTHHMMSSZ.log`, one per tick, including the
  skipped ones.
* `lane z already runs <sha>; nothing to do` — normal, the newest green run is
  already deployed.
* `no successful push run found` — CI on `multica-moct` has no green push run
  yet; a pull-request run does not count.
* `card <M-NNN> is not readable` — the CLI config cannot see the workspace. The
  generated body is kept at `data/autodeploy-comment.md`; fix the config and
  post it by hand.
* Stop the timer while debugging: `systemctl --user stop dsh-z-autodeploy.timer`.
