package main

import (
	"database/sql"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	obtypes "github.com/cocosip/go-oceanbase/types"
	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestIntervalComparisonUsesExactValue(t *testing.T) {
	for _, tc := range []struct {
		want, got any
		equal     bool
	}{
		{obtypes.IntervalYM("+0123-04"), obtypes.IntervalYM("+123-04"), true},
		{obtypes.IntervalYM("+0123-04"), obtypes.IntervalYM("-123-04"), false},
		{obtypes.IntervalDS("+0012 03:04:05.123456789"), obtypes.IntervalDS("+12 03:04:05.123456789"), true},
		{obtypes.IntervalDS("+0012 03:04:05.120000000"), obtypes.IntervalDS("+12 03:04:05.12"), true},
		{obtypes.IntervalDS("+0012 03:04:05.123456789"), obtypes.IntervalDS("+12 03:04:05.123456788"), false},
	} {
		if got := equalInterval(tc.want, tc.got); got != tc.equal {
			t.Fatalf("%v / %v: got %v", tc.want, tc.got, got)
		}
	}
}

func TestClockComparisonPreservesFractionAndIgnoresDate(t *testing.T) {
	want := time.Date(1970, 1, 1, 12, 30, 45, 123456000, time.UTC)
	if !equalClock(want, want.AddDate(20, 0, 0)) || equalClock(want, want.Add(time.Nanosecond)) {
		t.Fatal("TIME comparison changed clock semantics")
	}
}

func TestScalarMatrixParsesBothModes(t *testing.T) {
	dialects := map[ob.Mode]*gorm.DB{}
	for _, mode := range []ob.Mode{ob.ModeMySQL, ob.ModeOracle} {
		modeName := modeMySQL
		if mode == ob.ModeOracle {
			modeName = modeOracle
		}
		db, err := gorm.Open(oceanbase.New(oceanbase.Config{DSN: "oceanbase://offline:offline@127.0.0.1:1/test?mode=" + modeName, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		dialects[mode] = db
	}
	seen := map[string]bool{}
	for _, c := range scalarFields() {
		if seen[c.name] {
			t.Fatalf("duplicate scalar %s", c.name)
		}
		seen[c.name] = true
		for _, mode := range []ob.Mode{ob.ModeMySQL, ob.ModeOracle} {
			if c.mode == modeMySQL && mode != ob.ModeMySQL || c.mode == modeOracle && mode != ob.ModeOracle {
				continue
			}
			model := reflect.New(c.modelType(false)).Interface()
			s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			d := dialects[mode]
			field := s.FieldsByDBName["value"]
			if field == nil || d.DataTypeOf(field) == "" {
				t.Fatalf("%s invalid GORM field", c.name)
			}
			modeName := modeMySQL
			if mode == ob.ModeOracle {
				modeName = modeOracle
			}
			got := normalizeDatabaseType(d.DataTypeOf(field))
			// MySQL declarations include UNSIGNED but information_schema.DATA_TYPE does not.
			got = strings.TrimSuffix(got, " UNSIGNED")
			got = strings.TrimSuffix(got, " NULL")
			if got == "BOOLEAN" {
				got = "TINYINT"
			}
			if want := expectedDatabaseType(c, modeName); got != want {
				t.Fatalf("%s mode=%s type=%s want=%s", c.name, modeName, got, want)
			}
			if len(c.values) == 0 {
				t.Fatalf("%s has no test values", c.name)
			}
		}
	}
	if len(seen) < 40 {
		t.Fatalf("only %d scalar cases", len(seen))
	}
}

func TestBehaviorFixtureSchemas(t *testing.T) {
	for _, model := range []any{&associationParent{}, &foreignChild{}, &generatedRow{}, &explicitRow{}, &hookRow{}, indexModel("test_unique")} {
		if _, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{}); err != nil {
			t.Fatalf("%T: %v", model, err)
		}
	}
}

func TestEveryCaseHasExecutableBody(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range caseDefinitions() {
		if seen[c.name] || c.run == nil || c.description == "" {
			t.Fatalf("invalid case %+v", c)
		}
		seen[c.name] = true
	}
}

func TestPointerInputsDistinguishSQLNull(t *testing.T) {
	for _, value := range []any{nil, sql.NullBool{}, sql.NullString{}, sql.NullInt64{}, sql.NullTime{}} {
		if !isSQLNull(value) {
			t.Fatalf("SQL NULL input %T treated as non-NULL", value)
		}
	}
	for _, value := range []any{false, int64(0), "", sql.NullString{String: "", Valid: true}} {
		if isSQLNull(value) {
			t.Fatalf("non-NULL input %T treated as NULL", value)
		}
	}
}
