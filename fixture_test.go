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
)
