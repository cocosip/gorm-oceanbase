# Live tenant acceptance tool

`cmd/gorm-obtest` is a standalone command that connects to OceanBase through this repository's GORM
Dialector. One JSON configuration can select MySQL, Oracle, or all targets in sequence. The existing
`tests/integration` suite remains available; this command adds a type matrix, field metadata,
independent object cleanup, and JSON reports that can be saved.

Live acceptance tests ran against OceanBase **4.4.2.2** in both modes on 2026-10-24. All applicable
cases in the supported MySQL and Oracle column matrix passed. See the live test results below.

## Offline inspection and validation

Run from the repository root. No environment variables or database connections are needed:

```powershell
go run ./cmd/gorm-obtest -list

go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -check
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -mode oracle -check
go build -o bin/gorm-obtest.exe ./cmd/gorm-obtest
```

## Connection configuration and execution

Use a test schema with permission to create tables, indexes, constraints, and Oracle sequences/types.
The tool creates test objects with random prefixes, writes test data, and cleans up objects it
successfully created after each case. It does not truncate business tables or create databases.
Forced termination or network loss can leave objects behind; their names appear in report `objects`.

Configuration references environment variable names only, without embedding passwords or DSNs.
The example has four targets covering plain GORM execution and cached `PrepareStmt` execution in both
modes. `prepare_stmt` controls GORM's statement cache; it is separate from the underlying driver's
client-side parameter interpolation setting. OBJECT values require server-side preparation.
Statements remain cached within each case and are synchronously released at case completion, then
released again after object cleanup. This prevents random table names from exhausting server prepared
statement handles across the suite; the tool does not change server limits.

```powershell
$env:OB_MYSQL_DSN = 'oceanbase://user:password@127.0.0.1:2881/test_db?mode=mysql'
$env:OB_ORACLE_DSN = 'oceanbase://user:password@127.0.0.1:2881/TEST_SCHEMA?mode=oracle'

# Run all configured targets in sequence.
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -report report-all.json

# Select one mode or one target.
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -mode mysql -report report-mysql.json
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -target oracle -report report-oracle.json
```

Use a new report path each time; the tool refuses to overwrite an existing file. Remove unwanted
targets or set a MySQL target's `mysql_profile` to 5 (which truncates TIME fractional seconds).
Omitting it selects 8; target configuration overrides the DSN MySQL profile. Oracle targets must not
set a MySQL profile. An explicit DSN mode must match the target mode. The tool parses times in UTC to
provide a fixed baseline for values without time zones. A missing DSN is a configuration error,
never a successful live test run.

`timeout` is the per-case budget; `cleanup_timeout` is a separate budget for each cleanup action.
Cleanup is attempted even after cancellation. Connection and network read timeouts do not exceed
the case budget. `-check` validates only JSON structure, modes, targets, and case names. It does not
read DSN environment variables or validate tenant permissions, networking, credentials, or server capabilities.

Add a `cases` array to the configuration to run selected cases, for example:

```json
"cases": ["types.uint64", "types.oracle_number", "schema.fields", "schema.migration"]
```

## Coverage

Use `-list` for exact names and applicable modes. There are currently 104 independent cases:

| Area | Coverage |
| --- | --- |
| Go numeric types | bool; int/int8/int16/int32/int64 and uint/uint8/uint16/uint32/uint64; minimum, zero, and maximum values; float32/float64 and large values |
| Exact decimals | DECIMAL/NUMBER(38,9) using `types.Number` to avoid float64 precision loss |
| Strings and binary | Ordinary, length-limited, and long strings; Unicode; SQL empty-string differences; raw bytes, length-limited bytes, and LOB payloads |
| NULL | Go pointers and sql.NullBool/Int16/Int32/Int64/Float64/String/Time, distinguishing NULL from valid zero values |
| Time | time.Time; second/millisecond/microsecond precision; DATE/DATETIME/TIMESTAMP, MySQL TIME/YEAR, Oracle TIMESTAMP(9); string TIME values including negative durations and durations over 24 hours |
| MySQL-specific types | CHAR, TEXT/MEDIUMTEXT/LONGTEXT, VARBINARY/BLOB, ENUM, SET, JSON |
| Oracle-specific types | NUMBER, CHAR/NCHAR/NVARCHAR2, RAW/BLOB/CLOB, INTERVAL YM/DS, time zone/local time zone timestamps, ROWID, and OBJECT columns |
| Fields and metadata | Actual database type, length, precision/scale, nullability, defaults; primary key/auto-increment/comment metadata exposed by GORM; NOT NULL, ignored fields, and custom/explicit-case column names |
| Migrations and constraints | Incremental/repeated AutoMigrate, existing data preservation, column rename/drop, composite primary keys, composite unique indexes and rename/drop, foreign keys, CHECK, MySQL column comments |
| GORM behavior | CRUD, explicit/generated keys, batch/default-only inserts, maps and explicit NULL, zero-value updates, pagination, Upsert/DoNothing, error translation, transactions/savepoints, hooks, timestamps, soft deletion, four association types/Preload/Join/FullSaveAssociations, Oracle sequences |

