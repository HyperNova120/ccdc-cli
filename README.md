# ccdc-cli

A portable CLI (and now TUI) for CCDC inventory and credential rolling
across MySQL, PostgreSQL, and Kubernetes.

## What changed in this pass

**Important caveat first:** this was written without a Go toolchain or
network access available to verify compilation. Everything below was
reviewed carefully by hand, but run `go build ./...` (see below) before
you trust it in a live environment, and treat this as a strong draft
rather than a tested release.

### Unix socket support (new)
Both `mysql` and `psql` now accept `-S`/`--socket` as an alternative to
`-H`/`--host` (mutually exclusive with it):
```
ccdc-cli mysql -S /var/run/mysqld/mysqld.sock -u root -i
ccdc-cli psql -S /var/run/postgresql -u postgres -i
```
For psql, `--socket` takes the *directory* containing the socket (same
convention `psql`/`pg_dumpall` themselves use - it looks for
`<dir>/.s.PGSQL.<port>`), not the socket file itself. For mysql,
`--socket` takes the socket file path directly.

This matters because MySQL/Postgres grants are host-pattern-based: a
local `mysql -u user -p` / `psql` login (no `-h` given) normally goes
over a Unix socket and matches a `'user'@'localhost'`-style grant, while
ccdc-cli previously always connected over TCP and needed a grant matching
the connecting host specifically. If inventory reports auth failures that
don't match what you see logging in manually, try `--socket` to connect
the same way the local CLI does - `describePingError` will also tell you
this directly when it detects the mismatch.

Saved targets support this too - `ccdc-cli targets add ... --socket
/path/to/socket` - and the TUI's add-target form has a Socket field
(leave it blank to use Host/Port).

