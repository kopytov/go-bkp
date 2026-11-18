# 04. Классы источников и обработки

## Цель

Перенести классы, которые сводятся к построению argv: источники `cmd`, `tar`,
`mysql`, `redis`; сжатие `gzip`, `pigz`; шифрование `openssl`; ограничение
скорости `pv`. Умолчания, атрибуты и суффиксы такие же, как в Raku.

## Источник

`../raku-bkp/lib/Bkp/Src/{Cmd,Tar,Mysql,Redis}.pm6`,
`../raku-bkp/lib/Bkp/Compress/{Gzip,Pigz}.pm6`,
`../raku-bkp/lib/Bkp/Crypt/Openssl.pm6`, `../raku-bkp/lib/Bkp/Shape/Pv.pm6`,
`../raku-bkp/README.md`. Отличия — разделы «Every stage's exit status is
checked» и «`openssl` key» в `doc/compat.md`.

## Что сделать

Один файл на класс в пакете своего пространства имён. Класс регистрируется
в таблице из задачи 02 и отдаёт стадию для конвейера из задачи 03.

| Класс | Суффикс | `cmd` по умолчанию | Атрибуты → argv | Допустимые коды |
| --- | --- | --- | --- | --- |
| `src cmd` | `suffix`, обязателен | `cmd`, обязателен | — | 0 |
| `src tar` | `tar` | `tar --warning=none --numeric-owner -cf -` | `cwd` (каталог) → `-C <cwd>`; `exclude` → `--exclude=<x>` на каждый; `files` (обязателен) в конец | 0, 1 |
| `src mysql` | `sql` | `mysqldump -ER --single-transaction` | `defaults-file` (файл) → `--defaults-file=<f>`; `dbs` в конец, без них `--all-databases` | 0 |
| `src redis` | `rdb` | — | `dump` (файл, обязателен) — первое звено-файл | — |
| `compress gzip` | `+.gz` (`add-suffix`) | `gzip -c` | `level` 1–9 → `-<level>` | 0, 2 |
| `compress pigz` | `+.gz` (`add-suffix`) | `pigz -c` | `level` → `-<level>`; `procs` → `-p <n>` | 0 (проверить по `man pigz`, есть ли у него код-предупреждение) |
| `crypt openssl` | `+.enc` (`add-suffix`) | `openssl enc` | `cipher` (`^[a-z0-9-]+$`, по умолчанию `aes-256-cbc`) → `-<cipher>`; `key` (обязателен) → `-pass fd:3`, ключ пишется в пайп на дескрипторе 3 | 0 |
| `shape pv` | без изменений | `pv -q` | `rate-limit` (по умолчанию `1m`) → `-L <rate>` | 0 |

`cmd` везде можно переопределить списком. Суффикс промежуточной стадии —
суффикс источника, точка и `add-suffix`.

## Тесты

- argv каждого класса: с умолчаниями, со всеми атрибутами, с переопределённым
  `cmd`; ошибки обязательных атрибутов и проверок;
- суффиксы цепочек (`tar.gz.enc`, `sql.gz`, `rdb`);
- `openssl`: ключ не попадает в argv. Совместимость проверить на настоящем
  `openssl` (если его нет — `t.Skip`): зашифровать по-новому и расшифровать
  командой из README Raku-версии `openssl enc -d -k <key>`, и наоборот;
- `gzip`: настоящий `gzip` в цепочке с `tar` на `t.TempDir()`, результат
  разворачивается обратно (если утилит нет — `t.Skip`).

## Готово, когда

`scripts/test.sh` проходит, ревьюеры не нашли blocking, файл задачи
удалён.
