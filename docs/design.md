# gorm-oceanbase design

This document defines the planned GORM Dialector built on [go-oceanbase](https://github.com/cocosip/go-oceanbase)
(the `database/sql` driver), targeting OceanBase **MySQL compatibility mode** and **Oracle compatibility mode**.
See [development.md](development.md) for implementation and acceptance status.

- Module path: `github.com/cocosip/gorm-oceanbase`; package name: `oceanbase`
- Development baseline: `gorm.io/gorm` v1.31.2 (the current latest version); v1.25+ is a compatibility
  target, with support claims requiring a completed version matrix
- Driver baseline: `github.com/cocosip/go-oceanbase` **v0.1.2**
- Go version: 1.26, as required by the underlying driver

---

## 1. Background and constraints

### 1.1 Underlying driver capabilities (go-oceanbase)

| Capability | Details | Implications for GORM |
| --- | --- | --- |
| Connections | `sql.Open("oceanbase", dsn)` is registered; prefer `oceanbase.NewConnector(oceanbase.Config)` for a validated `driver.Connector` | Initialize should use `sql.OpenDB(connector)` |
| DSN format | URL form: `oceanbase://user:pass@host:2881/db?mode=mysql`; unknown parameters are errors; `ParseDSN` also applies driver defaults | Use the driver parser, record explicit parameters before parsing, then apply GORM defaults; do not invent a DSN format |
| Dual modes | `ModeMySQL` / `ModeOracle` / `ModeAuto`; the Connector fixes its mode after the first successful connection initialization and rejects mixed modes afterward | Default to MySQL; for explicit `mode=auto`, use the existing `SessionVariable(ctx, conn, "ob_compatibility_mode")` to read and validate the actual mode (§4.1) |
| MySQL compatibility profiles (v0.1.1+) | `Session.CompatibleMysqlVersion` (DSN `compatibleMysqlVersion`) selects MySQL 5/8 client semantics; **the driver defaults to 5, which truncates TIME fractional seconds, not all DATETIME/TIMESTAMP fractions** | GORM MySQL mode defaults to **8**, preserving TIME fractions and using the corresponding metadata semantics; respect explicit Config/DSN settings |
| Time reads | `Session.ParseTime` defaults to false, returning ordinary dates/timestamps as strings that cannot scan directly into GORM `time.Time` fields; MySQL TIME always returns duration strings | Enable ParseTime for internal connections; callers enable it for external connections; use string fields for TIME; see §5.2 for time zone types |
| Version parsing (v0.1.1+) | Internal driver parsing recognizes the "OceanBase" marker and takes the following version as the actual OB version (internal/configspec, **not exported**) | Apply the same strategy in version.go; query the actual OB version by mode (R1) |
| Parameter binding | Both modes support positional `?` parameters; Oracle also supports named parameters | `BindVarTo` writes `?` in both modes; Oracle `:1` is unnecessary |
| Errors | `*dberrors.Error{Code, SQLState, Message}` preserves original server codes and SQLSTATE | `ErrorTranslator` maps directly by Code (§7) |
| RowsAffected | Oracle mode requires `MatchedRows`; the driver rejects `ChangedRows` | Validate early and return a configuration error without silently correcting the caller's setting |
| Generated primary keys | `LastInsertId()` returns `ErrUnsupported` when there is no nonzero insert ID; OUT parameters are supported | Oracle Create must control execution and model key population; basic RETURNING INTO belongs to M2 |
| Extensions | ExecBatch, LOBs, cursors, XA, OBProxy routing, full-link diagnostics, etc. | Use native driver APIs, with an optional connection-scope helper and configuration examples (§8) |

### 1.2 Reference implementations

- **MySQL mode**: follow `gorm.io/driver/mysql` v1.6 behavior (OceanBase MySQL mode is highly compatible
  with MySQL syntax), but **do not import that package**. Implement the required type mappings, QuoteTo,
  ON DUPLICATE KEY UPDATE builder, and information_schema queries here to avoid a `go-sql-driver/mysql` dependency.
- **Oracle mode**: follow community Oracle dialect conventions: double quotes and uppercased identifiers,
  `FETCH FIRST` pagination, `MERGE` upserts, `NUMBER/VARCHAR2/CLOB` types, and `ALL_*` dictionary views.

---

## 2. Overall structure

One package supports both dialects. Users configure a single `oceanbase.Dialector` through DSN/Config;
MySQL and Oracle behavior is split into internal implementation files.

```
gorm-oceanbase/
├── oceanbase.go          # Config, Open(dsn), New(cfg), Dialector, and Initialize
├── dialect_mysql.go      # MySQL QuoteTo (backticks), DataTypeOf, and ClauseBuilders
├── dialect_oracle.go     # Oracle behavior, Create callback, full INSERT/INSERT ALL/MERGE, key population
├── migrator.go           # MySQL Migrator (information_schema; differences from the mysql driver)
├── migrator_oracle.go    # Oracle Migrator (ALL_TAB_COLUMNS / ALL_CONSTRAINTS / ALL_INDEXES, etc.)
├── error_translator.go   # dberrors.Error → gorm.ErrDuplicatedKey, etc.
├── version.go            # Version detection/parsing ("5.7.25-OceanBase_CE-v4.2.1…")
├── options.go            # Optional WithPinnedConn bridge and transaction boundary checks
├── docs/design.md        # This document
└── tests/integration/    # Integration tests: integration build tag and DSNs from environment variables
```

**Tradeoff**: do not split into `mysqlmode`/`oraclemode` subpackages. Users configure the mode once;
Config and error translation entry points are shared. Initialize fixes the mode and selects builders
and callbacks without probing again for each operation.

---

## 3. Config and entry points

```go
type Config struct {
    DSN        string              // oceanbase:// URL
    DSNConfig  *obdriver.Config    // Structured alternative to DSN; copied, not serialized back to a DSN
    Conn       gorm.ConnPool       // External pool; mutually exclusive with DSN/DSNConfig

    Mode       obdriver.Mode       // Nonzero overrides input; zero is unspecified; request auto via DSN mode=auto

    SkipInitializeWithVersion bool // Skip dialect version queries, not mode resolution; unknown versions disable unconfirmed features
    ServerVersion             string

    // MySQL mode: semantics follow the gorm mysql driver.
    DefaultStringSize        uint    // 0 follows mysql v1.6.0: ordinary strings use longtext; certain indexed, etc. fields use varchar(191)
    DefaultDatetimePrecision *int
    DisableDatetimePrecision bool
    CompatibleMysqlVersion   int     // 0 is unspecified; nonzero overrides DSN/DSNConfig; MySQL defaults to 8
    DontSupportRenameIndex / RenameColumn / ForShareClause / DropConstraint / NullAsDefaultValue … bool

    // Oracle mode.
    DefaultNumberPrecision …          // Default NUMBER precision policy
    // See §6 for sequence/IDENTITY primary key strategies.

    // Internal state.
    resolvedMode obdriver.Mode       // Fixed before creating the final Connector
}
```

This is a field sketch. `obdriver` aliases `github.com/cocosip/go-oceanbase`; the entry package remains
`oceanbase`. `Open/New` do not connect; Initialize returns parsing/configuration errors. Input must pass
driver validation; top-level overrides must not relax validation of invalid DSNs or unknown parameters.

Entry points follow GORM conventions:

```go
gorm.Open(oceanbase.Open("oceanbase://user:pwd@127.0.0.1:2881/db?mode=mysql"), &gorm.Config{...})
gorm.Open(oceanbase.New(oceanbase.Config{DSNConfig: &cfg, Mode: obdriver.ModeOracle}), ...)
```

---

## 4. Initialize flow

**Pre-initialization hook `Apply(*gorm.Config)`** (called by gorm.Open before Initialize): only record
whether a default `NowFunc` is needed. After resolving the final mode, Initialize installs the matching
precision policy for users without a custom NowFunc: milliseconds for MySQL, microseconds for Oracle.
Apply must not treat unresolved auto mode as MySQL; neither phase may overwrite the caller's NowFunc.

```
Initialize(db)
 ├─ 1. Validate mutually exclusive inputs, record explicit DSN parameters; ParseDSN or copy DSNConfig
 ├─ 2. Resolve mode; only DSN mode=auto uses a temporary pool and SessionVariable probe (§4.1)
 ├─ 3. Internal config: set concrete mode, enable ParseTime, default MySQL profile to 8, validate RowsAffectedMode
 ├─ 4. Create final NewConnector→sql.OpenDB; external Conn follows §4.2; keep the candidate pool local
 │     NewConnector copies config; step 3 must precede it, and later edits to the original cannot fix it
 ├─ 5. Probe version: MySQL SELECT OB_VERSION(); Oracle SELECT ob_version() FROM dual
 │     MySQL falls back to SELECT VERSION() only for an unsupported function; return network/permission errors
 │     Set capabilities from the actual OB version; handle unknown versions conservatively; install default NowFunc
 ├─ 6. callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{CreateClauses:…, …})
 │     MySQL: Create=[INSERT,VALUES,ON CONFLICT]; Update/Delete include LIMIT
 │     Oracle: retain transaction/hook/association callbacks; replace gorm:create to build/execute full SQL (§5.3)
 ├─ 7. Register other ClauseBuilders without replacing caller-registered builders of the same name
 └─ 8. Assign the candidate pool to db.ConnPool only after all fallible initialization steps succeed
```

Use `*sql.DB` directly for internal pools, exposed through `db.DB()`. GORM queries ConnPool for
`GetDBConnector`; implement it only on an actual pool wrapper, rather than adding an ineffective
Dialector interface. Implement `SavePointerDialectorInterface`: OB supports SAVEPOINT in both modes.

**Ownership on Initialize failure**: after Dialector.Initialize fails, GORM v1.31.2 obtains the pool
through `db.DB()` and closes it. Avoiding Close in the Dialector alone is insufficient. Use a local
candidate pool for version queries and other fallible steps, attaching it to `db.ConnPool` only after
success. If early attachment is necessary, clear `db.ConnPool` and any assigned `db.Statement.ConnPool`
before returning an error, preventing GORM from closing an external pool or the pool owning a transaction.
On failure, the Dialector closes only temporary/final pools it created; external Conn ownership remains
with the caller. Acceptance tests must inject version-query and other initialization failures through
`gorm.Open` and prove the original pool and transaction remain usable; direct Initialize tests alone are insufficient.

### 4.1 Mode resolution and defaults

GORM SQL generation requires **one consistent mode per GORM DB instance**. Top-level Mode zero means
"unspecified", not explicit auto. Resolution precedence:

```
Config.Mode (nonzero) > input configuration mode > MySQL default
Input configuration: explicit DSN mode (including auto), or nonzero DSNConfig.Mode
```

- Default unspecified mode to MySQL; the structured ModeAuto zero value also means unspecified.
  Request detection with DSN `mode=auto`, without adding a second meaning for zero.
- Auto uses a temporary Connector (ModeAuto) to obtain `*sql.Conn`, then calls
  `SessionVariable(ctx, conn, "ob_compatibility_mode")`. The existing API builds SQL for the physical
  connection's actual mode. Accept only non-NULL MYSQL/ORACLE values, case-insensitively; unknown values,
  timeouts, permissions, and other query errors cause initialization failure.
- Close the probe connection and temporary pool, then create the final pool with the concrete mode
  and final configuration. The probe performs driver connection initialization and mode reading only,
  without ORM CRUD. Caller initialization such as Session.InitSQL also runs on the probe connection;
  do not assume gorm.Open creates only one physical connection. Do not try to mutate configuration
  already copied by the temporary Connector. No new PinnedMode API or driver PR is needed.
- A GORM DB cannot mix tenant modes. The final internal Connector's concrete Mode and `ErrModeMismatch`
  enforce this on later physical connections. During M1, before Oracle delivery, detecting Oracle must
  return an explicit unsupported error rather than applying MySQL behavior.

### 4.2 Time and external connection configuration

- **Internal connections**: convert DSN/DSNConfig to structured configuration, apply adapter policy,
  then use NewConnector. Explicit DSN `parseTime=false` is a configuration error. If omitted in the
  DSN, or zero in DSNConfig, set `Session.ParseTime=true`, as required by ordinary GORM `time.Time` fields.
- **MySQL profile**: use nonzero top-level `CompatibleMysqlVersion`, then the explicit DSN parameter
  or nonzero DSNConfig value, otherwise 8. Record DSN parameter presence before ParseDSN applies its
  default of 5. Preserve 5 in caller-parsed/normalized DSNConfig as configured; do not guess its origin.
  Do not inject a MySQL profile in Oracle mode.
- **External Conn**: mutually exclusive with DSN/DSNConfig; requires an explicit concrete Mode.
  Do not modify or rebuild the caller's pool. Callers enable ParseTime, ensure mode consistency, and
  select MySQL profile 8 (or accept profile 5 TIME fractional loss). Skipping version queries does not
  waive these requirements.
- **Version**: query the actual Oracle `ob_version()` rather than inferring OB features from Oracle
  compatibility-view versions. Use a supplied `ServerVersion`; without one and with probing skipped,
  treat the version as unknown. `SkipInitializeWithVersion` affects the dialect only; driver version
  probing is separately controlled by `Session.SkipVersionProbe`.

---

## 5. Dialect behavior comparison

| Aspect | MySQL mode | Oracle mode |
| --- | --- | --- |
| `Name()` | `"oceanbase"` (flavor distinguished internally) | Same |
| `QuoteTo` | Backticks with escaped backticks; follow the mysql driver state machine | Double quotes `"`, escaped as `""`; **uppercase identifiers in unquoted paths** to normalize GORM snake_case model names to OB Oracle folding |
| `BindVarTo` | `?` | `?` (positional parameters supported in both driver modes) |
| `DefaultValueOf` | `DEFAULT`; INSERT can use `VALUES (DEFAULT)` | A database-default marker consumed by Oracle Create by omitting the column; never substitute NULL (§5.3) |
| NowFunc (record in Apply, install in Initialize) | Truncate to `DefaultDatetimePrecision` | Truncate to microseconds (TIMESTAMP default precision 6) |
| LIMIT/OFFSET | Native | Rewrite `ClauseBuilders["LIMIT"]` as `OFFSET n ROWS FETCH FIRST m ROWS ONLY` |
| ON CONFLICT | `ON DUPLICATE KEY UPDATE`, including `excluded` → `VALUES(col)` as in the mysql driver | Oracle Create selects MERGE or INSERT RETURNING by primary key/conflict target; limits in §5.3 |
| Batch inserts (`Create(&slice)`/CreateBatchSize) | Native multirow `VALUES (...),(...)` | Create builds full INSERT ALL statements; rows needing generated keys use individual INSERT RETURNING INTO, preserving order (M2) |
| FOR SHARE | `FOR SHARE`/`LOCK IN SHARE MODE`, version-dependent | `FOR UPDATE`; document the change in shared-lock semantics |
| SavePoint | `SAVEPOINT` / `ROLLBACK TO SAVEPOINT` | Same |
| `Explain` | `logger.ExplainSQL(sql, nil, '\'', vars...)` | Same; both use `?` placeholders |

### 5.1 DataTypeOf mappings

**MySQL mode** follows the gorm mysql driver, retaining the `getSchema*Type` behavior:

| schema.DataType | SQL type |
| --- | --- |
| Bool | `boolean` (supported by OB 4.x) |
| Int/Uint | `tinyint/smallint/mediumint/int/bigint` by Size; Uint adds `unsigned`; auto-increment adds `AUTO_INCREMENT` |
| Float | `decimal(p,s)` / `float` / `double` |
| String | `varchar(n)` / `mediumtext` / `longtext`; ordinary strings default to `longtext`, with 191 limited to the conditions below |
| Time | `datetime(p)` (configurable, default precision 3) |
| Bytes | `varbinary(n)` / `mediumblob` / `longblob` |

Follow mysql v1.6.0 string rules: use field Size first, otherwise nonzero `DefaultStringSize`. If neither
is set, use 191 for primary keys, fields with defaults, or `INDEX`/`UNIQUE`/`UNIQUEINDEX` tags; otherwise
use `longtext`. Once a length is determined, 65536 through 2²⁴ inclusive use `mediumtext`, above 2²⁴ uses
`longtext`, and other positive lengths use `varchar(n)`. Explicit SQL type tags pass through. Do not
limit every unsized string to 191.

**Oracle mode**:

| schema.DataType | SQL type |
| --- | --- |
| Bool | `NUMBER(1)` |
| Int | `NUMBER(n,0)`; Size → precision: ≤8 → 3, ≤16 → 5, ≤32 → 10, 64 → 19; auto-increment in §6 |
| Uint | `NUMBER(n,0)`; Size → precision: ≤8 → 3, ≤16 → 5, ≤32 → 10, 64 → 20; uint64 maximum is 18446744073709551615 |
| Float | `NUMBER(p,s)` / `BINARY_FLOAT` / `BINARY_DOUBLE` |
| String | `VARCHAR2(n)` (default 512) / `NVARCHAR2` / `CLOB` for long values |
| Time | `TIMESTAMP(p)` / `TIMESTAMP WITH TIME ZONE` / `TIMESTAMP WITH LOCAL TIME ZONE`, selectable by tag |
| Bytes | `RAW(n)` / `BLOB` |
| Custom | Pass through explicit `gorm:"type:ACTUAL_SQL_TYPE_NAME"`; see §5.2 for `types.Object/Array` requirements and limits |

### 5.2 Special driver type boundaries

- `types.Object/Array` cannot identify a database named type from the Go type alone. Models must use
  an actual type tag, such as `gorm:"type:MY_OBJECT_T"`, and matching Schema, Type, and ordered attributes.
  Without a tag, GORM may infer Object as string.
- Complex results require server-prepared binary queries. Use driver `PrepareServer` and GORM
  `PrepareStmt: true` so SELECTs without bind parameters are also prepared. Setting only the driver's
  default PrepareMode cannot ensure this for parameterless queries. Preserve ErrUnsupported from
  ordinary text queries returning complex types.
- Validate type creation and server storage separately. OBJECT column support does not imply named
  collection column support; PL/SQL Array binding does not imply array column storage. AutoMigrate
  does not create custom types.
- ParseTime handles ordinary DATE/TIMESTAMP reads into `time.Time`. Time zone/local time zone results
  require matching driver Scanner types or equivalent lossless paths; do not promise automatic mapping
  of every TIMESTAMP variant to `time.Time`.

### 5.3 Oracle Create, defaults, and upsert

Replace the `gorm:create` execution callback while retaining GORM's default transaction, model hook,
and association callbacks. Read VALUES, ON CONFLICT, and schema context to build and execute complete
INSERT, INSERT ALL, or MERGE statements. Do not depend on an ON CONFLICT builder absent from
BuildClauses or append a full INSERT ALL after a VALUES builder has already emitted INSERT INTO.

1. **Defaults**: distinguish omitted columns from explicit NULL using GORM field/default tags and
   input form. Struct zero values with default tags trigger defaults under GORM conventions; a nil
   pointer alone does not prove the caller requested explicit NULL. Use distinguishable input such
   as a present map key with nil value or `gorm.Expr("NULL")` for explicit NULL. Omit unspecified
   generated keys and database-function defaults, preserving explicit NULL. Consume DefaultValueOf's
   recognizable marker before final SQL construction; do not bind all missing fields as NULL.
2. **Batch inserts**: combine only adjacent rows with identical column sets, no generated-key output,
   and a safe INSERT ALL representation. Execute rows individually when column sets differ, rows
   contain only defaults, or batching is unsafe. Generated keys require single-row INSERT RETURNING
   INTO and population of original records, never inferred contiguous IDs. Preserve input order and
   the current transaction. Defaults, mixed explicit/generated IDs, and associations are M2 acceptance items.
3. **MERGE**: resolve ON CONFLICT match columns, using the complete model primary key when unspecified.
   Reject missing targets; targets require uniqueness. Build insert/update branches by column and
   exclude ON columns from `UpdateAll`. Reject explicit ON-column updates without silently removing
   assignments. DoNothing omits the update branch. Unsupported targets, conditions, or expressions
   must fail before DML in the corresponding Create callback; validate the callback's whole batch
   before executing any of its DML. Concurrent sessions targeting the same absent key can still cause
   a unique constraint error; translate and return it without automatic retry.
4. **Generated keys and associations**: GORM association callbacks automatically add ON CONFLICT for
   new objects, so a zero primary key alone is not a reason to reject. For a single generated primary
   key, when the effective conflict target is only that key, its value is missing under GORM rules,
   and conflict options are supported, use single-row INSERT RETURNING INTO and populate the original
   object. Rows with existing keys still use MERGE. Decide from schema/target/input, not clause origin;
   this applies to explicit calls and `Save(&slice)` too. New association IDs must be available before
   foreign-key or join-table writes. Route mixed new/existing records individually in input order.
   Propagate unique conflicts without replaying writes or finding/merging associations by other unique keys.
5. **M2 upsert limits**: MERGE requires a supplied or preallocated primary key and does not promise to
   return server-generated keys. Reject non-primary unique targets requiring generated/populated keys
   before the callback's DML and direct callers to preallocate keys (§6); rule 4 cannot bypass this.
   MERGE matching non-primary columns does not populate an existing row's key; the input ID is not a
   queried target-row ID. Non-primary database-default output, generic clause.Returning, and
   Update/Delete RETURNING belong to M3.
6. **Failure semantics**: propagate unsupported RETURNING or output decoding errors without falling
   back to LastInsertId, MAX(id), or reinsertion. Default transactions roll back on errors. Without
   default transactions, earlier successful hooks, association writes, or batch writes may already
   persist. Callback prevalidation does not undo earlier hooks or association writes; document and test this boundary.

---

## 6. Oracle primary key generation and population (required for M2)

The driver provides SQL execution and OUT parameters; the dialect owns table DDL, generation strategy,
and model population. Oracle cannot rely on MySQL LastInsertId, and querying by an unknown key cannot
retrieve it. M2 must implement basic primary key RETURNING INTO without driver changes.

### 6.1 IDENTITY

For an auto-increment model, Oracle Migrator targets `id NUMBER(20,0) GENERATED BY DEFAULT AS IDENTITY`
for uint64, or NUMBER(19,0) for int64. Ordinary inserts omit zero IDs and retrieve keys using
`INSERT ... RETURNING id INTO ?` with `sql.Out{Dest: &id}`. Execute through the current ConnPool's
ExecContext and populate models in Oracle Create, avoiding the default Create LastInsertId branch.

4.2.1 is the candidate minimum version from the original proposal; **its IDENTITY support threshold
was not verified in that review**. Validate the threshold and DDL/RETURNING combination on real versions
before recording support in the compatibility matrix. Unknown or earlier versions must fail before
CREATE TABLE DDL and suggest explicit keys/sequences. Do not fail table creation and automatically
try a different schema.

`BY DEFAULT` differs from `BY DEFAULT ON NULL`: explicit NULL does not trigger generation. Retrieve
and populate generated batch IDs individually; insert supplied IDs unchanged.

### 6.2 Explicit sequence preallocation

Without IDENTITY, explicitly create `CREATE SEQUENCE my_seq`, obtain `my_seq.NEXTVAL` in the current
transaction, set the model ID, then Create. Example:

```go
type User struct {
    ID uint64 `gorm:"primaryKey;autoIncrement:false"`
}

user := User{}
err := db.Transaction(func(tx *gorm.DB) error {
    if err := tx.Raw("SELECT my_seq.NEXTVAL FROM dual").Scan(&user.ID).Error; err != nil {
        return err
    }
    return tx.Create(&user).Error
})
```

Consumed sequence values are not restored on rollback; IDs need not be contiguous. OracleMigrator may
provide `HasSequence/CreateSequence` extensions outside GORM's generic Migrator interface, with quoted
and escaped sequence identifiers. Do not use PostgreSQL-style nextval functions or confuse a database
default expression with model ID population.

### 6.3 Required tests

Cover IDENTITY, preallocated sequences, explicit keys, single/batch rows, mixed explicit/generated IDs,
uint64 limits, rollback, and errors with default transactions disabled. Associations must cover new
belongs-to/has-one/has-many/many-to-many objects, mixed new/existing objects, and FullSaveAssociations,
checking populated keys, foreign keys, join-table references, and existing-object updates. Also cover
Save(slice) zero-key routing and rejection of non-primary conflict targets. Claim Oracle Create support
only after real successful RETURNING cases and failure-without-replay cases pass.

## 7. Error translation (ErrorTranslator)

`Translate(err)` obtains the original code with
`var serverErr *dberrors.Error; errors.As(err, &serverErr)` and selects a table by the resolved mode:

- MySQL: `1062→gorm.ErrDuplicatedKey`, `1451/1452→gorm.ErrForeignKeyViolated`; these MySQL protocol codes
  also apply in OB MySQL mode
- Oracle: `1(ORA-00001)→ErrDuplicatedKey`, `2291/2292(ORA-02291/2292)→ErrForeignKeyViolated`
- Return unmatched errors unchanged; isolate tables by mode to avoid code collisions

Enable with `gorm.Config{TranslateError: true}`.

## 8. Exposing driver extensions outside the Dialector interface

Follow actual driver signatures: ExecBatch accepts Execer; the Session used by LOBs, cursors, and
metadata supports non-nil `*sql.Conn` or `*sql.Tx`; XA requires a pinned connection and cannot mix with
local sql.Tx. Use existing APIs directly rather than requiring a new connection for every extension.

Optional `options.go` provides only:

```go
// WithPinnedConn reuses the current *sql.Conn or borrows from a plain pool; it rejects transaction contexts.
func WithPinnedConn[T any](db *gorm.DB, fn func(ctx context.Context, conn *sql.Conn) (T, error)) (T, error)
```

- Borrow from a plain pool and return the connection after the callback; reuse an existing `*sql.Conn`
  without closing the caller's connection. Preserve GORM Statement.Context and support PrepareStmt
  wrappers. Return an error if ownership cannot be determined.
- WithPinnedConn rejects transactions, including PrepareStmt-wrapped transactions, without borrowing
  another connection via `db.DB()`. For ExecBatch/LOBs/cursors/metadata inside transactions, pass the
  current `*sql.Tx` directly to retain commit/rollback boundaries; reject if it cannot be obtained.
- XAStart/End/Prepare and other operations on one branch must share a single pinned-connection scope.
  LOB/cursor handles must not outlive their owning connection or transaction.
- M3 examples cover write-then-rollback, avoiding extra borrowing from single-connection pools,
  context cancellation, and rejection of XA/local transaction mixing. Do not add duplicate driver wrappers.

## 9. Milestones

| Phase | Scope | Acceptance |
| --- | --- | --- |
| **M1: usable MySQL mode** | Configuration merging, SessionVariable mode detection, pre-connection defaults, MySQL dialect/Migrator, error translation, actual OB version parsing | DryRun SQL baseline; live MySQL CRUD/associations/transactions/migrations; parseTime, profiles 5/8, datetime/TIME precision |
| **M2: usable Oracle mode** | Oracle Create, omitted defaults, INSERT ALL and per-row key output, limited MERGE (§5.3), FETCH FIRST, IDENTITY/sequences, basic key RETURNING INTO, ALL_* Migrator, error translation | Live Oracle CRUD/associations/migrations; single/batch key population, explicit NULL, type boundaries, transaction failure semantics; record tested versions |
| **M3: enhancements** | Non-primary defaults and generic Create/Update/Delete RETURNING, driver extension examples with explicit transaction boundaries, OBProxy configuration docs | Live RETURNING and transaction bridge acceptance; publish docs/compatibility.md |

## 10. Test strategy

1. **Unit and protocol simulation, without a live database**: input exclusivity/precedence, explicit
   DSN parameter origins, final Connector configuration, auto MYSQL/ORACLE/NULL/unknown/error results,
   NowFunc timing and caller overrides, QuoteTo, type boundaries, error codes, and version parsing.
   Inject initialization errors through gorm.Open and prove external pools/transactions stay open.
2. **SQL and execution paths**: compare MySQL baselines for ordinary/indexed strings and
   DefaultStringSize. Snapshot complete Oracle INSERT/INSERT ALL/MERGE statements, beyond individual
   builders. Cover OUT keys populated into original records, no LastInsertId branch, default-tag zero
   values versus explicit NULL, mixed IDs, no error replay, hook/association/transaction lifecycles,
   automatic association ON CONFLICT, Save(slice), rejected non-primary targets, batch prevalidation,
   and rejected ON-column updates. Simulation cannot replace server syntax acceptance.
3. **Integration, build tag `integration`**: use `OB_MYSQL_DSN` / `OB_ORACLE_DSN` for real tenants.
   Beyond CRUD, associations, transactions/SavePoint, incremental AutoMigrate, and concurrency, cover
   datetime(3)/(6), TIME(3)/(6), profiles 5/8, ParseTime, Oracle generated keys/sequences, NUMBER
   int64/uint64 limits, restricted Object/Array paths, and negative cases. Record IDENTITY thresholds,
   defaults, and MERGE results by actual version.
4. **CI and support claims**: run offline tests by default and compile integration tests with
   `go test -tags=integration ./tests/integration -run '^$'`. Run live acceptance explicitly with
   `go test -tags=integration ./tests/integration -run MySQL` or `-run Oracle`. Missing-DSN skips do not
   count as live passes. GORM v1.25+ support requires a separate version matrix.

## 11. Risks and open questions

| # | Risk/question | Mitigation |
| --- | --- | --- |
| R1 | Mistaking a MySQL compatibility prefix or Oracle compatibility-view version for the OB version | Query actual ob_version() in both modes, with FROM dual for Oracle; limit MySQL VERSION() fallback and parse the OceanBase marker; disable unconfirmed features for unknown versions |
| R2 | Oracle identifier case: snake_case models versus automatic uppercase folding | Uppercase unquoted identifiers in QuoteTo; document prominently and cover in integration tests |
| R3 | Changing only VALUES/ON CONFLICT builders can leave an INSERT prefix or skip upsert | Replace gorm:create in M2 and build/execute complete statements; snapshot prefixes, defaults, conflicts, and batches |
| R4 | The driver is still under development, as noted in its README | Use the remote module via `go get github.com/cocosip/go-oceanbase` without replace; depend on stable Config/DSN/dberrors/types APIs and update versions for incompatible driver changes |
| R5 | Mixed modes/multiple tenants sharing one pool | Explicitly unsupported; Initialize fixes the mode and driver ErrModeMismatch enforces it |
| R6 | Profile 5 truncates TIME fractions; driver ParseTime=false causes time.Time scan failures | Apply profile 8 and ParseTime=true before creating the final Connector; preserve explicit 5 and test differences; test TIME and DATETIME separately |
| R7 | Default GORM Create returns ErrUnsupported without an Oracle insert ID; querying by an unknown key cannot work | Include basic key RETURNING INTO in M2; retrieve generated batch IDs per row and prohibit write replay |
| R8 | Substituting NULL for defaults or batching mixed IDs changes data semantics | Omit defaulted columns, preserve explicit NULL, and build per row or by adjacent identical column sets |
| R9 | Injecting configuration after NewConnector copies it, or trying to write it into an external pool | Parse, detect, and merge before final internal pool creation; define separate explicit prerequisites for external pools |
| R10 | NUMBER(19) cannot cover uint64; Object/Array types and query paths have prerequisites | Use NUMBER(20,0) for uint64; require custom SQL type tags and server preparation; validate actual server storage boundaries |
| R11 | Extension bridges can escape the current GORM transaction by borrowing another connection | WithPinnedConn rejects transactions; reuse sql.Tx directly inside them; test rollback, single-connection pools, and wrappers |
| R12 | IDENTITY's 4.2.1 threshold and minimum GORM version lack live acceptance | Keep them pending validation; claim support only after a real version matrix, never based on documentation changes |
| R13 | GORM closes an attached external pool after Initialize fails | Delay candidate attachment, detach on early-attachment failure paths, and test caller pool/transaction usability through gorm.Open |
| R14 | Automatic association ON CONFLICT conflicts with generated keys | Use INSERT RETURNING for missing generated values targeting the primary key, MERGE for supplied keys; include every association type, mixed records, and Save(slice) in M2 |
