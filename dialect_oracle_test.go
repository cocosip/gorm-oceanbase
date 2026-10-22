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
	if createSQL != `INSERT INTO "ORACLE_USERS" ("NAME") VALUES (?)` {
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

	article := oracleArticle{Title: "news", Rating: 8}
	if err := db.Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantExec := `INSERT INTO "ORACLE_ARTICLES" ("TITLE","RATING") VALUES (?,?)`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
}

func TestOracleCreateLiteralDefaultBindsValue(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	article := oracleTaggedArticle{Title: "draft"}
	if err := db.Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A literal default tag binds the default value for a zero field and
	// writes it back to the model (GORM convention).
	wantExec := `INSERT INTO "ORACLE_TAGGED_ARTICLES" ("TITLE","RATING") VALUES (?,?)`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if article.Rating != 50 {
		t.Fatalf("model Rating = %d, want the literal default 50", article.Rating)
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

	article := oracleArticle{Title: "draft"}
	if err := db.Create(&article).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantExec := `INSERT INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?)`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
}

func TestOracleCreateMapNilAndExprNull(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	if err := db.Model(&oracleArticle{}).Create(map[string]interface{}{
		"rating": 5,
		"title":  nil,
	}).Error; err != nil {
		t.Fatalf("Create map: %v", err)
	}
	// Map keys render sorted; a present nil stays an explicit bound NULL.
	wantExec := `INSERT INTO "ORACLE_ARTICLES" ("RATING","TITLE") VALUES (?,?)`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}

	if err := db.Model(&oracleArticle{}).Create(map[string]interface{}{
		"title":  "x",
		"rating": gorm.Expr("NULL"),
	}).Error; err != nil {
		t.Fatalf("Create map with expr: %v", err)
	}
	wantExprExec := `INSERT INTO "ORACLE_ARTICLES" ("RATING","TITLE") VALUES (NULL,?)`
	if !containsExec(pool.server.recordedExecs(), wantExprExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExprExec)
	}
}

func TestOracleCreateBatchInsertAll(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	articles := []oracleArticle{{Title: "a"}, {Title: "b"}}
	tx := db.Create(&articles)
	if tx.Error != nil {
		t.Fatalf("Create slice: %v", tx.Error)
	}
	wantExec := `INSERT ALL INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?) INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?) SELECT 1 FROM DUAL`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
	}
	if tx.RowsAffected != 1 {
		t.Fatalf("RowsAffected = %d, want the fake driver's per-statement 1", tx.RowsAffected)
	}
}

func TestOracleCreateBatchDefaultMarkersOmitted(t *testing.T) {
	db, pool := openOracleArticlesDB(t)

	// Both rows leave the default-tagged field zero: the column is omitted for
	// the whole INSERT ALL instead of being written as NULL.
	articles := []oracleArticle{{Title: "a", Rating: 0}, {Title: "b", Rating: 0}}
	if err := db.Create(&articles).Error; err != nil {
		t.Fatalf("Create slice: %v", err)
	}
	wantExec := `INSERT ALL INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?) INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?) SELECT 1 FROM DUAL`
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

	// Every column falls back to a database default: the record renders the
	// single-column DEFAULT form instead of an empty column list.
	type oracleAllDefault struct {
		ID     uint
		Rating int `gorm:"default:(50)"`
	}
	if err := db.Create(&oracleAllDefault{}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantExec := `INSERT INTO "ORACLE_ALL_DEFAULTS" ("RATING") VALUES (DEFAULT)`
	if !containsExec(pool.server.recordedExecs(), wantExec) {
		t.Fatalf("execs = %v, want %q", pool.server.recordedExecs(), wantExec)
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

func TestOracleCreateRejectsOnConflict(t *testing.T) {	db, pool := openOracleArticlesDB(t)

	err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&oracleArticle{Title: "x"}).Error
	if !errors.Is(err, ErrUpsertUnsupported) {
		t.Fatalf("err = %v, want ErrUpsertUnsupported", err)
	}
	if len(pool.server.recordedExecs()) != 0 {
		t.Fatal("the batch must be rejected before any DML")
	}
}

func TestOracleCreateExecErrorPropagatesAndRollsBack(t *testing.T) {
	db, pool := openOracleArticlesDB(t)
	pool.server.replyExecError(`INSERT INTO "ORACLE_ARTICLES" ("TITLE") VALUES (?)`, errors.New("unique constraint violated"))

	articles := []oracleArticle{{Title: "a", Rating: 8}, {Title: "b"}}
	tx := db.Create(&articles)
	if tx.Error == nil {
		t.Fatal("expected the execution error to propagate")
	}
	exec := pool.server.recordedExecs()
	if !containsExec(exec, `INSERT INTO "ORACLE_ARTICLES" ("TITLE","RATING") VALUES (?,?)`) {
		t.Fatalf("execs = %v, want the first group executed", exec)
	}
	if tx.RowsAffected != 1 {
		t.Fatalf("RowsAffected = %d, want 1 from the first group", tx.RowsAffected)
	}
	tx2 := pool.server.recordedTx()
	if len(tx2) != 2 || tx2[0] != "BEGIN" || tx2[1] != "ROLLBACK" {
		t.Fatalf("tx log = %v, want BEGIN then ROLLBACK", tx2)
	}
}

func TestOracleDryRunCreateMultiGroupSQL(t *testing.T) {
	db, _ := openOracleArticlesDB(t)

	sql := db.Session(&gorm.Session{DryRun: true}).Create(&[]oracleArticle{
		{Title: "a", Rating: 8},
		{Title: "b"},
	}).Statement.SQL.String()

	want := "INSERT INTO \"ORACLE_ARTICLES\" (\"TITLE\",\"RATING\") VALUES (?,?);\n" +
		"INSERT INTO \"ORACLE_ARTICLES\" (\"TITLE\") VALUES (?)"
	if sql != want {
		t.Fatalf("dry run SQL = %q, want %q", sql, want)
	}
}
