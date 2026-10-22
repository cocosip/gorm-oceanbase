package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	obtypes "github.com/cocosip/go-oceanbase/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// openOracleWithFakeDB boots a MySQL-free Oracle-mode gorm.DB against the fake
// server, answering the Oracle version query.
func openOracleWithFakeDB(t *testing.T) (*gorm.DB, *Dialector, *fakePool) {
	t.Helper()
	pool := newFakePool(t)
	pool.server.reply("SELECT ob_version() FROM dual", testOBCEVersion)
	dialector := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeOracle}}
	db, err := gorm.Open(dialector, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db, dialector, pool
}

// stubSessionReader replaces the auto-probe session variable reader and
// restores the original when the test ends.
func stubSessionReader(t *testing.T, fn func(ctx context.Context, cfg ob.Config) (sql.NullString, error)) {
	t.Helper()
	orig := sessionVariableReader
	sessionVariableReader = fn
	t.Cleanup(func() { sessionVariableReader = orig })
}

func TestOracleModeOpensAndProbesVersionFromDual(t *testing.T) {
	db, dialector, pool := openOracleWithFakeDB(t)
	if dialector.resolvedMode != ob.ModeOracle {
		t.Fatalf("resolvedMode = %v, want ModeOracle", dialector.resolvedMode)
	}
	if dialector.ServerVersion != testOBCEVersion {
		t.Fatalf("ServerVersion = %q", dialector.ServerVersion)
	}
	if !containsQuery(pool.server.recordedQueries(), "SELECT ob_version() FROM dual") {
		t.Fatalf("Oracle mode must query ob_version() FROM dual, got %v", pool.server.recordedQueries())
	}
	if db.NowFunc == nil {
		t.Fatal("NowFunc must be installed")
	}
}

func TestOracleNowFuncTruncatesToMicroseconds(t *testing.T) {
	db, _, _ := openOracleWithFakeDB(t)
	if got := db.NowFunc().Nanosecond() % 1e3; got != 0 {
		t.Fatalf("Oracle NowFunc must truncate to microseconds, got nanos=%d", got)
	}

	custom := func() time.Time { return time.Unix(1, 2) }
	pool := newFakePool(t)
	db2, err := gorm.Open(&Dialector{Config: &Config{
		Conn: pool, Mode: ob.ModeOracle, ServerVersion: testExplicitVersion,
	}}, &gorm.Config{NowFunc: custom, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if db2.NowFunc().Nanosecond() != 2 {
		t.Fatal("user NowFunc must not be replaced")
	}
}

func TestOracleModeSkipsMySQLSyntaxGates(t *testing.T) {
	_, dialector, _ := openOracleWithFakeDB(t)
	if dialector.DontSupportForShareClause || dialector.DontSupportRenameColumn || dialector.DontSupportDropConstraint {
		t.Fatalf("MySQL 5.7 gates must not apply to Oracle mode: %+v", dialector.Config)
	}
}

func TestOracleModeDoesNotInjectMySQLProfile(t *testing.T) {
	// A DSN carries the driver's own default profile after parsing; the
	// dialect only has to keep its 8 to itself.
	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=oracle"}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Mode != ob.ModeOracle {
		t.Fatalf("mode = %v, want ModeOracle", cfg.Mode)
	}
	if !cfg.Session.ParseTime {
		t.Fatal("internal connections must enable ParseTime")
	}
	if cfg.Session.CompatibleMysqlVersion == 8 {
		t.Fatal("MySQL profile 8 must not be injected for Oracle mode")
	}

	// A structured config without a profile stays profile-free.
	original := ob.Config{Addr: testAddr, User: "u", Mode: ob.ModeOracle}
	d2 := &Dialector{Config: &Config{DSNConfig: &original}}
	cfg2, err := d2.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg2.Session.CompatibleMysqlVersion != 0 {
		t.Fatalf("MySQL profile must not be injected for Oracle mode, got %d", cfg2.Session.CompatibleMysqlVersion)
	}
	if original.Session.ParseTime {
		t.Fatal("caller DSNConfig must not be modified")
	}
}

func TestAutoProbeResolvesOracle(t *testing.T) {
	var received ob.Config
	stubSessionReader(t, func(_ context.Context, cfg ob.Config) (sql.NullString, error) {
		received = cfg
		return sql.NullString{String: "ORACLE", Valid: true}, nil
	})

	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=auto"}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Mode != ob.ModeOracle || d.resolvedMode != ob.ModeOracle {
		t.Fatalf("mode = %v, want ModeOracle", cfg.Mode)
	}
	if received.Mode != ob.ModeAuto {
		t.Fatalf("probe must run with ModeAuto, got %v", received.Mode)
	}
}

