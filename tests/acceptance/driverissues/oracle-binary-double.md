# Oracle binary-double binding: live regression and protocol diagnosis

## Current status: v0.1.4 verified on 2026-10-24

The published go-oceanbase v0.1.4 fixes the reproduced float binding failure on
OceanBase 4.4.2.2. That verification used v0.1.4 through go.mod/go.sum;
the earlier local-driver workspace was disabled for those checks.
No driver implementation changes were made in this consumer verification.

- TestOracleBinaryDoubleRange passes in ordinary and explicit prepared paths,
  including 1.5, 1.1, 1e200, positive/negative MaxFloat64 and smallest subnormals.
- An independent unparameterized SELECT TO_CHAR(V) returns 1.5, 1.1, MaxFloat64
  and 1e200 correctly: all eight text readbacks parse to the exact input float64.
- TestOracleMigrationColumnChanges passes, including the previously malformed
  mixed-parameter INSERT, existing-data preservation and constraint changes.
- The initial v0.1.4 four-target matrix reports 350 pass / 4 fail / 0 skip / 70 NA,
  run ID 6efcd3844af7. Each Oracle target has 88 passes and only two failures:
  NCLOB (902) and collection-column (600) DDL. All float comparisons pass.
- Official Connector/J subsequently confirmed these DDL rejections independently.
  After removing the two unsuitable column cases, the fresh supported-scope
  matrix reports 350 pass / 0 fail / 0 skip / 66 NA, run ID 0ad4c1c7cea6.
  See the [DDL diagnosis and scope correction](oracle-ddl-support.md).
- Build, unit tests, vet, integration compilation, module checks and both lint
  selections pass. Independent catalog checks found no owned objects remaining.

Current evidence: `.cache/live-v014-20261024.json`,
`.cache/live-v014-regressions-20261024.log`,
`.cache/live-v014-stored-values-20261024.log`, and
`.cache/live-v014-cleanup-20261024.log`.
The full older integration suite, profile 5, older servers and OBProxy were not
retested in this consumer round. The diagnosis below records the earlier failure
and the superseded recommendation; it is retained as historical evidence.

## Historical failure: 695a8f2

Local driver commit `695a8f2ef2fc4ee4ccfa2cecd1427300ee4d0273` fails live
roundtrip testing. Type 257 silently stores incorrect values and can break
multi-parameter packets. This diagnosis was provided to the owner before v0.1.4.
No driver source changes or connection credentials are included here.

## Correction to the earlier recommendation

The previous recommendation to use type 257 in the ordinary Oracle float64
parameter header was incorrect and is superseded by this report. That experiment
checked successful INSERT execution without checking stored values.

Although OceanBase defines `MYSQL_TYPE_ORA_BINARY_DOUBLE = 257` internally, the
ordinary COM_STMT_EXECUTE parameter header is **type(1 byte) + flags(1 byte)**,
not a 16-bit type field. AppendUint16(257) sends `01 01`; the server reads type 1
(TINYINT) and flag 1. It does not read type 257.

## Historical verification environment and dependency selection

- Server: OceanBase 4.4.2.2, direct TCP, Oracle and MySQL compatibility tenants.
- Consumer: gorm-oceanbase at d0d40c6, GORM v1.31.2.
- Updated driver under test: local go-oceanbase commit 695a8f2.
- Client: Go 1.27.2, Windows amd64.
- Consumer go.mod still references published go-oceanbase v0.1.2.
- An ignored `.cache/go.work` selects the local driver solely for verification;
  the tested executable's module information confirms that replacement.

Both parameterized ExecContext and explicit PrepareContext/Stmt.ExecContext
reproduce the failure with database/sql alone, without GORM. Disabling GORM
PrepareStmt does not avoid server preparation in the driver's default
parameterized execution path.

## Live stored-value evidence for 695a8f2

An independent database/sql probe creates an owned random table with
`(V BINARY_DOUBLE)`, binds one value, and reads it using an unparameterized
`SELECT TO_CHAR(V)`. This checks the stored server value independently of the
client's binary float result decoding.

| Bound float64 | First payload byte | Actual stored value |
| --- | --- | --- |
| 1.5 | 00 | 0 |
| 1.1 | 9a | -102 |
| math.MaxFloat64 | ff | -1 |
| 1e200 | 5a | 90 |

All four INSERTs report success. Both execution paths produce the same values.
These values match the first byte of the IEEE-754 little-endian payload read as
signed TINYINT. The remaining seven bytes were intended to belong to the double;
with more parameters they can misalign subsequent parameter payloads.

The migration regression demonstrates a packet failure:

```text
INSERT INTO "ORACLE_REVIEW_COLUMN_CHANGES" ("ID","AMOUNT","NAME") ...
server error 600 (HY000): OBE-00600: internal error code,
arguments: -9747, malformed ps packet
```

Not every mixed-parameter statement fails: one float-followed-by-string probe
returned success. Execution success must always be paired with value checks.

## Protocol evidence and driver adjustment points

Driver paths at 695a8f2:

- `internal/valuecodec/binary.go:45`: Oracle float64 sets Parameter.Type to 257.
- `internal/wire/stmt.go:113`: AppendUint16 writes Parameter.Type as two bytes.
- `internal/configspec/attributes.go:14`: client attribute capabilities start at
  1 and optionally include 2 for metadata; binary-double capability is absent.
- `internal/connect/protocol.go:43`: parses the server's
  `__ob_client_capability_flag`; binary-double negotiation needs consideration.

OceanBase public server source provides the corresponding interpretation:

1. `obmp_stmt_execute.cpp:680-687` declares uint8_t type and int8_t flag and
   reads them separately. Its own comment says `type(1) + flag(1)`.
