# Freebuff Desktop accounts (M-153)

## Concepts

- **Account**: a per-workspace record describing one Freebuff Desktop
  login. Fields: `id`, `provider` (currently `freebuff-desktop`), `label`,
  `endpoint` (desktop URL), `credential_ref` (daemon-local reference),
  `enabled`.
- **credential_ref**: never a secret. It is a reference the daemon resolves
  on its own machine (e.g. a token file path or `FB_HOME` dir). Multica's
  DB stores only the reference; the secret never leaves the daemon host.
- **Modes** on an agent: `default` (no account env, current behaviour),
  `specific` (one chosen account), `any` (lease picks one at run time).
- **Lease**: for `specific`/`any`, an account with a running or dispatched
  task holding it is considered busy. Known limit: claim-time lease
  enforcement is not yet implemented; the daemon resolves the account from
  the agent record and the bridge relies on its own single-run lock.
  Documented as a known limit until the claim path supports it.

## API reference

Base: `/api/workspaces/{workspaceId}`. Member read, admin/owner write.

- `GET  /accounts` — list accounts.
- `POST /accounts` — create. Body: `{provider, label, endpoint, credential_ref, enabled}`.
- `PATCH /accounts/{accountId}` / `PUT` — update.
- `DELETE /accounts/{accountId}` — delete (demotes dependant agents to `default`).

Agent create/update accept `account_mode` and `account_id` (required for `specific`).

Authz: listing is member-visible; `credential_ref` is returned to admins only.

## Daemon env contract

For `specific`/`any`, the daemon injects (under the agent's `custom_env`,
so `custom_env` wins on collision):

- `FREEBUFF_DESKTOP_URL` = account endpoint
- `FREEBUFF_DESKTOP_CREDENTIAL_REF` = account credential_ref

## Bridge contract (freebuff fork)

`freebuff-desktop-bridge.mjs` reads the env vars above (falling back to
`FB_URL`/`FB_HOME`) and supports `--list-models --json` emitting
`{models, supported}`. Stream-json output on stdout, exit 0 on success.

## Runtime profile setup

Create a runtime profile with `protocol_family=codebuddy` and command pointing
at the bridge (or `MULTICA_FREEBUFF_DESKTOP_PATH`). No Go registry change needed.

## Deploy and upgrade runbook

1. Merge PR into `multica-moct`, then stack PR `multica-moct → master`.
2. Update deployed Freebuff Desktop to latest upstream (recorded commit:
   `9cd09bacf`; previously referenced `3fc5979f3`).
3. Deploy via `ddvnguyen/moct` `dsh-lane-deploy.yml` from `stable`.
4. Smoke test: create 2 accounts, bind one agent to each + one to `any`,
   confirm model picker output and a run per agent.

## Known limits

- Claim-time account lease not enforced (first enabled account used for `any`); documented as known limit.
- Frontend accounts management UI (list/add/rename/enable/disable/delete) pending; selector added to ModelDropdown.
- Bridge change lives in the freebuff fork PR https://github.com/ddvnguyen/freebuff/pull/1.