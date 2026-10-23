package oceanbase

// Shared test fixtures. Keeping literals here avoids repeating them across
// test files and keeps every test readable as a single scenario.
const (
	// testDSNModeMySQL is a well-formed MySQL-mode DSN.
	testDSNModeMySQL = "oceanbase://u:p@h:2881/db?mode=mysql"
	// testDSNNoMode is a well-formed DSN without an explicit mode.
	testDSNNoMode = "oceanbase://u:p@h:2881/db"
	// testOBCEVersion is the version string OceanBase reports on older builds.
	testOBCEVersion = "5.7.25-OceanBase_CE-v4.2.1.0"
	// testVarchar191 is the MySQL utf8mb4 fallback length.
	testVarchar191 = "varchar(191)"
	// testAddr is a well-formed listener address for structured configs.
	testAddr = "127.0.0.1:2881"
	// testExplicitVersion is a bare OceanBase version as ob_version() returns it.
	testExplicitVersion = "4.2.1.0"
	// testUserName is the shared sample user name for CRUD tests.
	testUserName = "alice"
	// testObjectTypeName is a named Oracle compatibility server type used in
	// complex-type tag tests.
	testObjectTypeName = "MY_OBJECT_T"
	// testColumnTitle and testColumnRating are sample column keys for map
	// create and upsert tests.
	testColumnTitle  = "title"
	testColumnRating = "rating"
	// testUpdatedTitle is the upserted title value.
	testUpdatedTitle   = "new"
	testPhysicalName   = "NAME"
	testOracleVarchar2 = "VARCHAR2"
	// oracleReturningTitleSQL is the generated-key INSERT RETURNING statement
	// for a single title column on the oracle articles table.
	oracleReturningTitleSQL = `INSERT INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?) RETURNING "ID" INTO ?`
)

var testOracleColumnNames = []string{testOracleColumnName, "DATA_DEFAULT", "NULLABLE_FLAG", "DATA_TYPE", "CHAR_LENGTH", "DATA_PRECISION", "DATA_SCALE"}