2. `obmp_stmt_execute.cpp:2337-2353` decodes ordinary MYSQL_TYPE_DOUBLE (5).
   In Oracle mode it calls param.set_double when the session supports JDBC
   binary double; otherwise it converts the value to Oracle NUMBER.
3. `ob_mysql_packet.h:724-726` defines the client attribute capability bits:
   locator-v2 = 1, new metadata = 2, JDBC binary double = 4 (`1 << 2`).
4. `obmp_connect.cpp:1944-1953` enables server JDBC binary-double support and
   intersects the client request with server-supported capabilities.
5. `ob_basic_session_info.h:1478-1480` reads that negotiated session capability.

Direction identified during the original diagnosis, before v0.1.4 verification:

- Keep ordinary float64's wire type 5 and its eight IEEE-754 payload bytes.
- Investigate requesting `OB_CLIENT_SUPPORT_JDBC_BINARY_DOUBLE` (mask 4) through
  login attribute `__ob_client_attribute_capability_flag`, preserving other bits.
- Verify negotiated `__ob_client_capability_flag` and define behavior when an
  older server does not acknowledge support. Do not assume support everywhere.
- Keep ordinary binds, batch paths, and complex collection elements distinct
  where their layouts differ. The referenced server already decodes type-5
  complex elements as double.
- Do not globally change result metadata or decoder types to 257. A wider
  internal enum does not establish a wider ordinary parameter type header.

At the time of the original diagnosis this was a likely negotiation path, not a
verified patch. The owner subsequently implemented it in v0.1.4; the consumer
verification above confirms the reproduced ordinary/prepared binding cases.
No driver patch or overlay was applied by this consumer verification.

Public references use develop, not the exact source identity of the deployed
4.4.2.2 binary:

- [Parameter decoding](https://github.com/oceanbase/oceanbase/blob/develop/src/observer/mysql/obmp_stmt_execute.cpp#L680)
- [Capability bits](https://github.com/oceanbase/oceanbase/blob/develop/deps/oblib/src/rpc/obmysql/ob_mysql_packet.h#L713)
- [Capability negotiation](https://github.com/oceanbase/oceanbase/blob/develop/src/observer/mysql/obmp_connect.cpp#L1937)
- [Session capability check](https://github.com/oceanbase/oceanbase/blob/develop/src/sql/session/ob_basic_session_info.h#L1478)

## Original v0.1.2 failure remains relevant

Before the type-257 change, binding math.MaxFloat64 or 1e200 using ordinary
type 5 failed with `1426 / 22003 / OBE-01426 numeric overflow` in Oracle mode.
Prepare succeeds; execution fails. The same session accepts raw binary-double
literals such as `1.7976931348623157e308d` and
`TO_BINARY_DOUBLE('1.7976931348623157e308')` in the destination column.

`CAST(? AS BINARY_DOUBLE)` does not prevent failure because NUMBER conversion
occurs during parameter decoding. A decimal string through `TO_BINARY_DOUBLE(?)`
succeeds as a diagnostic control, not a consumer workaround. The original
behavior is consistent with type-5 decoding without the negotiated capability.

## Historical full live acceptance result: 695a8f2

Run ID: `6f209f015cf9`; duration: 1m42.1402206s.

| Target | Passed | Failed | Not applicable |
| --- | ---: | ---: | ---: |
| MySQL | 87 | 0 | 19 |
| MySQL prepared | 87 | 0 | 19 |
| Oracle | 82 | 8 | 16 |
| Oracle prepared | 82 | 8 | 16 |
| Total | 338 | 16 | 70 |

Each Oracle target has six float comparison failures: float32, float64, and
null_float64, each including its pointer/null variant. Two other failures are
independent server DDL capability limits already present with v0.1.2:
NCLOB (902, invalid datatype) and collection-valued columns (600, unsupported
nested table type). Do not hide these by changing column types.

The v0.1.2 baseline had 86 passes / 4 failures per Oracle target (346 / 8 total).
The update adds four failures per Oracle target and changes float failures from
overflow into incorrect values.

An independent post-run inventory found zero objects owned by this matrix run
in both schemas. The stored-value probe also confirmed zero owned objects after
cleanup.

## Regression requirements and reproduction

The GORM-free TestOracleBinaryDoubleRange in
`tests/acceptance/driverissues/oracle_test.go` checks ordinary and explicit
prepared execution, starting with 1.5 and covering positive/negative MaxFloat64
and smallest nonzero float64. With 695a8f2 both paths fail immediately with
`got=0 want=1.5`.

Supply OB_ORACLE_DSN through the process environment and explicitly select the
intended driver revision before running:

```powershell
go test -tags=integration ./tests/acceptance/driverissues `
  -run '^TestOracleBinaryDoubleRange$' -count=1 -v -timeout 2m
```

For the next driver change, verify stored values, not only execution success:
ordinary values including 1.1, negative zero, range extremes and subnormals;
repeated prepared execution; NULL/non-NULL transitions; mixed integer/float/text
parameters; batch execution; and both compatibility modes. NaN/Inf checks should
follow the existing documented policy. Unit tests must model the separate header
bytes rather than merely asserting Type equals 257.

Local ignored evidence in this consumer repository:

- `.cache/live-driver-695a8f2-matrix.json`: complete four-target results.
- `.cache/live-driver-695a8f2-regressions.log`: range and migration failures.
- `.cache/live-driver-695a8f2-stored-values.log`: independent stored-value probe.
- `.cache/oracle-double-readback.go`: GORM-free probe and owned-table cleanup.
- `.cache/live-oracle-failure-probe-20261023.log`: original v0.1.2 controls.

During the original 695a8f2 investigation the driver working tree remained clean
and the consumer dependency remained v0.1.2. The current consumer dependency is
v0.1.4 as recorded above; no driver implementation was changed or committed here.
