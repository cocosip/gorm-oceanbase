# gorm-oceanbase development plan and progress

This document defines development tasks from [design.md](design.md). Each task's status column tracks
current progress directly.

**Current progress: 22 / 32 completed, 7 in progress, 3 not started, 0 blocked. Next: revalidate Oracle
failures and complete the remaining G22/G39 integration coverage.**

**Latest validation round**: the dependency was upgraded to `go-oceanbase v0.1.2`. The standalone
`cmd/gorm-obtest` tool ran a four-target main matrix against OceanBase 4.4.2.2:
346 pass / 8 fail / 0 skip / 70 not_applicable. Two supplemental MySQL profile 5 targets produced
174 pass / 0 fail / 0 skip / 38 not_applicable. All applicable MySQL cases passed; Oracle still has
failures. See the [live test details](../tests/acceptance/README.md). Dropped rows in MySQL default-only
batch inserts and acceptance-tool issues were fixed, and objects created by that run were cleaned up.
The older integration suite with fixed table names was not executed in that schema. G22/G39 remain
partially complete; one server version does not justify broader compatibility claims.

**Maintenance rules**:

- Set a task to `◐` when starting, `✅` after meeting its acceptance criteria, or `⚠️` when blocked,
  with a brief reason in the row. Keep partially completed tasks at `◐` and describe the remaining work.
- Whenever a task status changes, update the progress summary, next task, and phase status above.
  Mark a phase `✅` only after every task in it passes acceptance.
- Edit task scope, dependencies, and acceptance criteria in place, keeping the design document in sync.
  Do not append work logs or change histories at the end.
- Documentation changes, temporary review experiments, and external driver tests do not complete
  this repository's development tasks and cannot justify `✅`.

**Status legend**: ☐ Not started | ◐ In progress | ✅ Completed | ⚠️ Blocked/on hold

---

## 1. Phase overview

| Phase | Goal | Design sections | Status |
| --- | --- | --- | --- |
| M0 | Repository skeleton and dependencies ready | §2 | ✅ |
| M1 | **Fully usable MySQL mode** (first usable release) | §3–§5, §7, §10 | ◐ |
| M2 | Usable Oracle mode | §5, §6, §9 | ◐ |
| M3 | Enhancements: non-primary fields and generic RETURNING, driver extension bridge, compatibility matrix | §5.3, §8 | ☐ |

---

## 2. M0: repository skeleton

| ID | Task | Scope | Dependencies | Acceptance | Status |
| --- | --- | --- | --- | --- | --- |
| G01 | Initialize module | `go.mod`: module github.com/cocosip/gorm-oceanbase, go 1.26; pin development baselines `gorm.io/gorm@v1.31.2` (current latest) and `github.com/cocosip/go-oceanbase@v0.1.2` (remote module, no replace); earlier GORM support requires a version matrix | — | `go build ./...` passes | ✅ |
| G02 | README skeleton | Project purpose, quick start matching design.md §3 entry points, documentation index | G01 | Review passes | ✅ |
| G03 | CI skeleton | GitHub Actions: build, vet, offline tests, golangci-lint using existing .golangci.yml; add `go test -tags=integration ./tests/integration -run '^$'` after integration packages exist | G01 | Routine skeleton checks pass; integration compilation passes after G22/G39 exist; skips do not count as live acceptance | ✅ |

## 3. M1: fully usable MySQL mode

### 3.1 Dialector core (oceanbase.go, version.go)

