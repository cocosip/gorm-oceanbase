# Oracle column support: independent Connector/J comparison

On 2026-10-24, official OceanBase Connector/J **2.4.14** and go-oceanbase
**v0.1.4** rejected the same native NCLOB and nested-table column definitions
against the same direct Oracle tenant, OceanBase **4.4.2.2**. These are server
SQL/storage capability limits, not a Go parameter-binding failure.

## Independent live evidence

The Java probe uses the published, unmodified Maven Central artifact
[com.oceanbase:oceanbase-client:2.4.14](https://repo.maven.apache.org/maven2/com/oceanbase/oceanbase-client/2.4.14/).
JAR SHA-256: `3168e7e3f2f14f35738d7c695e05012bf351f623757c7baada29659d2afa2ad4`.
It runs on Java 21 against the same tenant used by the Go acceptance tool.

Both JDBC Statement and native server PreparedStatement produce:

| SQL operation | Result |
| --- | --- |
| CREATE TABLE with a native NCLOB column | 902 / HY000 / OBE-00902: invalid datatype |
| CREATE TABLE with CLOB | Success |
| CREATE TABLE with NVARCHAR2(64) | Success |
| CREATE TYPE ... AS TABLE OF NUMBER(38,9) | Success |
| CREATE TABLE using that collection as a column | 600 / 0A000 / -4007: nested table type not supported |
| CREATE TABLE with NESTED TABLE ... STORE AS ... | 900 / 42000: syntax error at the storage clause |

The prepared path sets useServerPrepStmts=true, useOraclePrepareExecute=false,
and emulateUnsupportedPstmts=false. Successful control statements assert that
the returned statement class is a server-prepared implementation. Each rejection
checks both the expected code and message; it is diagnostic evidence, not a
successful column roundtrip. All created tables/types are dropped in reverse
order and a final USER_OBJECTS inventory confirms zero owned objects remaining.

The Go probe independently sends the same unparameterized SQL through
database/sql. Its v0.1.4 log records the same errors and successful controls.
This removes GORM SQL generation and floating-point parameter encoding from the
failing paths. Changing the Go driver cannot make these rejected column
definitions valid on this tested server.

## Correct GORM acceptance scope

The regular matrix no longer registers types.oracle_nclob or
types.oracle_collection. Their table definitions were unsuitable for the
supported target. Old configurations selecting either name now fail validation
before connecting; the tool does not send their DDL or silently skip errors.
The unused nested-table column implementation is removed.

The fresh 104-case four-target matrix (ID `0ad4c1c7cea6`) exits 0 with
350 pass / 0 fail / 0 skip / 66 not_applicable. No cleanup errors or owned
objects remain in either schema. The original failing reports are preserved;
they are not reclassified as successful feature coverage.

The supported matrix retains NCHAR/NVARCHAR2, Unicode CLOB, and named OBJECT
columns. NCLOB is not automatically converted to CLOB, and collection columns
are not replaced with JSON. Explicit caller-supplied type tags still pass through
to the server; they are not a promise that every Oracle SQL type is supported.

JDBC's NClob API does not establish native NCLOB column support. The pinned
official [DataNtypeOracleTest](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/test/java/com/oceanbase/jdbc/DataNtypeOracleTest.java#L88)
tests NClob writes against a CLOB column. Similarly, creating a named collection
type or binding a PL/SQL array does not establish collection-column storage.
PL/SQL collection parameters remain outside this GORM column matrix.

## Recorded comparison results

The independent Java comparison was a diagnostic experiment. This Go repository
retains its results only; Java source and the downloaded JAR are not retained.
The recorded output ends with `owned_objects_remaining=0` and
`all_reference_expectations_confirmed`.

Local ignored logs: `.cache/jdbc-compare/oracle-ddl-20261024.log` and
`.cache/live-v014-ddl-diagnosis-20261024.log`. Earlier matrix reports retain their
original failures; the later supported-scope matrix is a separate run.
This conclusion applies to the tested 4.4.2.2 deployment, not every OceanBase
version or every collection use case.