Each scalar case verifies written/read-back values and `ColumnTypes`, beyond table creation alone.
Field types, nullability, explicit lengths, and high-precision decimal metadata have separate assertions.
Missing field metadata is omitted from JSON, meaning GORM did not provide it; it cannot be claimed as
validated. Floating-point tests use ordinary, exactly representable values and do not claim exhaustive
coverage of floating-point values or server SQL types.

Oracle treats empty VARCHAR2 values as NULL. ROWID values are read from the server, never fabricated.
OBJECT cases create actual named types and read/write them through GORM fields. Native NCLOB and
nested-table columns are outside the supported column matrix: official Connector/J and the Go driver
both confirm server rejection on the tested 4.4.2.2 tenant. Their old case names are rejected during
configuration validation. See the [independent Java comparison](driverissues/oracle-ddl-support.md).
For retained cases, permission, prepared statement/LOB/type descriptor, and unexpected server errors
remain failures, without automatic fallback or skips. PL/SQL arrays, cursors, XA, spatial types, and
other driver-specific APIs are outside this GORM acceptance tool's scope.

## Reports and exit codes

Reports include the run ID, Go/platform, target name, probed server version, configured mode and
execution method, MySQL profile, case durations, field/index metadata, created objects, failures,
and cleanup errors. After connecting, the tool reads the actual tenant compatibility mode and checks
it against the configuration. GORM SQL logging is disabled. Reports exclude DSNs, passwords, SQL
parameters, and unprocessed driver error messages. Server errors retain their codes; case names locate
the failure scope. Scalar cases also record the last step: table creation, metadata, write, read, or comparison.

| Status | Meaning |
| --- | --- |
| pass | Execution and assertions passed, with no object cleanup failure |
| fail | Connection, DDL, read/write, assertion, panic, or cleanup failure |
| skip | Not executed because a prerequisite failed; does not count as a pass |
| not_applicable | The case belongs to the other mode and is explicitly inapplicable |

Exit codes: `0` means all applicable cases passed; `1` means failures occurred; `2` means a configuration,
argument, or report path error; `3` means no cases passed or some cases were skipped. With `go run`,
the Go launcher may map nonzero child exit codes to 1. CI requiring exact exit codes should use the
compiled binary.

Pass counts prove only the target/version/configuration tested in that run. They do not establish
complete compatibility with other versions or deployments.

## 2026-10-24 supported-scope verification

Official Connector/J 2.4.14 independently reproduced the Go driver's NCLOB (902), nested-table
column (600/-4007), and NESTED TABLE storage-clause (900) rejections in both text and native
server-prepared execution on the same 4.4.2.2 tenant. CLOB, NVARCHAR2 and named collection type
creation succeeded. See the [independent comparison results](driverissues/oracle-ddl-support.md).

The native NCLOB and nested-table column definitions were unsuitable for this supported target.
Their two cases and unused collection-column implementation were removed, reducing the regular
matrix from 106 to 104 cases. Old configurations selecting them are rejected before connection.
The regular tool no longer sends those definitions. Only the results of the independent Java
diagnostic experiment are retained. Existing failure reports below are unchanged historical data.

Fresh run: published go-oceanbase v0.1.4, OceanBase 4.4.2.2, GORM v1.31.2,
Go 1.27.2, Windows amd64; ID `0ad4c1c7cea6`; duration 1m34.1663097s.

| Target | MySQL profile | pass | fail | skip | not_applicable |
| --- | --- | --- | --- | --- | --- |
| mysql | 8 | 87 | 0 | 0 | 17 |
| mysql_prepared | 8 | 87 | 0 | 0 | 17 |
| oracle | — | 88 | 0 | 0 | 16 |
| oracle_prepared | — | 88 | 0 | 0 | 16 |
| **Total** | | **350** | **0** | **0** | **66** |

The command exits 0. There are no cleanup errors and an independent catalog check found zero
owned objects remaining in either schema; the Java probe also verified zero remaining objects.
Build, uncached unit tests, vet, integration compilation and both lint selections pass. A regression
first reproduced acceptance of the unsupported case names, then passed after their removal.
No library mapping or go-oceanbase implementation was changed for this scope correction.
MySQL profile 5, the full older integration suite, older servers and OBProxy were not rerun.

