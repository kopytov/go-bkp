# Compatibility with raku-bkp

Deliberate departures from `../raku-bkp`, one section each: what the Raku
version does, what this one does instead, and why. Everything not listed here
behaves as the Raku version does.

## Removed classes

`src` classes `ploop`, `lvm` and `virsh` are not ported. `ploop` and `lvm`
are no longer used; `virsh` was written for old libvirt and will be rewritten
as a separate task if it is needed. A config naming them fails to load.

## Every stage's exit status is checked

Raku checks only the exit status of the process that writes to the
destination, so `mysqldump` dying mid-dump still yields a "good" archive.
Here every stage must exit with a status its class allows: `tar` 0 or 1
(1 means a file changed while being read, which is normal on live data and
is logged), `gzip` 0 or 2 (2 is a warning), everything else 0. These
allowances assume GNU tar and gzip, also when `cmd` is overridden.

## Send first, rotate after

Raku deletes excess archives, and on a second run the same day today's
archive, before sending the new one; a failed send leaves the storage one
archive short. Here the archive is written under a temporary name
`.<archive>.partial`, renamed to its real name once every stage has exited
cleanly, and only then is the storage rotated. A failed attempt deletes the
temporary file. A second run the same day replaces today's archive only after
the new one is complete. The storage needs room for one extra archive during
the send.

An archive that cannot be deleted in rotation is reported on stderr and
makes the exit status non-zero; the others are still deleted. The new
archive counts as written, the profile is not retried and its `post` hook
still runs. In Raku a failed delete fails the attempt, which then retries. A
failure to sync the directory after the rename, which only makes the new
name durable, is handled the same way.

## Exit status of `bkp <config>...`

Raku exits 0 even when a config failed to load or a profile ran out of
attempts, so a backup that was not taken raised no alert. Here every config
and profile is still processed, and the exit status is non-zero when any
config failed to load, any profile ran out of attempts or any `post` hook
failed.

## Progress log

Under `BKP_LOG` the progress lines of Raku are kept as they are, with two
more for each attempt: `<config>:<profile> writing <archive>` and
`<config>:<profile> deleting <archive>` for each archive replaced or
rotated away. A stage that exits with a status its class allows besides 0,
such as `tar` with 1, gets a line too.

## Error messages go to stderr

Raku prints `loading failed - skipping`, exception texts and
`BACKUP … FAILED!` to stdout. Here they go to stderr; progress lines under
`BKP_LOG` stay on stdout.

## `src.script`

Raku ignores the script's exit status: a failing script yields empty output,
which surfaces as `Couldn't parse YAML`. Here the script runs under
`bash -o pipefail -c`; a non-zero exit status is a config load error carrying
the status and the script's stderr, and so is empty output.

Each output line replaces `{{ item }}` in the template as text, as in Raku,
but a line may hold only `A-Z a-z 0-9 . _ -`. Anything else would change the
meaning of the generated YAML, so it is a load error naming the line. That
includes an empty line: Raku stops reading at the first one and silently
drops it and every line after it.

A template that does not end in a line break gets one after each copy.
Raku joins the copies as they are, which runs them into one line that does
not parse.

A process the script leaves in the background is killed once the script
exits; if it holds the script's output open, the load fails.

## Config checking

- An unknown key anywhere in a config is a load error; Raku ignores it. The
  one exception is `dst.encoding` of class `ftp`, accepted and ignored.
- Fields that hold a command (`cmd`, `mount.cmd`) must be YAML lists. Raku
  turns a string into a single argv element with spaces inside, which never
  runs; here it is a load error that says to write a list.
- A key written without a value (`cmd:`, `files: ~`) is a load error for
  attributes whose value is checked: lists, commands, paths, `plan`. Raku
  passes an undefined value on and fails later, or not at all. A `plan`
  entry without a value still counts as 0, and a quoted count such as
  `d: "6"` is accepted, as in Raku.
- `dst.plan` entries must not be negative and at least one must be
  positive. Raku builds an archive name with an empty period without a
  positive entry. With a negative one, rotation deletes every archive of
  that period and then fails the attempt.
- Class names are matched case-insensitively, as in Raku.
- `dst.prefix` in a config with several sources is a load error. Raku
  silently replaces it with each source's name, so the value written in the
  config has no effect. With one source it still overrides the file name.
- A key given twice in one mapping is a load error; Raku keeps the last one.
- `dst.dir` and `dst.plan` of class `local` are required, and `dst.mount`
  with `cmd` or `umount` needs `point`; each is a load error. Raku finds
  them missing when it builds the destination for the first attempt, outside
  the attempt's error handling, so the whole run stops there. An empty
  `mount` is ignored, as in Raku.
- `files: []` of `tar` and `dbs: []` of `mysql` are load errors. Raku runs
  `tar` without files, which fails, and `mysqldump` without databases,
  which dumps nothing useful. Leaving `dbs` out still dumps all databases.