### Bugs fixed
- **MySQL anonymous-login check was broken in two separate ways.** First,
  `connectToDatabase()` took a `user` parameter but used the package-global
  `username` variable instead, so the test (which passes an empty
  username) was silently connecting as your real `-u` user. Second, once
  that was fixed, a pre-existing **inverted-logic bug** surfaced: a
  *successful* anonymous connection printed "Anonymous login disabled"
  (backwards), and any unrelated error (timeout, wrong port, anything
  that wasn't exactly MySQL error 1045) printed "allows ANONYMOUS login"
  - a false positive. Both are fixed; an inconclusive result is now
  reported honestly instead of guessed.
- **MySQL DSN was a hand-formatted string, not properly escaped.**
  `fmt.Sprintf("%s:%s@tcp(...)", user, password, ...)` silently corrupts
  if the password contains `@`, `:`, `/`, or other DSN-meaningful
  characters - very plausible for generated CCDC creds - even though the
  same password works fine with the real `mysql` CLI (which never has to
  serialize it into a connection string). Now built via the driver's own
  `mysql.Config`/`FormatDSN()`, which escapes correctly.
- **The real MySQL auth error was discarded and never shown.** Every
  caller just printed a generic "Authentication failed" with no detail.
  Added `describePingError()` which surfaces the actual driver error and
  specifically flags the classic gotcha: a working local `mysql -u user
  -p` login often goes over a Unix socket (matches a `'user'@'localhost'`
  grant), while ccdc-cli always connects over TCP (needs a grant matching
  `'user'@'127.0.0.1'` or `'user'@'%'`). If mysql/psql inventory reports
  anonymous access or auth failure in a way that doesn't match what you
  see logging in manually, this is almost always why - check `SELECT
  user, host FROM mysql.user WHERE user='youruser';` on the box.
- **mysql restore deleted the backup file you were restoring FROM on
  failure** (`os.Remove(file)` on the *source* file, not a partial
  output). If a restore failed partway - network hiccup, bad creds,
  whatever - your only backup was gone. Removed.
- **k8 inventory silently discarded 7 different section errors.**
  `clusterTopologyAndNodes`, `podAndNetworkInventory`, and five other
  inventory sections return an error on failure (e.g. RBAC denies
  listing nodes), but `runInventory` was throwing every one of them away
  with `_ = fn(...)` - so a denied section just rendered blank with zero
  explanation. For a security inventory tool, "we got denied access to
  X" is itself an important finding. Now every section's real error
  prints inline and rolls up into a summary at the end.
- **K8s secret rotation double-decoded base64.** `client-go` already
  base64-decodes `Secret.Data` for you; the code decoded it a second time
  when printing "current value," which would error or print garbage on
  any real secret. Fixed to use the value as-is.
- **K8s rotation annotation key had inconsistent casing** between the
  create path (`kube-secret-rotator/rotated`) and update path
  (`Kube-secret-rotator/rotated`) — the update path was never actually
  overwriting the same annotation, just adding a second one every time.
  Both now write the same key.
- **A `Printf` was missing its format verb** in `securityVars()`, so
  the actual DB error was never shown, just the literal string.
- Removed dead duplicate `cachedPassword`/`askedPass` globals in
  `mysqlModule` that shadowed (and were never used instead of) the real
  ones in `utils`.

### CLI resilience (exit codes now mean something)
- `runInventory`/`runBackup`/`runRestore` in mysql, psql, and k8 now
  return real errors instead of silently returning void, and `runCmd`
  in each module propagates the first failure - so `$?` actually
  reflects whether the command succeeded, which matters if you're
  looping over targets or checking results in a script/CI pipeline.
  Added `SilenceErrors` to each command so Cobra's own error printing
  doesn't double up with `cmd/root.go`'s.
- The TUI's capture wrappers now prepend a clear `=== SUCCEEDED ===` /
  `=== FAILED: <reason> ===` banner to the output (via the new
  `utils.WithResultBanner`), using these same real errors - so a failure
  is obvious at a glance in the output viewer, without losing any of the
  detailed printed diagnostics underneath it.

### New capabilities
- **Secrets are redacted by default** everywhere the k8 module used to
  print plaintext (inventory listing, rotation current-value, rotation
  new-value). Pass `--reveal` to opt into plaintext, same as before.
  This matters more than it sounds like — printing live secrets to a
  terminal during a comp is a shoulder-surfing/logging/tmux-history risk
  you don't want by default.
- **Saved targets** (`ccdc-cli targets add/list/remove`), stored in
  `~/.ccdc-cli/targets.yaml`. No more retyping `-H -u -p` for every box:
  ```
  ccdc-cli targets add web1 --type mysql --host 10.0.1.5 --username root --notes "team web box"
  ccdc-cli targets add k8s-prod --type k8 --kubeconfig ~/.kube/prod.yaml
  ccdc-cli targets list
  ```
  Passwords are never stored — only connection info.
- **A fully self-sufficient TUI dashboard** (`ccdc-cli tui`). Everything
  the CLI can do is reachable from here — a completely fresh install can
  be run entirely through the TUI, no other terminal needed:
  - Add, edit, and delete saved targets from within the TUI (`a`/`e`/`d`
    on the target list) — first run, with zero targets saved, prompts
    you to add one right there.
  - Run inventory, backup, or restore against mysql/psql targets, with
    file-path and password prompts and a destructive-action confirm
    before restore actually runs.
  - **Browse k8s secrets interactively**: pick a namespace → pick a
    secret (tagged so you can see at a glance whether it's a system/TLS
    secret or a real opaque one worth rotating) → pick a key → see its
    current value (redacted by default, `v` to reveal) → rotate it to
    either a fresh random value or one you type in, choosing whether to
    retain the previous value under `KEY_PREV`. Every rotation goes
    through an explicit confirm screen first, since it's destructive and
    can't be undone.
  - The raw `SECRET_NAME,NAMESPACE,KEY,STRATEGY[|...]` sequence syntax
    is still available too (`Rotate (raw sequence)` in the k8 action
    menu) for multi-secret rotations in one shot, same as the CLI's `-s`
    flag.
  - Save any result to a report file (`~/.ccdc-cli/reports/`).
  It's a thin layer on top of the *same* underlying functions the CLI
  uses (via new exported `*Capture` functions in each module, plus
  `ListNamespaces`/`ListSecrets`/`GetSecretKeyValue`/
  `RotateSecretValueCapture` in the k8 module) so the two interfaces
  can't drift apart on what they actually do.
- **The CLI remains the primary, most resilient interface.** Nothing
  about the TUI changes flag-command behavior; the TUI is additive and
  calls into the exact same code paths. If you only ever script against
  `ccdc-cli mysql/psql/k8 ...`, nothing here affects you.

### Layout
```
main.go
cmd/
  root.go        (unchanged)
  targets.go     (new: targets add/list/remove)
  tui.go         (new: tui command)
mysqlModule/mysql.go   (bug fixes + RunInventoryCapture/RunBackupCapture/RunRestoreCapture/TestConnectionCapture)
psqlModule/psql.go     (+ RunInventoryCapture/RunBackupCapture/RunRestoreCapture)
k8Module/k8.go         (bug fixes, redaction, + RunInventoryCapture/RollCredentialsCapture/
                         ListNamespaces/ListSecrets/GetSecretKeyValue/RotateSecretValueCapture)
utils/utils.go         (+ SetPassword/ResetPassword/Redact/CaptureStdout)
config/config.go       (new: targets.yaml read/write)
tui/app.go             (new: Bubble Tea dashboard - target CRUD, actions, k8 secret browser)
```

## Build

```
go mod tidy   # fetches the new TUI deps (bubbletea/bubbles/lipgloss) and
              # promotes yaml.v3 to a direct dependency - REQUIRED, the
              # committed go.sum doesn't have entries for the new deps yet
go build ./...
```

If `go mod tidy` or `go build` surfaces anything, paste the error back
and I'll fix it — or if you want a tighter compile-test-fix loop than a
chat interface can give you, Claude Code (terminal/VS Code/JetBrains) has
a real Go environment and can iterate against actual compiler output.

## Usage

Flag-based (unchanged, and still the recommended path for scripting/CI -
it's the most resilient interface since it has no interactive state to
get into a weird spot):
```
ccdc-cli mysql -H 10.0.1.5 -u root -i
ccdc-cli psql -H 10.0.1.6 -i
ccdc-cli k8 -i --reveal
ccdc-cli k8 -r -s "mysecret,default,password,retainPrev"
```

TUI (new) — a completely fresh install can do everything from here:
```
ccdc-cli tui
```
- Target list: `↑/↓` move, `enter` open actions, `a` add target, `e` edit,
  `d` delete, `q` quit.
- Per-target action menu: Inventory / Backup / Restore (mysql, psql);
  Inventory / Browse & Rotate Secret / Rotate (raw sequence) (k8).
- k8 secret browser: namespace → secret → key → view value (`v` to
  reveal) → `r` rotate to random / `u` rotate to a value you type in →
  choose retainPrev/omitPrev → confirm.
- Output screen: `s` save report, `esc`/`b` back, `q` quit.

## Known limitations / good next steps
- No concurrent/parallel scanning across multiple targets yet — each
  run is one target at a time. A "run inventory against all mysql
  targets" batch mode would pair well with the report-saving already in
  place.
- No encryption on `targets.yaml` — it's connection info only (no
  passwords), but if that ever changes, revisit.
- The k8 secret browser fetches namespaces/secrets fresh on each
  navigation step rather than caching, so backing out and back in
  re-queries the API - fine for CCDC-scale clusters, worth revisiting
  if you're pointing this at something huge.