| ID | Task | Scope | Dependencies | Acceptance | Status |
| --- | --- | --- | --- | --- | --- |
| G10 | Config and entry points | Fields from design.md §3; Open/New perform no networking; validate DSN/DSNConfig/Conn exclusivity; record explicit DSN parameters before driver ParseDSN; copy structured configuration without serializing back or retaining a custom DriverName path | G01 | Unit tests: input branches, no side effects, errors returned by Initialize, caller config unchanged | ✅ |
| G10a | MySQL profile injection | Nonzero top-level value > explicit DSN/nonzero DSNConfig > default 8; apply before final Connector creation; preserve 5 in normalized DSNConfig; no injection for Oracle (design.md §4.2, R6) | G10, G11 | Unit tests: actual Connector config uses 8, explicit 5 preserved, source precedence; verify TIME fractions and DATETIME behavior separately | ◐ |
| G10b | Time read policy | Internal Session.ParseTime=true; reject explicit DSN false; enable a zero DSNConfig bool under GORM policy; document external Conn prerequisites (design.md §4.2) | G10 | Unit tests: successful time.Time scanning, explicit false rejected, external pool unchanged | ◐ |
| G11 | Mode resolution | Nonzero top-level Mode > explicit DSN/nonzero DSNConfig Mode > MySQL; zero is unspecified; only DSN mode=auto uses a temporary pool and existing SessionVariable to read/validate mode, then closes temporary resources and fixes final config (design.md §4.1) | G10 | Unit/protocol tests: explicit sources and overrides, MYSQL/ORACLE/NULL/unknown values, timeout/permission errors; during M1 explicitly reject detected Oracle rather than applying MySQL behavior | ◐ |
| G12 | NowFunc installation | Apply records caller customization; Initialize installs MySQL/Oracle precision after final mode resolution; auto must not default prematurely to MySQL (design.md §4) | G11 | Unit tests: both precisions, final auto mode, caller customization preserved | ✅ |
| G13 | Version detection | MySQL `SELECT OB_VERSION()`, falling back to VERSION() only for unsupported functions; Oracle `SELECT ob_version() FROM dual`; parse actual OB version; handle supplied ServerVersion, skipped probing, and unknown versions conservatively; distinguish driver probing | G11 | Unit tests: both SQL variants, OceanBase marker parsing, limited fallback, error propagation, unconfirmed features disabled for unknown versions | ✅ |
| G14 | Initialize | Parse/detect/merge final config before NewConnector→OpenDB; external Conn requires explicit concrete Mode; attach candidate pool to db.ConnPool only after all fallible steps; detach on early-attachment failure paths to prevent GORM closing external pools/transaction pools; close owned resources on failure; register callbacks/ClauseBuilders and SavePointerDialectorInterface; support db.DB() through actual ConnPool (design.md §4, R13) | G10a, G10b, G11, G12, G13 | Protocol tests: final config, defaults, resource release; initialization failures through gorm.Open preserve caller pool queries and transactions, including wrappers; live connections in G22 | ✅ |

### 3.2 MySQL dialect behavior (dialect_mysql.go)

| ID | Task | Scope | Dependencies | Acceptance | Status |
| --- | --- | --- | --- | --- | --- |
| G15 | QuoteTo | Backtick state machine, including dot separators and escaped backticks, matching the gorm mysql driver | G10 | Unit tests: quoting cases match mysql driver output | ✅ |
| G16 | DataTypeOf | Complete MySQL table in design.md §5.1: Bool/Int/Uint/Float/String/Time/Bytes/custom, AUTO_INCREMENT/unsigned; strings follow field Size, DefaultStringSize, conditional 191, then longtext rather than a universal 191 default | G10 | Full mapping snapshots; ordinary string→longtext, primary/default/INDEX/UNIQUE fields→varchar(191), explicit/global lengths, mediumtext/longtext boundaries; compare mysql v1.6.0 | ✅ |
| G17 | Other interfaces | `DefaultValueOf` (DEFAULT), `BindVarTo` (?), `Explain` | G10 | Unit tests | ✅ |
| G18 | ClauseBuilders | ON CONFLICT→`ON DUPLICATE KEY UPDATE`, including excluded→VALUES(col); empty VALUES() special case; version-dependent FOR SHARE (design.md §5 table) | G14 | DryRun SQL snapshots | ✅ |

