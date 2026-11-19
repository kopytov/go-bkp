# bkp

`bkp` makes backups and rotates them by a plan of yearly, monthly, weekly
and daily copies.

Data goes from a source straight to a destination, with no temporary
copies:

```text
src -> [compress] -> [crypt] -> [shape] -> dst
```

Each stage is an external tool: `tar`, `mysqldump`, `gzip`, `openssl`,
`pv`. Only the tools your configs use must be installed. FTP is built in.
`bash` runs `src.script` and `post`. `local` with `mount` needs
`mountpoint`, `mount` and `umount`.

Configs hold passwords and keys: make them readable by root only (`0600`).

Run it from cron or a systemd timer with a list of configs:

```sh
bkp /usr/local/etc/bkp/*.yaml
```

## Example

Back up `/var/www/htdocs` and the MySQL database `wordpress` to an FTP
server. Keep 6 monthly, 3 weekly and 6 daily copies:

```yaml
src:
  files:
    class: tar
    cwd: /var/www
    files:
      - htdocs
  database:
    class: mysql
    dbs:
      - wordpress
compress:
  class: gzip
dst:
  class: ftp
  hostname: ftp.example.com
  username: bkpuser
  password: bkpsecret
  path: /backups
  plan:
    m: 6
    w: 3
    d: 6
```

This makes two archives a run:

- `files-YYYY-MM-DD-P.tar.gz`
- `database-YYYY-MM-DD-P.sql.gz`

`P` is the period: `y`, `m`, `w` or `d`.

## Config

A config is a YAML file with these sections:

| Section    | Required | What it does                        |
|------------|----------|-------------------------------------|
| `src`      | yes      | where the data comes from           |
| `compress` | no       | compresses the stream               |
| `crypt`    | no       | encrypts the stream                 |
| `shape`    | no       | limits the speed of the stream      |
| `dst`      | yes      | where archives go and how many stay |
| `post`     | no       | shell command run after the archive |

Each section except `post` has a `class` and the attributes of that class.
Class names are case-insensitive.

The config is checked before anything runs. These are errors:

- an unknown key, or a key given twice;
- a key without a value, such as `cmd:`;
- a command written as a string: write `cmd` and `mount.cmd` as lists.

## Profiles

A profile makes one archive. The `src` section decides how many profiles a
config has.

### One source

```yaml
src:
  class: tar
  cwd: /var/www
  files:
    - htdocs
```

The profile is named after the config file: `www.yaml` gives `www`. A
file without `.yml` or `.yaml` gives its name in lower case. The
archives start with this name. Set `dst.prefix` to use another name.

### Several sources

```yaml
src:
  site:
    class: tar
    cwd: /var/www
    files:
      - htdocs
  database:
    class: mysql
    dbs:
      - wordpress
```

Each source is a profile with its own name. All profiles share the other
sections. `dst.prefix` is not allowed here.

### Sources from a script

```yaml
src:
  script: >-
    mysql -Nse 'SHOW DATABASES'
    | grep -Ev '^(information_schema|performance_schema|sys)$'
  template: |
    mysql-{{ item }}:
      class: mysql
      dbs:
        - {{ item }}
```

1. `bkp` runs `script` with `bash -o pipefail -c`.
2. Each output line replaces `{{ item }}` in a copy of `template`.
3. The copies are read as the list of sources.

The config fails to load when the script:

- exits non-zero;
- prints nothing;
- prints a line with characters other than `A-Z a-z 0-9 . _ -`.

## src

Every source takes `suffix`, the file extension of its data. Every source
except `redis` takes `cmd`, the command as a list.

### tar

Archives files with GNU tar.

```yaml
src:
  class: tar
  cwd: /var/www
  files:
    - site1.com
    - site2.com
  exclude:
    - site1.com/cache
```

| Attribute | Required | Default                                          |
|-----------|----------|--------------------------------------------------|
| `files`   | yes      |                                                  |
| `cwd`     | no       | current directory                                |
| `exclude` | no       |                                                  |
| `cmd`     | no       | `[tar, --warning=none, --numeric-owner, -cf, -]` |
| `suffix`  | no       | `tar`                                            |

Exit status 1 of tar is not a failure: it means a file changed while it was
read. Any other non-zero status of any stage fails the attempt. The only
other exception is status 2 of `gzip`, a warning.

`files` must not be empty.

### mysql

Dumps MySQL databases with `mysqldump`.

```yaml
src:
  class: mysql
  dbs:
    - db1
    - db2
```

| Attribute       | Required | Default                                  |
|-----------------|----------|------------------------------------------|
| `dbs`           | no       | all databases (`--all-databases`)        |
| `defaults-file` | no       |                                          |
| `cmd`           | no       | `[mysqldump, -ER, --single-transaction]` |
| `suffix`        | no       | `sql`                                    |