- A `suffix` or `add-suffix` that is empty, starts with a dot or contains a
  slash, a space or a control character is a load error. Raku builds
  archive names from it as it is: a slash leads out of the destination
  directory, with an empty main suffix rotation takes any archive of the
  prefix for its own, and a space or a line break breaks the output of
  `bkp list`.
- An entry without a value in a list such as `exclude: [a, ~]` is a load
  error; Raku drops it.
- An empty `rate-limit` of `pv` is a load error; Raku passes it to `pv`,
  which refuses it at every attempt.
- A negative `procs` of `pigz` is a load error. Raku passes it to `pigz`,
  which refuses it at every attempt. `procs: 0` still leaves the number of
  threads to `pigz`.
- A source name or `dst.prefix` that contains a slash is a load error: it
  starts archive names, and Raku would write such an archive outside the
  destination directory. An empty one, `.` and `..` are refused too, as they
  only make confusing names such as `..-2026-11-18-d.tar`.

## Archive names

- A name that looks like an archive but holds a date that does not exist,
  such as `mysql-2026-02-30-y.sql.gz`, is not an archive: it is never
  deleted or counted. Raku fails every attempt while it is the last archive
  of a period it checks, and deletes it in rotation as the oldest of its
  period.
- A second run on the same day after the suffix changed (compression or
  encryption added or removed) writes today's archive under the new suffix,
  then deletes the one of the same date and period under the old suffix,
  with or without `no-rotate`.
  Raku keeps the old name, so new contents end up under a wrong suffix.

## Destination setup failures

Raku mounts the destination outside the attempt's error handling, so a mount
that fails aborts the whole run and the remaining configs are skipped. Here
it is a failed attempt of that profile.

## Retry pauses

Raku pauses `N*180` seconds after failed attempt `N` (counting from 0),
including after the tenth, so a profile that fails every time waits 27
minutes more before `BACKUP … FAILED!`. Here the pause after the last attempt
is dropped; the others are the same.

## Signals

Raku does not handle signals. Here `SIGTERM` or `SIGINT` cancels the current
attempt: its processes are killed, cleanup runs, the temporary archive is
deleted, the remaining profiles and configs are skipped, `bkp: stopped by
SIGTERM` (or `SIGINT`) goes to stderr and the exit status is 128 plus the
signal number, as for a process the signal killed.

## Stage process groups

Raku sends `SIGTERM` to the stage process only and leaves whatever it
started running. Here each stage runs in a process group of its own. When
a stage exits, its group is killed with `SIGKILL`, so nothing it left in the
background keeps writing or holds the next stage's input open, which would
hang the run. When a stage fails or the attempt is cancelled, the groups of
the stages still running get `SIGTERM` and, 5 seconds later, `SIGKILL`.

A stage is thus not in the terminal's foreground group: run by hand, a
stage that reads the terminal itself, such as `ssh` asking about a host
key, is stopped until the attempt is cancelled. Runs from cron have no
terminal.

## Cleanup after every attempt

Raku runs the source's cleanup and `umount` only after a successful send.
Here they run after every attempt, failed ones included.
An `umount` that fails after the archive was written is reported on stderr
and makes the exit status non-zero; the archive counts as written, the
profile is not retried and its `post` hook still runs. Raku dies there and
skips the rest of the run. `umount` runs after a cancelled attempt too
and is given up to 5 minutes.

## `post` hook failures

Raku ignores a failing `post` hook. Here its failure is reported on stderr and
makes the exit status non-zero; the archive is still counted as written and
the profile is not retried.

## Stage stderr

Raku sends stage stderr to `/dev/null` unless `BKP_LOG` is set. Here it is
still copied to stderr under `BKP_LOG`, and in either case the last lines are
kept and go into the error message when the stage fails.

## Permissions

Raku creates archives with the process umask, usually leaving database dumps
world-readable. Here archives, and files `bkp download` writes, are created
`0600` and directories `0700`.

## `openssl` key

Raku passes the key as `-k <key>`, visible to every user in `ps`. Here it is
passed as `-pass fd:3`. The encrypted format is the same: archives decrypt
with `openssl enc -d -<cipher> -k <key>` as before. (The command in the
Raku README lacks `-<cipher>`, and without it `openssl` copies the data
undecrypted.)

`openssl` reads one line of at most 1023 bytes from the descriptor, so a
key that contains a line break or a NUL byte, or is longer than 1023
bytes, is a load error; Raku passes it in argv, where only a NUL byte
fails. An empty key is a load error too: Raku encrypts with an empty
password, which protects nothing.

## FTP

Raku drives the `ncftp` tools. Here FTP is built in: the host needs no
`ncftp`, the password stays out of argv, and connections and stalled
transfers time out instead of hanging.

## CLI

- `bkp` without arguments prints the usage to stderr and exits 2; Raku exits
  0 silently. A first argument that is not a subcommand is a config path, as
  in Raku.
- `bkp list-profiles` prints profile names sorted.
- `bkp --version` is new.