Ignored evidence: `.cache/live-v014-supported-20261024.json`,
`.cache/live-v014-supported-matrix-20261024.log`,
`.cache/live-v014-supported-cleanup-20261024.log`, and
`.cache/jdbc-compare/oracle-ddl-20261024.log`.

## 2026-10-24 live test results before scope correction

Baseline: published `go-oceanbase v0.1.4`, OceanBase 4.4.2.2, GORM v1.31.2,
Go 1.27.2, Windows amd64. The executable's module information confirms v0.1.4;
the earlier local-driver workspace was disabled. Run ID: `6efcd3844af7`;
duration: 1m36.4737065s.

| Target | MySQL profile | pass | fail | skip | not_applicable |
| --- | --- | --- | --- | --- | --- |
| mysql | 8 | 87 | 0 | 0 | 19 |
| mysql_prepared | 8 | 87 | 0 | 0 | 19 |
| oracle | — | 88 | 2 | 0 | 16 |
| oracle_prepared | — | 88 | 2 | 0 | 16 |
| **Total** | | **350** | **4** | **0** | **70** |

All float32, float64, nullable-float and pointer variants passed in both Oracle
targets. The separate GORM-free `TestOracleBinaryDoubleRange` passed for ordinary
and explicit prepared execution, including 1.5, 1.1, 1e200, positive/negative
MaxFloat64 and smallest nonzero float64. An independent unparameterized
`SELECT TO_CHAR(V)` probe verified that the four previously corrupted values
parse back to the exact input float64 in both execution paths (8/8 comparisons).
`TestOracleMigrationColumnChanges` also passed, including its mixed-parameter
INSERT, existing-data preservation, and constraint changes. These focused tests
are separate from the matrix counts above.

The remaining failures occur at `create_table`: `types.oracle_nclob` reports
server code 902 and `types.oracle_collection` reports code 600, once per Oracle
target. These are the previously reproduced server DDL capability limits;
they were not converted to skips. The matrix therefore exits with code 1.

There were no cleanup errors. An independent catalog inventory found zero objects
owned by this matrix run in either schema and no remaining focused migration
table. The direct stored-value probe also verified its own table cleanup.
Build, unit tests, vet, integration compilation, module checks, and ordinary plus
integration-tagged golangci-lint passed. The full older integration suite and
MySQL profile 5 matrix were not rerun in this round.

Ignored local evidence: `.cache/live-v014-20261024.json`,
`.cache/live-v014-regressions-20261024.log`,
`.cache/live-v014-stored-values-20261024.log`, and
`.cache/live-v014-cleanup-20261024.log`. See the
[binary-double diagnosis](driverissues/oracle-binary-double.md) for protocol details.

## 2026-10-23 live test results

Baseline: OceanBase 4.4.2.2, `go-oceanbase v0.1.2`, GORM v1.31.2, Go 1.27.2, Windows amd64.
Main matrix run ID: `c211424018de`; supplemental matrix: `b86cfeb78b1f`.

| Target | MySQL profile | pass | fail | skip | not_applicable |
| --- | --- | --- | --- | --- | --- |
| mysql | 8 | 87 | 0 | 0 | 19 |
| mysql_prepared | 8 | 87 | 0 | 0 | 19 |
| oracle | — | 86 | 4 | 0 | 16 |
| oracle_prepared | — | 86 | 4 | 0 | 16 |
| **Main matrix total** | | **346** | **8** | **0** | **70** |
| mysql_profile5 (supplemental matrix) | 5 | 87 | 0 | 0 | 19 |
| mysql_prepared_profile5 (supplemental matrix) | 5 | 87 | 0 | 0 | 19 |

The supplemental matrix also ran all 106 cases, checking profile 5 TIME fractional truncation,
negative/long durations, and DATE/DATETIME/TIMESTAMP round trips. All eight main-matrix failures remain
recorded; none were converted to skips or ignored.

That validation round fixed dropped rows in MySQL batch INSERTs with no columns, and corrected the
tool's TIME types, INTERVAL positive-sign comparison, and prepared statement cache lifecycle.
The final matrix had no cleanup errors. Two owned tables left by the initial handle-exhaustion run
were removed using their report names; an independent object inventory found no remaining objects.
The existing `tests/integration` suite, which uses fixed table names, was compile-checked only in that
round. Counts above come from the standalone tool's random objects; the older integration suite and
separate driver reproductions are excluded.

Complete local JSON results are stored in the ignored `.cache/` directory as `live-v012-20261023.json`
and `live-profile5-v012-20261023.json`. Neither contains connection credentials.