`dbs` must not be empty: leave it out to dump all databases.

One dump of all databases is hard to restore one database from. To dump
each database to its own archive, make a source for each, or use a
[script](#sources-from-a-script).

To run `mysqldump` in a container:

```yaml
cmd: [docker, exec, -u, root, mysql, mysqldump, -ER, --single-transaction]
```

### redis

Copies a Redis dump file.

```yaml
src:
  class: redis
  dump: /var/lib/redis/dump.rdb
```

| Attribute | Required | Default |
|-----------|----------|---------|
| `dump`    | yes      |         |
| `suffix`  | no       | `rdb`   |

### cmd

Takes the output of any command.

```yaml
src:
  class: cmd
  cmd: [pg_dump, -Fc, mydb]
  suffix: pgdump
```

| Attribute | Required |
|-----------|----------|
| `cmd`     | yes      |
| `suffix`  | yes      |

The command must write the data to stdout.

## compress

### gzip

```yaml
compress:
  class: gzip
  level: 6
```

| Attribute    | Required | Default             |
|--------------|----------|---------------------|
| `level`      | no       | 1-9; tool's default |
| `cmd`        | no       | `[gzip, -c]`        |
| `add-suffix` | no       | `gz`                |

### pigz

Like `gzip`, but uses several CPU cores.

```yaml
compress:
  class: pigz
  procs: 4
```

| Attribute    | Required | Default             |
|--------------|----------|---------------------|
| `procs`      | no       | tool's default      |
| `level`      | no       | 1-9; tool's default |
| `cmd`        | no       | `[pigz, -c]`        |
| `add-suffix` | no       | `gz`                |

## crypt

### openssl

Encrypts the stream with `openssl enc`.

```yaml
crypt:
  class: openssl
  key: s3creT
```

| Attribute    | Required | Default          |
|--------------|----------|------------------|
| `key`        | yes      |                  |
| `cipher`     | no       | `aes-256-cbc`    |
| `cmd`        | no       | `[openssl, enc]` |
| `add-suffix` | no       | `enc`            |

The key goes to `openssl` through a file descriptor, not the command line.

To decrypt:

```sh
openssl enc -d -aes-256-cbc -k 's3creT' -in archive.tar.gz.enc -out archive.tar.gz
```

Use the cipher from the config in place of `aes-256-cbc`.

## shape

### pv

Limits the speed of the stream with `pv`.

```yaml
shape:
  class: pv
  rate-limit: 5m
```

| Attribute    | Required | Default    |
|--------------|----------|------------|
| `rate-limit` | no       | `1m`       |
| `cmd`        | no       | `[pv, -q]` |

`rate-limit` is in bytes a second. Suffixes `k`, `m`, `g` and `t` mean
kilobytes, megabytes and so on.

## dst

Every destination takes these attributes:

| Attribute   | Required | What it does                                 |
|-------------|----------|----------------------------------------------|
| `plan`      | yes      | how many archives of each period to keep     |
| `no-rotate` | no       | `true`: do not delete old archives           |
| `prefix`    | no       | archive name prefix, one-source configs only |

### Plan

```yaml
plan:
  y: 2
  m: 6
  w: 4
  d: 6
```

This keeps 2 yearly, 6 monthly, 4 weekly and 6 daily archives. Leave out
or set to 0 the periods you do not need. At least one must be positive.
Archives of a period not in the plan are never deleted: remove them by
hand.

Each run writes one archive per profile and picks its period:

1. If an archive was already written today, the new one replaces it and
   takes its period.
2. Otherwise, the longest period that is due: it has no archive yet, or its
   last one is old enough (365, 30, 7 or 1 days).
3. If none is due, the shortest period of the plan.

Then the oldest archives of each period over the plan are deleted.

### Archive names

```text
<prefix>-YYYY-MM-DD-<period>.<suffix>
```

- `prefix`: the profile name, or `dst.prefix`.
- `suffix`: the source suffix plus the suffix of each filter, such as
  `tar.gz.enc`.

The archive is written as `.<name>.partial` and renamed when every stage
has finished without errors. Old archives are deleted only after that.
The destination needs room for one extra archive.

### local

Writes archives to a local directory.

```yaml
dst:
  class: local
  dir: /mnt/bkp/backups
  mount:
    point: /mnt/bkp
    cmd: [sshfs, bkp@backup.example.com:/srv/bkp, /mnt/bkp]
    umount: true
  plan:
    d: 7
```

| Attribute      | Required          | What it does                           |
|----------------|-------------------|----------------------------------------|
| `dir`          | yes               | directory for archives; must exist     |
| `mount.point`  | yes, with `mount` | mount point to check before writing    |
| `mount.cmd`    | no                | mount command; default `mount <point>` |
| `mount.umount` | no                | `true`: unmount after each attempt     |

`mount.cmd` runs only when `mount.point` is not mounted. Without
`mount.cmd`, the mount point must be in `/etc/fstab`.

Archives are created with mode `0600`.

### ftp

Uploads archives to an FTP server.

This is plain FTP, without TLS: the password and the data are sent
unencrypted. Use `crypt` to protect the archives.

```yaml
dst:
  class: ftp
  hostname: ftp.example.com
  port: 21
  username: bkp
  password: s3crEt
  path: /backups
  plan:
    d: 7
```

| Attribute  | Required | Default                        |
|------------|----------|--------------------------------|
| `hostname` | yes      |                                |
| `port`     | no       | `21`                           |
| `username` | no       | `anonymous`                    |
| `password` | no       | `test@test.com`                |
| `path`     | no       | short host name of this server |

`path` is relative to the login directory, even with a leading `/`.

Timeouts:

- 30 seconds to connect;
- 5 minutes without progress during a transfer.

`encoding` is accepted for old configs and ignored.

## post

A shell command run after the archive is written:

```yaml
post: curl -fsS https://hc-ping.com/<uuid> >/dev/null
```

It runs with `bash -o pipefail -c`. Its output goes to `bkp`'s stdout and
stderr. If it fails, the error goes to stderr and the exit status is
non-zero. It is not retried: the archive is already written.

## Commands

```text
bkp <config>...
bkp list <config> [--bytes] [--profile <name>]
bkp list-profiles <config>
bkp download <config> <archive> [--profile <name>] [--filename <path>|-] [--force]
bkp --version
bkp --help
```

Options can go anywhere on the command line, as `--opt value` or
`--opt=value`.

### Backup

Backs up every profile of every config.

- A profile gets up to 10 attempts.
- Once the archive is written, the profile is not retried. A failed
  rotation, `umount` or `post` after that only makes the exit status 1.
- After failed attempt N (from 0), `bkp` waits N × 3 minutes.
- After each attempt, `bkp` unmounts what it mounted, if `mount.umount`
  is set.
- A broken config or profile does not stop the others.

### bkp list

Lists the archives of a config, one per line: name and size. Profiles
and archives are sorted by name.
Other files in the destination are not shown.

```text
$ bkp list /usr/local/etc/bkp/www.yaml
database:

database-2026-11-01-m.sql.gz 85.3M
database-2026-11-18-d.sql.gz 86.0M

files:

files-2026-11-01-m.tar.gz 1.2G
files-2026-11-18-d.tar.gz 1.1G
```

- `--bytes` shows sizes in bytes.
- `--profile <name>` lists one profile, without the header.
- With one profile, there is no header.

### bkp list-profiles

Prints the profile names of a config, one per line.

### bkp download

Copies an archive from the destination.

```sh
bkp download /usr/local/etc/bkp/www.yaml files-2026-11-18-d.tar.gz --profile files
```

- `--profile <name>` is required when the config has several profiles.
- `--filename <path>` sets where to write; `-` means stdout. By default the
  archive goes to the current directory under its own name.
- `--force` replaces an existing file.

Only archives that `bkp list` shows can be downloaded: names that match
the [archive name](#archive-names) of the profile. The file is written
under a temporary name and renamed when complete.

## Output

| What             | Where  | When                    |
|------------------|--------|-------------------------|
| progress lines   | stdout | only with `BKP_LOG` set |
| errors           | stderr | always                  |
| stderr of stages | stderr | only with `BKP_LOG` set |

A progress line looks like this:

```text
2026-11-19T00:05:01+03:00 bkp: [1234] www.yaml:files started [0]
```

When a stage fails, the error includes the last lines of its stderr, with
or without `BKP_LOG`.

After a profile runs out of attempts, this line goes to stderr:

```text
BACKUP www.yaml:files FAILED!
```

## Exit status

| Status | Meaning                                                            |
|--------|--------------------------------------------------------------------|
| 0      | everything succeeded                                               |
| 1      | an error: a config did not load, a profile failed, a `post` failed |
| 2      | wrong command line, or no arguments                                |
| 128+N  | backup stopped by signal N (`SIGINT` 130, `SIGTERM` 143)           |

`bkp <config>...` finishes all configs before it exits with 1. `list`,
`list-profiles` and `download` exit with 1 on any error, a signal included.

On `SIGTERM` or `SIGINT`, `bkp` stops the current attempt, kills its
processes, deletes the partial archive and skips the rest.
