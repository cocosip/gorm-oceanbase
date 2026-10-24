package oceanbase

import (
	"errors"
	"strings"
	"testing"

	ob "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/dberrors"
	"gorm.io/gorm"
)

func TestParseOBVersion(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		valid   bool
		major   int
		minor   int
		patch   int
		atLeast bool
	}{
		{name: "ob marker", raw: "5.7.25-OceanBase_CE-v4.2.1.0", valid: true, major: 4, minor: 2, patch: 1, atLeast: true},
		{name: "bare version", raw: "4.3.5.1", valid: true, major: 4, minor: 3, patch: 5, atLeast: true},
		{name: "lowercase marker", raw: "oceanbase-v3.2.3", valid: true, major: 3, minor: 2, patch: 3, atLeast: false},
		{name: "mysql prefix only", raw: "5.7.25", valid: true, major: 5, minor: 7, patch: 25, atLeast: true},
		{name: "empty", raw: "", valid: false},
		{name: "garbage", raw: "unknown", valid: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOBVersion(tc.raw)
			if got.valid != tc.valid {
				t.Fatalf("valid = %v, want %v", got.valid, tc.valid)
			}
			if !tc.valid {
				return
			}
			if got.major != tc.major || got.minor != tc.minor || got.patch != tc.patch {
				t.Fatalf("version = %d.%d.%d, want %d.%d.%d", got.major, got.minor, got.patch, tc.major, tc.minor, tc.patch)
			}
			if got.atLeast(4, 2, 1) != tc.atLeast {
				t.Fatalf("atLeast(4,2,1) = %v, want %v", !tc.atLeast, tc.atLeast)
			}
		})
	}
}

func TestMysqlCapabilitiesMatchMySQL57Profile(t *testing.T) {
	caps := mysqlCapabilities()
	if !caps.dontSupportRenameColumn || !caps.dontSupportForShareClause || !caps.dontSupportDropConstraint {
		t.Fatalf("MySQL 5.7-compatible syntax gates missing: %+v", caps)
	}
}

func TestTranslateMySQLCodes(t *testing.T) {
	d := &Dialector{Config: &Config{}}
	cases := []struct {
		name string
		err  error
		want error
	}{
		{name: "duplicate", err: &dberrors.Error{Code: 1062}, want: gorm.ErrDuplicatedKey},
		{name: "fk parent", err: &dberrors.Error{Code: 1451}, want: gorm.ErrForeignKeyViolated},
		{name: "fk child", err: &dberrors.Error{Code: 1452}, want: gorm.ErrForeignKeyViolated},
		{name: "other code", err: &dberrors.Error{Code: 1064}, want: nil},
		{name: "plain error", err: errors.New("boom"), want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Translate(tc.err)
			if tc.want == nil {
				if got != tc.err {
					t.Fatalf("Translate() = %v, want the original error", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("Translate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTranslateWrappedDriverError(t *testing.T) {
	d := &Dialector{Config: &Config{}}
	wrapped := errors.Join(errors.New("context"), &dberrors.Error{Code: 1062})
	if got := d.Translate(wrapped); !errors.Is(got, gorm.ErrDuplicatedKey) {
		t.Fatalf("wrapped duplicate error = %v, want ErrDuplicatedKey", got)
	}
}

func TestDsnHasParam(t *testing.T) {
	cases := []struct {
		dsn  string
		name string
		want bool
	}{
		{dsn: testDSNModeMySQL, name: "mode", want: true},
		{dsn: testDSNNoMode, name: "mode", want: false},
		{dsn: testDSNNoMode + "?parseTime=false", name: "parseTime", want: true},
		{dsn: testDSNModeMySQL, name: "parseTime", want: false},
		{dsn: testDSNNoMode + "?x=%zz", name: "x", want: false},
	}
	for _, tc := range cases {
		if got := dsnHasParam(tc.dsn, tc.name); got != tc.want {
			t.Fatalf("dsnHasParam(%q, %q) = %v, want %v", tc.dsn, tc.name, got, tc.want)
		}
	}
}

func TestQuoteToMySQL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "users", want: "`users`"},
		{in: "db.users", want: "`db`.`users`"},
		{in: "we`ird", want: "`we``ird`"},
		{in: "a.b.c", want: "`a`.`b`.`c`"},
	}
	for _, tc := range cases {
		var builder strings.Builder
		mysqlQuoteTo(&builder, tc.in)
		if builder.String() != tc.want {
			t.Fatalf("mysqlQuoteTo(%q) = %q, want %q", tc.in, builder.String(), tc.want)
		}
	}
}

func TestTranslateOracleCodes(t *testing.T) {
	d := &Dialector{Config: &Config{resolvedMode: ob.ModeOracle}}
	cases := []struct {
		name string
		err  error
		want error
	}{
		{name: "duplicate ORA-00001", err: &dberrors.Error{Code: 1}, want: gorm.ErrDuplicatedKey},
		{name: "fk parent ORA-02291", err: &dberrors.Error{Code: 2291}, want: gorm.ErrForeignKeyViolated},
		{name: "fk child ORA-02292", err: &dberrors.Error{Code: 2292}, want: gorm.ErrForeignKeyViolated},
		{name: "other ORA code", err: &dberrors.Error{Code: 942}, want: nil},
		{name: "mysql duplicate code untouched", err: &dberrors.Error{Code: 1062}, want: nil},
		{name: "plain error", err: errors.New("boom"), want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Translate(tc.err)
			if tc.want == nil {
				if got != tc.err {
					t.Fatalf("Translate() = %v, want the original error", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("Translate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTranslateTablesStayIsolatedPerMode(t *testing.T) {
	mysqlDialector := &Dialector{Config: &Config{resolvedMode: ob.ModeMySQL}}
	oracleDialector := &Dialector{Config: &Config{resolvedMode: ob.ModeOracle}}

	// Code 1 is an ORA duplicate in Oracle mode and untouched in MySQL mode.
	if got := oracleDialector.Translate(&dberrors.Error{Code: 1}); !errors.Is(got, gorm.ErrDuplicatedKey) {
		t.Fatalf("oracle Translate(1) = %v, want ErrDuplicatedKey", got)
	}
	original := error(&dberrors.Error{Code: 1})
	if got := mysqlDialector.Translate(original); got != original {
		t.Fatalf("mysql Translate(1) = %v, want the original error", got)
	}

	// Code 1062 is a MySQL duplicate and untouched in Oracle mode.
	if got := mysqlDialector.Translate(&dberrors.Error{Code: 1062}); !errors.Is(got, gorm.ErrDuplicatedKey) {
		t.Fatalf("mysql Translate(1062) = %v, want ErrDuplicatedKey", got)
	}
	mysqlCode := error(&dberrors.Error{Code: 1062})
	if got := oracleDialector.Translate(mysqlCode); got != mysqlCode {
		t.Fatalf("oracle Translate(1062) = %v, want the original error", got)
	}
}