func TestAutoProbeErrorPropagates(t *testing.T) {
	probeErr := errors.New("permission denied")
	stubSessionReader(t, func(context.Context, ob.Config) (sql.NullString, error) {
		return sql.NullString{}, probeErr
	})

	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=auto"}}
	_, err := d.resolveDriverConfig()
	if !errors.Is(err, ErrInvalidConfiguration) || !errors.Is(err, probeErr) {
		t.Fatalf("err = %v, want ErrInvalidConfiguration wrapping the probe error", err)
	}
}

func TestOracleQuoteTo(t *testing.T) {
	d := dialectorWithMode(t, ob.ModeOracle)
	quote := func(str string) string {
		var sb strings.Builder
		d.QuoteTo(&sb, str)
		return sb.String()
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain identifier", in: "users", want: `"USERS"`},
		{name: "snake case", in: "created_at", want: `"CREATED_AT"`},
		{name: "qualified", in: "users.id", want: `"USERS"."ID"`},
		{name: "schema qualified", in: "db.users.id", want: `"DB"."USERS"."ID"`},
		{name: "explicit case preserved", in: `"Foo"`, want: `"Foo"`},
		{name: "explicit part keeps case, rest upper", in: `"Foo".bar`, want: `"Foo"."BAR"`},
		{name: "embedded quote escaped", in: `a"b`, want: `"A""B"`},
		{name: "empty identifier", in: "", want: `"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quote(tc.in); got != tc.want {
				t.Fatalf("QuoteTo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOracleDryRunQuoting(t *testing.T) {
	db, _, _ := openOracleWithFakeDB(t)
	dry := db.Session(&gorm.Session{DryRun: true})

	type oracleUser struct {
		ID   uint   `gorm:"primaryKey"`
		Name string `gorm:"size:64"`
	}

	createSQL := dry.Create(&oracleUser{Name: testUserName}).Statement.SQL.String()
	if createSQL != `INSERT INTO "ORACLE_USERS" ("NAME") VALUES (?) RETURNING "ID" INTO ?` {
		t.Fatalf("create SQL = %q", createSQL)
	}

	querySQL := dry.Model(&oracleUser{}).Where(`"NAME" = ?`, testUserName).Find(&[]oracleUser{}).Statement.SQL.String()
	wantQuery := `SELECT * FROM "ORACLE_USERS" WHERE "NAME" = ?`
	if querySQL != wantQuery {
		t.Fatalf("query SQL = %q, want %q", querySQL, wantQuery)
	}
}

func TestOracleScanMapsUppercaseColumns(t *testing.T) {	pool := newFakePool(t)
	pool.server.reply("SELECT ob_version() FROM dual", testOBCEVersion)

	type oracleRow struct {
		Name string `gorm:"size:64"`
	}

	db, err := gorm.Open(&Dialector{Config: &Config{Conn: pool, Mode: ob.ModeOracle}}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	selectSQL := db.Session(&gorm.Session{DryRun: true}).
		Model(&oracleRow{}).Where(`"NAME" = ?`, testUserName).Find(&[]oracleRow{}).Statement.SQL.String()
	pool.server.replyRows(selectSQL, []string{"NAME"}, [][]driver.Value{{testUserName}})

	var rows []oracleRow
	if err := db.Model(&oracleRow{}).Where(`"NAME" = ?`, testUserName).Find(&rows).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != testUserName {
		t.Fatalf("scan result = %+v, want one row named alice", rows)
	}
}

func TestOracleDryRunLimitClause(t *testing.T) {
	db, _, _ := openOracleWithFakeDB(t)

	type oracleUser struct {
		ID   uint   `gorm:"primaryKey"`
		Name string `gorm:"size:64"`
	}

	query := func(apply func(*gorm.DB) *gorm.DB) string {
		tx := apply(db.Session(&gorm.Session{DryRun: true}).Model(&oracleUser{}).Where(`"NAME" = ?`, testUserName))
		return tx.Find(&[]oracleUser{}).Statement.SQL.String()
	}

	if got := query(func(tx *gorm.DB) *gorm.DB { return tx.Limit(5) }); got != `SELECT * FROM "ORACLE_USERS" WHERE "NAME" = ? FETCH FIRST ? ROWS ONLY` {
		t.Fatalf("limit-only SQL = %q", got)
	}
	if got := query(func(tx *gorm.DB) *gorm.DB { return tx.Offset(2) }); got != `SELECT * FROM "ORACLE_USERS" WHERE "NAME" = ? OFFSET ? ROWS` {
		t.Fatalf("offset-only SQL = %q", got)
	}
	if got := query(func(tx *gorm.DB) *gorm.DB { return tx.Limit(5).Offset(2) }); got != `SELECT * FROM "ORACLE_USERS" WHERE "NAME" = ? OFFSET ? ROWS FETCH FIRST ? ROWS ONLY` {
		t.Fatalf("limit+offset SQL = %q", got)
	}
}

func TestOracleDataTypeOf(t *testing.T) {
	d := dialectorWithMode(t, ob.ModeOracle)

	cases := []struct {
		name  string
		field schema.Field
		want  string
	}{
		{name: "bool", field: schema.Field{DataType: schema.Bool}, want: "NUMBER(1)"},
		{name: "int8", field: schema.Field{DataType: schema.Int, Size: 8}, want: "NUMBER(3,0)"},
		{name: "int16", field: schema.Field{DataType: schema.Int, Size: 16}, want: "NUMBER(5,0)"},
		{name: "int32", field: schema.Field{DataType: schema.Int, Size: 32}, want: "NUMBER(10,0)"},
		{name: "int64", field: schema.Field{DataType: schema.Int, Size: 64}, want: "NUMBER(19,0)"},
		{name: "uint64", field: schema.Field{DataType: schema.Uint, Size: 64}, want: "NUMBER(20,0)"},
		{name: "uint32", field: schema.Field{DataType: schema.Uint, Size: 32}, want: "NUMBER(10,0)"},
		{name: "binary float", field: schema.Field{DataType: schema.Float, Size: 32}, want: "BINARY_FLOAT"},
		{name: "binary double", field: schema.Field{DataType: schema.Float, Size: 64}, want: "BINARY_DOUBLE"},
		{name: "number", field: schema.Field{DataType: schema.Float, Precision: 10, Scale: 2}, want: "NUMBER(10,2)"},
		{name: "default string", field: schema.Field{DataType: schema.String}, want: "VARCHAR2(512)"},
		{name: "sized string", field: schema.Field{DataType: schema.String, Size: 100}, want: "VARCHAR2(100)"},
		{name: "varchar2 boundary", field: schema.Field{DataType: schema.String, Size: 4000}, want: "VARCHAR2(4000)"},
		{name: "clob", field: schema.Field{DataType: schema.String, Size: 4001}, want: "CLOB"},
		{name: "timestamp default", field: schema.Field{DataType: schema.Time}, want: "TIMESTAMP(6)"},
		{name: "timestamp precision", field: schema.Field{DataType: schema.Time, Precision: 3}, want: "TIMESTAMP(3)"},
		{name: "raw", field: schema.Field{DataType: schema.Bytes, Size: 100}, want: "RAW(100)"},
		{name: "raw boundary", field: schema.Field{DataType: schema.Bytes, Size: 2000}, want: "RAW(2000)"},
		{name: "blob oversize", field: schema.Field{DataType: schema.Bytes, Size: 2001}, want: "BLOB"},
		{name: "blob unsized", field: schema.Field{DataType: schema.Bytes}, want: "BLOB"},
		{name: "custom tag passthrough", field: schema.Field{DataType: schema.DataType(testObjectTypeName)}, want: testObjectTypeName},
		{name: "timezone tag passthrough", field: schema.Field{DataType: "TIMESTAMP WITH TIME ZONE"}, want: "TIMESTAMP WITH TIME ZONE"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.DataTypeOf(&tc.field); got != tc.want {
				t.Fatalf("DataTypeOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOracleDataTypeOfDefaultStringSize(t *testing.T) {
	d := &Dialector{Config: &Config{resolvedMode: ob.ModeOracle, DefaultStringSize: 255}}
	if got := d.DataTypeOf(&schema.Field{DataType: schema.String}); got != "VARCHAR2(255)" {
		t.Fatalf("DataTypeOf() = %q, want VARCHAR2(255)", got)
	}
}

func TestOracleComplexTypeNeedsTag(t *testing.T) {
	type complexHolder struct {
		ID     uint64
		Plain  obtypes.Object
		Tagged obtypes.Object `gorm:"type:MY_OBJECT_T"`
	}

	parsed, err := schema.Parse(&complexHolder{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}
	d := dialectorWithMode(t, ob.ModeOracle)

	// Without a tag the driver Object type falls back to GORM's string
	// inference; the dialect must not fabricate a server type from the Go type.
	if got := d.DataTypeOf(parsed.FieldsByDBName["plain"]); got != "VARCHAR2(512)" {
		t.Fatalf("untagged Object DataTypeOf() = %q, want VARCHAR2(512)", got)
	}
	if got := d.DataTypeOf(parsed.FieldsByDBName["tagged"]); got != testObjectTypeName {
		t.Fatalf("tagged Object DataTypeOf() = %q, want %s", got, testObjectTypeName)
	}
}

// oracleArticle is the shared create-callback model: an auto-increment style
// integer primary key plus an expression default-tagged field, which takes the
// database-default marker path.
type oracleArticle struct {
	ID     uint
	Title  string
	Rating int `gorm:"default:(50)"`
}

// oracleTaggedArticle uses a literal default tag, which GORM binds as a value.
type oracleTaggedArticle struct {
	ID     uint
	Title  string
	Rating int `gorm:"default:50"`
}

func openOracleArticlesDB(t *testing.T) (*gorm.DB, *fakePool) {
	t.Helper()
	db, _, pool := openOracleWithFakeDB(t)
	return db, pool
}

func TestOracleCreateSingleRowSQL(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(100)

	article := oracleArticle{Title: "news", Rating: 8}
	if err := db.Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantExec := `INSERT INTO "ORACLE_ARTICLES" ("TITLE","RATING") VALUES (?,?) RETURNING "ID" INTO ?`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if article.ID != 100 {
		t.Fatalf("generated ID = %d, want 100", article.ID)
	}
}

func TestOracleCreateLiteralDefaultBindsValue(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(110)

	article := oracleTaggedArticle{Title: "draft"}
	if err := db.Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A literal default tag binds the default value for a zero field and
	// writes it back to the model (GORM convention).
	wantExec := `INSERT INTO "ORACLE_TAGGED_ARTICLES" ("TITLE","RATING") VALUES (?,?) RETURNING "ID" INTO ?`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if article.Rating != 50 {
		t.Fatalf("model Rating = %d, want the literal default 50", article.Rating)
	}
	if article.ID != 110 {
		t.Fatalf("generated ID = %d, want 110", article.ID)
	}
}

func TestOracleCreateRunsInDefaultTransaction(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	if err := db.Create(&oracleArticle{Title: "news"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	tx := pool.server.recordedTx()
	if len(tx) != 2 || tx[0] != "BEGIN" || tx[1] != "COMMIT" {
		t.Fatalf("tx log = %v, want BEGIN then COMMIT", tx)
	}
}

func TestOracleCreateOmitsZeroDefaultField(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(120)

	article := oracleArticle{Title: "draft"}
	if err := db.Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantExec := oracleReturningTitleSQL
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if article.ID != 120 {
		t.Fatalf("generated ID = %d, want 120", article.ID)
	}
}

func TestOracleCreateMapNilAndExprNull(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(130)

	if err := db.Model(&oracleArticle{}).Create(map[string]interface{}{
		testColumnRating: 5,
		testColumnTitle:  nil,
	}).Error; err != nil {
		t.Fatalf("Create map: %v", err)
	}
	// Map keys render sorted; a present nil stays an explicit bound NULL. The
	// generated key is written back under the column name.
	wantExec := `INSERT INTO "ORACLE_ARTICLES" ("RATING","TITLE") VALUES (?,?) RETURNING "ID" INTO ?`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}

	if err := db.Model(&oracleArticle{}).Create(map[string]interface{}{
		testColumnTitle:  "x",
		testColumnRating: gorm.Expr("NULL"),
	}).Error; err != nil {
		t.Fatalf("Create map with expr: %v", err)
	}
	wantExprExec := `INSERT INTO "ORACLE_ARTICLES" ("RATING","TITLE") VALUES (NULL,?) RETURNING "ID" INTO ?`
	if !containsExec(pool.server.recordedExecs(), wantExprExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExprExec)
	}
}

func TestOracleCreateBatchInsertAll(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	// Provided primary keys keep the multi-row INSERT ALL shape.
	articles := []oracleArticle{{ID: 1, Title: "a"}, {ID: 2, Title: "b"}}
	tx := db.Create(&articles)
	if tx.Error != nil {
		t.Fatalf("Create slice: %v", tx.Error)
	}
	wantExec := `INSERT ALL INTO "ORACLE_ARTICLES" ("TITLE","ID") VALUES (?,?) INTO "ORACLE_ARTICLES" ("TITLE","ID") VALUES (?,?) SELECT 1 FROM DUAL`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if containsExec(pool.server.recordedExecs(), "RETURNING") {
		t.Fatal("provided keys must not take the RETURNING path")
	}
	if tx.RowsAffected != 1 {
		t.Fatalf("RowsAffected = %d, want the fake driver's per-statement 1", tx.RowsAffected)
	}
}

func TestOracleCreateBatchGeneratedKeysRowByRow(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(200)

	// Generated keys are fetched one row at a time; they are never batched
	// into INSERT ALL and never extrapolated (design.md §6.1).
	articles := []oracleArticle{{Title: "a"}, {Title: "b"}}
	if err := db.Create(&articles).Error; err != nil {
		t.Fatalf("Create slice: %v", err)
	}
	if containsExec(pool.server.recordedExecs(), "INSERT ALL") {
		t.Fatal("generated keys must not batch into INSERT ALL")
	}
	wantExec := oracleReturningTitleSQL
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if articles[0].ID != 200 || articles[1].ID != 201 {
		t.Fatalf("generated IDs = %d, %d; want 200, 201", articles[0].ID, articles[1].ID)
	}
}

func TestOracleCreateBatchDefaultMarkersOmitted(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	// Both rows leave the default-tagged field zero: the column is omitted for
	// the whole INSERT ALL instead of being written as NULL.
	articles := []oracleArticle{{ID: 1, Title: "a"}, {ID: 2, Title: "b"}}
	if err := db.Create(&articles).Error; err != nil {
		t.Fatalf("Create slice: %v", err)
	}
	wantExec := `INSERT ALL INTO "ORACLE_ARTICLES" ("TITLE","ID") VALUES (?,?) INTO "ORACLE_ARTICLES" ("TITLE","ID") VALUES (?,?) SELECT 1 FROM DUAL`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if containsExec(pool.server.recordedExecs(), `"RATING"`) {
		t.Fatal("zero default field must not appear in the column list")
	}
}

func TestOracleCreateMixedColumnSetsSplitStatements(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	// The first row provides the default-tagged field, the second does not:
	// adjacent groups with different effective column sets run one by one.
	articles := []oracleArticle{{Title: "a", Rating: 8}, {Title: "b"}}
	if err := db.Create(&articles).Error; err != nil {
		t.Fatalf("Create slice: %v", err)
	}
	exec := pool.server.recordedExecs()
	if !containsExec(exec, `INSERT INTO "ORACLE_ARTICLES" ("TITLE","RATING") VALUES (?,?)`) {
		t.Fatalf("execs = %v, want the rated insert", exec)
	}
	if !containsExec(exec, `INSERT INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?)`) {
		t.Fatalf("execs = %v, want the plain insert", exec)
	}
	if containsExec(exec, "INSERT ALL") {
		t.Fatal("rows with different column sets must not batch into INSERT ALL")
	}
}

func TestOracleCreateAllDefaultRow(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(140)

	// Every column falls back to a database default: the record renders the
	// single-column DEFAULT form, and the generated key still returns.
	type oracleAllDefault struct {
		ID     uint
		Rating int `gorm:"default:(50)"`
	}
	record := oracleAllDefault{}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantExec := `INSERT INTO "ORACLE_ALL_DEFAULTS" ("RATING") VALUES (DEFAULT) RETURNING "ID" INTO ?`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if record.ID != 140 {
		t.Fatalf("generated ID = %d, want 140", record.ID)
	}
}

func TestOracleCreateEmptySliceErrors(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	if err := db.Create(&[]oracleArticle{}).Error; !errors.Is(err, gorm.ErrEmptySlice) {
		t.Fatalf("err = %v, want ErrEmptySlice", err)
	}
	if len(pool.server.recordedExecs()) != 0 {
		t.Fatal("an empty slice must not execute any DML")
	}
}

func TestOracleUpsertGeneratePathUsesInsertReturning(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(300)

	// A new record (zero primary key) upserted by that key takes the INSERT
	// RETURNING path and gets its key before associations would write theirs.
	article := oracleArticle{Title: "x"}
	if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantExec := oracleReturningTitleSQL
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if containsExec(pool.server.recordedExecs(), "MERGE INTO") {
		t.Fatal("the generated key path must not MERGE")
	}
	if article.ID != 300 {
		t.Fatalf("generated ID = %d, want 300", article.ID)
	}
}

func TestOracleCreateExecErrorPropagatesAndRollsBack(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(400)
	pool.server.replyExecError(`INSERT INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?) RETURNING "ID" INTO ?`, errors.New("unique constraint violated"))

	articles := []oracleArticle{{ID: 1, Title: "a", Rating: 8}, {Title: "b"}}
	tx := db.Create(&articles)
	if tx.Error == nil {
		t.Fatal("expected the execution error to propagate")
	}
	exec := pool.server.recordedExecs()
	if !containsExec(exec, `INSERT INTO "ORACLE_ARTICLES" ("TITLE","RATING","ID") VALUES (?,?,?)`) {
		t.Fatalf("execs = %v, want the first row executed", exec)
	}
	if tx.RowsAffected != 1 {
		t.Fatalf("RowsAffected = %d, want 1 from the first row", tx.RowsAffected)
	}
	merges := 0
	for _, e := range exec {
		if containsQuery([]string{e}, "RETURNING") {
			merges++
		}
	}
	if merges != 1 {
		t.Fatalf("the failed RETURNING statement must not be replayed, execs = %v", exec)
	}
	if articles[1].ID != 0 {
		t.Fatalf("failed row must not be backfilled, got ID = %d", articles[1].ID)
	}
	tx2 := pool.server.recordedTx()
	if len(tx2) != 2 || tx2[0] != "BEGIN" || tx2[1] != "ROLLBACK" {
		t.Fatalf("tx log = %v, want BEGIN then ROLLBACK", tx2)
	}
}

func TestOracleDryRunCreateMultiGroupSQL(t *testing.T) {
	db, _ := openOracleArticlesDB(t)

	sql := db.Session(&gorm.Session{DryRun: true}).Create(&[]oracleArticle{
		{ID: 1, Title: "a", Rating: 8},
		{Title: "b"},
	}).Statement.SQL.String()

	want := "INSERT INTO \"ORACLE_ARTICLES\" (\"TITLE\",\"RATING\",\"ID\") VALUES (?,?,?);\n" +
		"INSERT INTO \"ORACLE_ARTICLES\" (\"TITLE\") VALUES (?) RETURNING \"ID\" INTO ?"
	if sql != want {
		t.Fatalf("dry run SQL = %q, want %q", sql, want)
	}
}

func TestOracleUpsertMergeUpdateAllSQL(t *testing.T) {
	db, _ := openOracleArticlesDB(t)

	sql := db.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.OnConflict{UpdateAll: true}).
		Create(&oracleArticle{ID: 7, Title: "x", Rating: 5}).Statement.SQL.String()

	want := `MERGE INTO "ORACLE_ARTICLES" USING (SELECT ? "TITLE", ? "RATING", ? "ID" FROM DUAL) S ` +
		`ON ("S"."ID" = "ORACLE_ARTICLES"."ID") ` +
		`WHEN MATCHED THEN UPDATE SET "ORACLE_ARTICLES"."TITLE" = "S"."TITLE" ` +
		`WHEN NOT MATCHED THEN INSERT ("TITLE","RATING","ID") VALUES ("S"."TITLE", "S"."RATING", "S"."ID")`
	if sql != want {
		t.Fatalf("merge SQL = %q, want %q", sql, want)
	}
}

func TestOracleUpsertMergeDoNothingSQL(t *testing.T) {
	db, _ := openOracleArticlesDB(t)

	sql := db.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&oracleArticle{ID: 7, Title: "x"}).Statement.SQL.String()

	want := `MERGE INTO "ORACLE_ARTICLES" USING (SELECT ? "TITLE", ? "ID" FROM DUAL) S ` +
		`ON ("S"."ID" = "ORACLE_ARTICLES"."ID") ` +
		`WHEN NOT MATCHED THEN INSERT ("TITLE","ID") VALUES ("S"."TITLE", "S"."ID")`
	if sql != want {
		t.Fatalf("merge SQL = %q, want %q", sql, want)
	}
}

func TestOracleUpsertMergeDoUpdatesSQL(t *testing.T) {
	db, _ := openOracleArticlesDB(t)

	sql := db.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{testColumnTitle}),
		}).
		Create(&oracleArticle{ID: 7, Title: testUpdatedTitle}).Statement.SQL.String()

	want := `MERGE INTO "ORACLE_ARTICLES" USING (SELECT ? "TITLE", ? "ID" FROM DUAL) S ` +
		`ON ("S"."ID" = "ORACLE_ARTICLES"."ID") ` +
		`WHEN MATCHED THEN UPDATE SET "ORACLE_ARTICLES"."TITLE" = "S"."TITLE" ` +
		`WHEN NOT MATCHED THEN INSERT ("TITLE","ID") VALUES ("S"."TITLE", "S"."ID")`
	if sql != want {
		t.Fatalf("merge SQL = %q, want %q", sql, want)
	}
}

func TestOracleUpsertMergeConstantAssignmentSQL(t *testing.T) {
	db, _ := openOracleArticlesDB(t)

	sql := db.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.OnConflict{
			DoUpdates: []clause.Assignment{{Column: clause.Column{Name: testColumnTitle}, Value: "fixed"}},
		}).
		Create(&oracleArticle{ID: 7, Title: testUpdatedTitle}).Statement.SQL.String()

	want := `MERGE INTO "ORACLE_ARTICLES" USING (SELECT ? "TITLE", ? "ID" FROM DUAL) S ` +
		`ON ("S"."ID" = "ORACLE_ARTICLES"."ID") ` +
		`WHEN MATCHED THEN UPDATE SET "ORACLE_ARTICLES"."TITLE" = ? ` +
		`WHEN NOT MATCHED THEN INSERT ("TITLE","ID") VALUES ("S"."TITLE", "S"."ID")`
	if sql != want {
		t.Fatalf("merge SQL = %q, want %q", sql, want)
	}
}

func TestOracleUpsertExplicitNonPrimaryKeyTarget(t *testing.T) {
	db, _ := openOracleArticlesDB(t)

	sql := db.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: testColumnTitle}},
			DoNothing: true,
		}).
		Create(&oracleArticle{ID: 7, Title: "dup"}).Statement.SQL.String()

	if !strings.Contains(sql, `ON ("S"."TITLE" = "ORACLE_ARTICLES"."TITLE")`) {
		t.Fatalf("merge SQL must match on the explicit target, got %q", sql)
	}
}

func TestOracleUpsertBatchRunsOneMergePerRow(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	articles := []oracleArticle{{ID: 1, Title: "a"}, {ID: 2, Title: "b"}}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&articles).Error; err != nil {
		t.Fatalf("Create slice: %v", err)
	}
	merges := 0
	for _, exec := range pool.server.recordedExecs() {
		if strings.HasPrefix(exec, "MERGE INTO ") {
			merges++
		}
	}
	if merges != 2 {
		t.Fatalf("merges = %d, want one per row", merges)
	}
}

func TestOracleUpsertRejectsOnColumnUpdate(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	err := db.Clauses(clause.OnConflict{
		DoUpdates: clause.AssignmentColumns([]string{"id", testColumnTitle}),
	}).Create(&oracleArticle{ID: 7, Title: "x"}).Error
	if !errors.Is(err, ErrUpsertUnsupported) {
		t.Fatalf("err = %v, want ErrUpsertUnsupported", err)
	}
	if len(pool.server.recordedExecs()) != 0 {
		t.Fatal("the batch must be rejected before any DML")
	}
}

func TestOracleUpsertRejectsConditionalShapes(t *testing.T) {
	cases := []struct {
		name   string
		clause clause.OnConflict
	}{
		{
			name:   "on constraint",
			clause: clause.OnConflict{OnConstraint: "uni_title", DoNothing: true},
		},
		{
			name: "target where",
			clause: clause.OnConflict{
				TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "region"}, Value: "cn"}}},
				DoNothing:   true,
			},
		},
		{
			name: "update where",
			clause: clause.OnConflict{
				DoUpdates: clause.AssignmentColumns([]string{testColumnTitle}),
				Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: testColumnRating}, Value: 5}}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, pool := openOracleArticlesDB(t)
			err := db.Clauses(tc.clause).Create(&oracleArticle{ID: 7, Title: "x"}).Error
			if !errors.Is(err, ErrUpsertUnsupported) {
				t.Fatalf("err = %v, want ErrUpsertUnsupported", err)
			}
			if len(pool.server.recordedExecs()) != 0 {
				t.Fatal("the batch must be rejected before any DML")
			}
		})
	}
}

func TestOracleUpsertMixedPrimaryKeysSplit(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(500)

	// The first row carries its key and MERGEs; the second takes the
	// generated key path. Both run in input order.
	articles := []oracleArticle{{ID: 1, Title: "old"}, {Title: "new"}}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&articles).Error; err != nil {
		t.Fatalf("Create slice: %v", err)
	}
	exec := pool.server.recordedExecs()
	if !containsExec(exec, "MERGE INTO") {
		t.Fatalf("execs = %v, want the provided-key row to MERGE", exec)
	}
	if !containsExec(exec, oracleReturningTitleSQL) {
		t.Fatalf("execs = %v, want the generated-key row to INSERT RETURNING", exec)
	}
	if articles[1].ID != 500 {
		t.Fatalf("generated ID = %d, want 500", articles[1].ID)
	}
	if articles[0].ID != 1 {
		t.Fatalf("provided ID changed: %d", articles[0].ID)
	}
}

func TestOracleUpsertWithoutTargetErrors(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	type oracleNoPK struct {
		Title string
	}
	err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&oracleNoPK{Title: "x"}).Error
	if !errors.Is(err, ErrUpsertUnsupported) {
		t.Fatalf("err = %v, want ErrUpsertUnsupported", err)
	}
	if len(pool.server.recordedExecs()) != 0 {
		t.Fatal("no DML may run without a conflict target")
	}
}

func TestOracleIdentityVersionGate(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "bare candidate", version: "4.2.1.0", want: true},
		{name: "later release", version: "4.3.1.0", want: true},
		{name: "compatibility prefix", version: testOBCEVersion, want: true},
		{name: "below candidate", version: "4.2.0.0", want: false},
		{name: "older minor", version: "4.1.9.9", want: false},
		{name: "missing", version: "", want: false},
		{name: "no parsable OB version", version: "5.7.25-OceanBase", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dialector{Config: &Config{ServerVersion: tc.version}}
			if got := d.oracleIdentitySupported(); got != tc.want {
				t.Fatalf("oracleIdentitySupported(%q) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}

func TestOracleMigratorSequenceExtensions(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)

	migrator, ok := db.Migrator().(OracleSequenceMigrator)
	if !ok {
		t.Fatal("the oracle migrator must expose the sequence extension")
	}

	if err := migrator.CreateSequence("my_seq"); err != nil {
		t.Fatalf("CreateSequence: %v", err)
	}
	if !containsExec(pool.server.recordedExecs(), `CREATE SEQUENCE "MY_SEQ"`) {
		t.Fatalf("execs = %v, want the quoted CREATE SEQUENCE", pool.server.recordedExecs())
	}

	pool.server.reply(`SELECT count(*) FROM user_sequences WHERE sequence_name = ?`, "1")
	if !migrator.HasSequence("my_seq") {
		t.Fatal("HasSequence must find the seeded sequence")
	}
	pool.server.reply(`SELECT count(*) FROM user_sequences WHERE sequence_name = ?`, "0")
	if migrator.HasSequence("my_seq") {
		t.Fatal("HasSequence must not find an empty result")
	}
}

func TestOracleCreateGeneratedKeyNeverUsesLastInsertId(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(600)

	// The fake driver's LastInsertId always errors; a call would surface here.
	article := oracleArticle{Title: "x"}
	if err := db.Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	if article.ID != 600 {
		t.Fatalf("generated ID = %d, want 600", article.ID)
	}
}

func TestOracleCreateMapSliceGeneratedKeys(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.seedOut(700)

	rows := []map[string]interface{}{
		{testColumnTitle: "a"},
		{testColumnTitle: "b"},
	}
	if err := db.Model(&oracleArticle{}).Create(&rows).Error; err != nil {
		t.Fatalf("Create map slice: %v", err)
	}
	if rows[0]["id"] != uint64(700) || rows[1]["id"] != uint64(701) {
		t.Fatalf("generated map keys = %T %v, %T %v; want uint64 700, 701", rows[0]["id"], rows[0]["id"], rows[1]["id"], rows[1]["id"])
	}
	if containsExec(pool.server.recordedExecs(), "INSERT ALL") {
		t.Fatal("generated keys must not batch into INSERT ALL")
	}
}