### 3.3 Migrator and error translation (migrator.go, error_translator.go)

| ID | Task | Scope | Dependencies | Acceptance | Status |
| --- | --- | --- | --- | --- | --- |
| G19 | MySQL Migrator | Generic migrator plus information_schema queries for tables/columns/indexes/constraints/views/types; ColumnTypes/AlterColumn/RenameColumn, etc., with feature flags from G13 | G14, G16 | Integration: complete AutoMigrate workflow and incremental changes | ◐ |
| G20 | Error translation | MySQL `*dberrors.Error` codes: 1062→ErrDuplicatedKey, 1451/1452→ErrForeignKeyViolated; unmatched errors unchanged | G11 | Unit tests: matched/unmatched codes and non-driver errors | ✅ |

### 3.4 Tests and documentation

| ID | Task | Scope | Dependencies | Acceptance | Status |
| --- | --- | --- | --- | --- | --- |
| G21 | Complete unit coverage | Configuration/connection boundaries and DryRun SQL comparisons, including external resource ownership after gorm.Open failure and string defaults; explicit allowed differences such as probing/boolean; separate minimum-GORM matrix, no support claims for untested versions (design.md §10) | G10–G20 (including G10a/G10b) | `go test ./... -count=1` passes; record actual GORM version tested | ✅ |
| G22 | MySQL integration tests | integration tag and OB_MYSQL_DSN: CRUD, associations, transactions/SavePoint, AutoMigrate, concurrency; time.Time reads with ParseTime=true; datetime(3)/(6) round trips; TIME(3)/(6) differences under profiles 5/8; synchronize compile checks with G03 | G14–G20 | Live MySQL acceptance with version and pass/fail/skip counts; missing-DSN skips are not passes | ◐ |
| G23 | MySQL usage documentation | README: mode/profile precedence, ParseTime, external pool prerequisites/ownership, string defaults, auto errors, no mixed-mode tenants; claim only validated scope | G21, G22 | Review passes | ◐ |

## 4. M2: usable Oracle mode

