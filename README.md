# gorm-oceanbase

An OceanBase database driver for [GORM](https://gorm.io), supporting **MySQL compatibility mode** and
**Oracle compatibility mode**. It uses [go-oceanbase](https://github.com/cocosip/go-oceanbase)
(the `database/sql` driver), with a single `Dialector` selecting the mode through a DSN or configuration.

> Tested against OceanBase 4.4.2.2 on 2026-10-24 with go-oceanbase v0.1.4: all applicable cases in
> the supported column matrix passed for plain/prepared MySQL profile 8 and Oracle targets.
> Native NCLOB and nested-table columns are excluded after independent Connector/J confirmation
> of server rejection. See the
> [live test results](tests/acceptance/README.md#2026-10-24-supported-scope-verification).
> Supported scope follows the acceptance status in [docs/development.md](docs/development.md).
> Features without acceptance validation are not claimed to be production-ready.

## Installation

```bash
go get github.com/cocosip/gorm-oceanbase
```

The development baseline is `gorm.io/gorm` v1.31.2. Support for earlier GORM versions requires a separate
version matrix before it can be claimed.

## Quick start

```go
import (
    "github.com/cocosip/gorm-oceanbase"
    "gorm.io/gorm"
)

db, err := gorm.Open(oceanbase.Open("oceanbase://user:pass@127.0.0.1:2881/db?mode=mysql"), &gorm.Config{})
```

Structured configuration (use either this or a DSN):

```go
import ob "github.com/cocosip/go-oceanbase"

cfg, err := ob.ParseDSN("oceanbase://user:pass@127.0.0.1:2881/db")
if err != nil {
    return err
}

db, err := gorm.Open(oceanbase.New(oceanbase.Config{
    DSNConfig: &cfg,
    Mode:      ob.ModeOracle,
}), &gorm.Config{})
```

`Open`/`New` do not connect to the database. `Initialize` returns DSN and configuration errors, so the
Dialector can safely be constructed during application configuration.

## Mode selection

Precedence is as follows; a zero value means "unspecified":

```
Config.Mode (nonzero) > explicit DSN mode / nonzero DSNConfig.Mode > MySQL default
```

- Use the DSN parameter `mode=auto` to detect the tenant compatibility mode. Detection reads
  `ob_compatibility_mode` on a temporary connection and accepts only non-NULL `MYSQL`/`ORACLE` values.
  Unknown values, NULL, timeouts, and permission errors all cause initialization to fail.
- A GORM `DB` instance uses exactly one compatibility mode; tenants with mixed modes are unsupported.
- An external `Conn` is not probed and requires an explicit, concrete `Mode`.

## Time handling and MySQL profiles

- **Internal connections** (DSN / DSNConfig): the driver enables `Session.ParseTime` so ordinary GORM
  `time.Time`/`*time.Time` fields can be scanned correctly. An explicit DSN `parseTime=false` causes a
  configuration error rather than being silently rewritten.
- **External connections** (`Config.Conn`): callers must ensure `ParseTime=true`. This driver neither
  modifies nor closes the pool. Ownership remains with the caller, including when initialization fails.
- **MySQL profiles**: `CompatibleMysqlVersion` accepts 5 or 8 and controls MySQL client result and
  conversion semantics. Precedence is `Config.CompatibleMysqlVersion` > explicit DSN
  `compatibleMysqlVersion` > nonzero `DSNConfig` value > default **8**. Profile 5 truncates fractional
  seconds in TIME results; DATETIME/TIMESTAMP behavior must be verified separately for each profile.
- Oracle mode does not inject a MySQL profile.
- MySQL `TIME` is a duration that can be negative or exceed 24 hours. Use a `string` field with an
  explicit `type:time(6)` tag. The underlying driver returns strings even with ParseTime enabled;
  these values cannot be treated as ordinary dates in `time.Time`.

## String and time type defaults

MySQL mode follows `gorm.io/driver/mysql` v1.6:

- An ordinary `string` field without `size` uses `longtext`. Primary keys, fields with defaults, and
  fields with `INDEX`/`UNIQUE`/`UNIQUEINDEX` tags use `varchar(191)` (for utf8mb4 index length).
  Setting `Config.DefaultStringSize` supplies a common default length.
- Lengths from 65536 through 2²⁴ use `mediumtext`; larger lengths use `longtext`; other positive lengths
  use `varchar(n)`.
- `datetime` defaults to precision 3 (milliseconds). Adjust it with `DefaultDatetimePrecision` or
  disable it with `DisableDatetimePrecision`.
- The default `NowFunc` is installed after mode resolution: millisecond truncation for MySQL and
  microsecond truncation for Oracle. A caller-supplied `NowFunc` is preserved.

## Oracle compatibility mode

- **Identifiers**: unquoted identifiers are uppercased to match OB Oracle folding rules. Explicitly
  quoted names such as `gorm:"column:\"Foo\""` retain their case. Query result columns are mapped back
  to model DBName values, including custom names, explicit case, and joins; caller `MapColumns`
  mappings take precedence. For `Raw(...).Scan(...)` without a model, use SQL aliases or `MapColumns`
  for custom column mappings.
- **Locks**: Oracle has no `FOR SHARE` syntax. `clause.Locking{Strength: "SHARE"}` becomes `FOR UPDATE`,
  changing the lock from shared to exclusive. `UPDATE` strength and `NOWAIT`/`SKIP LOCKED` options are
  emitted unchanged.
- **Type mappings**: `Bool` → `NUMBER(1)`; `Int/Uint` → `NUMBER(p,0)` (int64: 19, uint64: 20);
  `Float` → `NUMBER(p,s)` / `BINARY_FLOAT` / `BINARY_DOUBLE`; `String` → `VARCHAR2` (default 512,
  `CLOB` above 4000); `Time` → `TIMESTAMP` (default precision 6, with explicit type tags for time zone
  variants); `Bytes` → `RAW` (≤2000) / `BLOB`. `types.Object` requires an explicit
  `gorm:"type:ACTUAL_TYPE_NAME"` tag and server-prepared reads. Native NCLOB and nested-table columns
  are outside the supported matrix on 4.4.2.2; driver-level PL/SQL Array binding is separate from
  GORM column storage. Explicit type tags pass through unchanged, without automatic substitution.
- **Generated primary keys**: auto-increment fields use `NUMBER(p,0) GENERATED BY DEFAULT AS IDENTITY`.
  `BY DEFAULT` does not generate an ID for explicit NULL. The candidate minimum IDENTITY version is
  4.2.1; unknown or earlier versions return `ErrIdentityUnsupported` before table DDL. Use explicit
  keys or sequences instead (`OracleSequenceMigrator.CreateSequence/HasSequence`, with
  `SELECT my_seq.NEXTVAL FROM dual` inside a transaction). Inserts omit zero-valued keys and retrieve
  them through `INSERT ... RETURNING ... INTO ?` to populate the model. Batch IDs are retrieved one
  row at a time; contiguous IDs are never inferred and `LastInsertId` is not used.
- **Upsert (MERGE)**: `clause.OnConflict` becomes `MERGE INTO ... USING (SELECT ? ... FROM dual)`.
  The conflict target is explicit `Columns` or the model's complete primary key. Non-primary-key
  targets are checked against database unique indexes before execution and must match a complete
  unique key. DryRun uses model `unique`/`uniqueIndex` declarations and rejects targets whose
  uniqueness cannot be proved without a model. `UpdateAll` excludes ON columns. Updates to ON columns
  and conditional forms (`Where`/`TargetWhere`/`OnConstraint`) return `ErrUpsertUnsupported` before
  execution without silently discarding semantics. Upserts matching a non-primary unique key while
  still requiring a generated primary key are rejected; preallocate the key first. MERGE on a
  non-primary target does not populate the existing row's primary key. Each callback validates its
  entire batch before execution. Unique conflicts use error translation, with no automatic retry.

## Error translation

With `gorm.Config{TranslateError: true}`, server codes from driver `*dberrors.Error` values are mapped
to GORM errors. The two code tables are isolated by the resolved mode:

| Mode | Error codes | GORM error |
| --- | --- | --- |
| MySQL | 1062 | `gorm.ErrDuplicatedKey` |
| MySQL | 1451 / 1452 | `gorm.ErrForeignKeyViolated` |
| Oracle | 1 (ORA-00001) | `gorm.ErrDuplicatedKey` |
| Oracle | 2291 / 2292 | `gorm.ErrForeignKeyViolated` |

Unmatched errors are returned unchanged; no fuzzy matching is used.

## Documentation

- [Live tenant acceptance tool](tests/acceptance/README.md): `cmd/gorm-obtest`, with configurable
  MySQL/Oracle targets, type coverage, field metadata, migrations, GORM behavior, and live test counts
- [Design](docs/design.md): architecture, configuration, dual-mode behavior, error translation, and test strategy
- [Development plan and progress](docs/development.md): tasks, dependencies, and acceptance status

## License

MIT. See [LICENSE](LICENSE).