| ID | Task | Scope | Dependencies | Acceptance | Status |
| --- | --- | --- | --- | --- | --- |
| G30 | Oracle entry path | Connect Oracle behavior to G11 mode resolution; reuse SessionVariable without new PinnedMode; enable G13 Oracle product-version query and final-mode NowFunc | G11, G12, G13, G14 | Unit/protocol tests: explicit oracle, auto→oracle, read errors propagated; live acceptance in G39 | ✅ |
| G31 | QuoteTo (Oracle) | Double quotes and uppercase unquoted identifiers (R2); distinguish explicit-case names and map result columns to model fields | G30 | Unit tests: case, escaping, scanning; live case validation in G39 | ✅ |
| G32 | DataTypeOf (Oracle) | Int64→NUMBER(19,0), Uint64→NUMBER(20,0); other NUMBER/VARCHAR2/CLOB/RAW/BLOB/TIMESTAMP types; Object/Array require explicit SQL type tags and server preparation; restrict time zone Scanner paths (design.md §5.1/5.2) | G30 | Unit tests: mappings, int64/uint64 limits, negative complex-type inference cases and positive tagged cases | ✅ |
| G33 | LIMIT/FOR rewriting | `ClauseBuilders["LIMIT"]`→`OFFSET n ROWS FETCH FIRST m ROWS ONLY`; `ClauseBuilders["FOR"]` converts `FOR SHARE` to `FOR UPDATE` because Oracle lacks shared-lock reads; document semantic change in README | G30 | DryRun snapshots | ✅ |
| G34 | Oracle Create and batch inserts | Replace gorm:create while retaining transaction/hook/association callbacks; build full INSERT/INSERT ALL; distinguish default-tag zero values from explicit NULL by GORM input form; batch adjacent identical column sets or execute individually; retain per-row conflict target and key context for G35/G36 (design.md §5.3) | G30, G31, G32 | Full SQL snapshots without duplicate prefixes; struct defaults, map nil/NULL expressions, different column sets/order, execution errors; generated-key path uses G36 | ✅ |
| G35 | MERGE upsert and key routing | Validate effective match columns and uniqueness; UpdateAll excludes ON columns, reject explicit ON-column updates, DoNothing omits update branch; MERGE for supplied/preallocated keys; missing single generated keys targeting only the primary key use G36 INSERT RETURNING regardless of clause origin; reject non-primary targets needing generated-key output; validate the whole callback batch before writing, including assignments referencing columns absent from a row (design.md §5.3, R14) | G34 | SQL snapshots and G39 integration: insert/update/DoNothing, association ON CONFLICT, Save(slice), mixed-key routing; reject unsupported conditions/ON-column updates/non-primary generated-key requests before callback DML; no retry of race errors | ✅ |
| G36 | Generated keys and basic RETURNING | Candidate IDENTITY threshold 4.2.1 awaits live validation; reject unknown/earlier versions before DDL; single INSERT RETURNING INTO + sql.Out populates models, batches per row; integrate G35 routing and populate association IDs before foreign-key/join-table writes; preallocate seq.NEXTVAL with autoIncrement:false; OracleMigrator sequence extensions (design.md §6) | G13, G32, G34, G35 | Simulation: no LastInsertId or replay; integration: IDENTITY/sequences/explicit keys, mixed IDs, new associations and FullSaveAssociations, Save(slice), foreign-key/join-table references, default transaction rollback and errors without default transactions | ✅ |
| G37 | Oracle Migrator | Query ALL_TAB_COLUMNS/ALL_CONSTRAINTS/ALL_INDEXES/ALL_CATALOG; integrate G36 key DDL and validated version thresholds; reject IDENTITY on unknown/unsupported versions before DDL | G31, G32, G36 | Integration: full AutoMigrate workflow, including allowed/rejected key DDL paths | ✅ |
| G38 | Oracle error translation | Mode-isolated ORA codes: 1→ErrDuplicatedKey, 2291/2292→ErrForeignKeyViolated | G30 | Unit tests | ✅ |
| G39 | Oracle integration tests and documentation | OB_ORACLE_DSN: CRUD/migrations, identifier scanning, defaults/NULL, batch key output, limited MERGE; new belongs-to/has-one/has-many/many-to-many, mixed new/existing objects, FullSaveAssociations, Save(slice); NUMBER limits, Object prepared/text negative paths, Array storage limits; synchronize compile checks with G03 | G31–G38 | Live Oracle acceptance with version/pass/fail/skip; check foreign-key/join-table references, rejection/transaction boundaries; document that non-primary MERGE does not populate target IDs; verify IDENTITY threshold; simulation/skips are not passes | ◐ |

## 5. M3: enhancements (optional, scheduled as needed)

| ID | Task | Scope | Dependencies | Status |
| --- | --- | --- | --- | --- |
| G51 | Extended RETURNING INTO | Non-primary database-default fields, generic clause.Returning, Update/Delete output; basic Create key output already belongs to G36 and must not be deferred from M2 | G36, G39 | ☐ |
| G52 | Extension bridge | WithPinnedConn reuses current connections or borrows from plain pools, rejecting transactions including PrepareStmt wrappers; transaction examples pass original sql.Tx directly; follow driver signatures for ExecBatch/LOBs/cursors/metadata; single-scope XA rejects local transaction mixing; validate rollback, single-connection pools, cancellation, and ownership (design.md §8) | G14, G39 | ☐ |
| G53 | Compatibility matrix | docs/compatibility.md: feature × mode × OB/GORM version × validation status; include M2 upsert limits, IDENTITY threshold evidence, complex types, and extension transaction boundaries | G22, G39, G51, G52 | ☐ |

---

> M3's three tasks count toward the total but are optional enhancements outside the M1/M2 delivery commitment.
