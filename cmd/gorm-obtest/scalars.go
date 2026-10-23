package main

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"time"

	obtypes "github.com/cocosip/go-oceanbase/types"
	"gorm.io/gorm/clause"
)

const unicodeSample = "中文测试-é-🙂"
const decimalSample = "12345678901234567890.123456789"

type scalarField struct {
	name, mode, tag string
	typ             reflect.Type
	values          []any
	compare         func(any, any) bool
}

func scalar[T any](name, mode, tag string, values ...T) scalarField {
	field := scalarField{name: name, mode: mode, tag: tag, typ: reflect.TypeFor[T]()}
	for _, value := range values {
		field.values = append(field.values, value)
	}
	return field
}

func scalarFields() []scalarField {
	stamp := time.Date(2024, 6, 1, 12, 30, 45, 123456000, time.UTC)
	fields := []scalarField{
		scalar("bool", modeBoth, "", false, true),
		scalar("int", modeBoth, "", math.MinInt, 0, math.MaxInt),
		scalar("int8", modeBoth, "", int8(math.MinInt8), int8(0), int8(math.MaxInt8)),
		scalar("int16", modeBoth, "", int16(math.MinInt16), int16(0), int16(math.MaxInt16)),
		scalar("int32", modeBoth, "", int32(math.MinInt32), int32(0), int32(math.MaxInt32)),
		scalar("int64", modeBoth, "", int64(math.MinInt64), int64(0), int64(math.MaxInt64)),
		scalar("uint", modeBoth, "", uint(0), uint(math.MaxUint)),
		scalar("uint8", modeBoth, "", uint8(0), uint8(math.MaxUint8)),
		scalar("uint16", modeBoth, "", uint16(0), uint16(math.MaxUint16)),
		scalar("uint32", modeBoth, "", uint32(0), uint32(math.MaxUint32)),
		scalar("uint64", modeBoth, "", uint64(0), uint64(math.MaxUint64)),
		scalar("float32", modeBoth, "", float32(-1.5), float32(0), float32(1.5), float32(math.MaxFloat32)),
		scalar("float64", modeBoth, "", -1.5, 0.0, 1.5, math.MaxFloat64),
		scalar("string", modeBoth, "", "plain", unicodeSample),
		scalar("varchar", modeBoth, "size:64", "plain", unicodeSample, strings.Repeat("x", 64)),
		scalar("long_string", modeBoth, "size:5001", strings.Repeat("L", 5001)),
		scalar("bytes", modeBoth, "", []byte{0, 1, 127, 128, 255}, bytes.Repeat([]byte{0, 255}, 4096)),
		scalar("bounded_bytes", modeBoth, "size:32", []byte{0, 255}, bytes.Repeat([]byte{127}, 32)),
		scalar("time_seconds", modeBoth, "precision:0", stamp.Truncate(time.Second)),
		scalar("time_millis", modeBoth, "precision:3", stamp.Truncate(time.Millisecond)),
		scalar("time_micros", modeBoth, "precision:6", stamp),
		scalar("null_bool", modeBoth, "", sql.NullBool{}, sql.NullBool{Bool: true, Valid: true}),
		scalar("null_int16", modeBoth, "", sql.NullInt16{}, sql.NullInt16{Int16: math.MinInt16, Valid: true}),
		scalar("null_int32", modeBoth, "", sql.NullInt32{}, sql.NullInt32{Int32: math.MaxInt32, Valid: true}),
		scalar("null_int64", modeBoth, "", sql.NullInt64{}, sql.NullInt64{Int64: math.MaxInt64, Valid: true}),
		scalar("null_float64", modeBoth, "", sql.NullFloat64{}, sql.NullFloat64{Float64: 1.5, Valid: true}),
		scalar("null_string", modeBoth, "size:64", sql.NullString{}, sql.NullString{String: unicodeSample, Valid: true}),
		scalar("null_time", modeBoth, "precision:6", sql.NullTime{}, sql.NullTime{Time: stamp, Valid: true}),
		scalar("mysql_decimal", modeMySQL, "type:decimal(38,9)", obtypes.Number(decimalSample), obtypes.Number("-0.000000001")),
		scalar("mysql_date", modeMySQL, "type:date", stamp.Truncate(24*time.Hour)),
		scalar("mysql_datetime", modeMySQL, "type:datetime(6)", stamp),
		scalar("mysql_timestamp", modeMySQL, "type:timestamp(6)", stamp),
		scalar("mysql_time", modeMySQL, "type:time(6)", "12:30:45.123456", "23:59:59.999999", "-25:00:00.123456", "838:59:58.999999"),
		scalar("mysql_year", modeMySQL, "type:year", int16(1970), int16(2024)),
		scalar("mysql_char", modeMySQL, "type:char(8)", "ascii"),
		scalar("mysql_text", modeMySQL, "type:text", unicodeSample, strings.Repeat("t", 10000)),
		scalar("mysql_mediumtext", modeMySQL, "type:mediumtext", strings.Repeat("m", 70000)),
		scalar("mysql_longtext", modeMySQL, "type:longtext", strings.Repeat("l", 70000)),
		scalar("mysql_varbinary", modeMySQL, "type:varbinary(32)", []byte{0, 255}),
		scalar("mysql_blob", modeMySQL, "type:blob", []byte{0, 255}, bytes.Repeat([]byte{3}, 4096)),
		scalar("mysql_enum", modeMySQL, "type:enum('red','green','blue')", "red", "blue"),
		scalar("mysql_set", modeMySQL, "type:set('red','green','blue')", "red", "red,blue"),
		scalar("mysql_json", modeMySQL, "type:json", `{"name":"test","n":123,"nested":[true,null]}`),
		scalar("oracle_number", modeOracle, "type:NUMBER(38,9)", obtypes.Number(decimalSample), obtypes.Number("-0.000000001")),
		scalar("oracle_date", modeOracle, "type:DATE", stamp.Truncate(time.Second)),
		scalar("oracle_timestamp9", modeOracle, "type:TIMESTAMP(9)", stamp.Add(789*time.Nanosecond)),
		scalar("oracle_char", modeOracle, "type:CHAR(8)", "12345678"),
		scalar("oracle_nchar", modeOracle, "type:NCHAR(4)", obtypes.NString("中文测试")),
		scalar("oracle_nvarchar", modeOracle, "type:NVARCHAR2(64)", obtypes.NString("中文测试-é")),
		scalar("oracle_raw", modeOracle, "type:RAW(32)", obtypes.Raw{0, 255}),
		scalar("oracle_blob", modeOracle, "type:BLOB", bytes.Repeat([]byte{0, 255}, 4096)),
		scalar("oracle_clob", modeOracle, "type:CLOB", unicodeSample, strings.Repeat("c", 10000)),
		scalar("oracle_nclob", modeOracle, "type:NCLOB", obtypes.NString(strings.Repeat("中文", 3000))),
		scalar("oracle_interval_ym", modeOracle, "type:INTERVAL YEAR(4) TO MONTH", obtypes.IntervalYM("+0123-04"), obtypes.IntervalYM("-0001-02")),
		scalar("oracle_interval_ds", modeOracle, "type:INTERVAL DAY(4) TO SECOND(9)", obtypes.IntervalDS("+0012 03:04:05.123456789")),
		scalar("oracle_timestamp_tz", modeOracle, "type:TIMESTAMP(9) WITH TIME ZONE", obtypes.TimestampTZ("2024-06-01 12:30:45.123456789 +08:00")),
		scalar("oracle_timestamp_ltz", modeOracle, "type:TIMESTAMP(9) WITH LOCAL TIME ZONE", obtypes.TimestampLTZ("2024-06-01 12:30:45.123456789 +00:00")),
	}
	for i := range fields {
		switch fields[i].name {
		case "mysql_decimal", "oracle_number":
			fields[i].compare = equalDecimal
		case "mysql_json":
			fields[i].compare = equalJSON
		case "oracle_timestamp_tz":
			fields[i].compare = equalTimestampTZ
		case "oracle_timestamp_ltz":
			fields[i].compare = equalTimestampLTZ
		case "oracle_interval_ym", "oracle_interval_ds":
			fields[i].compare = equalInterval
		}
	}
	return fields
}

var intervalNumbers = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

func equalInterval(want, got any) bool {
	normalize := func(value any) string {
		return intervalNumbers.ReplaceAllStringFunc(strings.TrimPrefix(fmt.Sprint(value), "+"), func(part string) string {
			n, _ := new(big.Rat).SetString(part)
			return n.RatString()
		})
	}
	// Driver Scanner/Valuer validates the syntax; padding and fraction scale can change on read.
	return reflect.TypeOf(want) == reflect.TypeOf(got) && normalize(want) == normalize(got)
}

func (f scalarField) modelType(nullable bool) reflect.Type {
	typ := f.typ
	if nullable {
		typ = reflect.PointerTo(typ)
	}
	return reflect.StructOf([]reflect.StructField{
		{Name: "ID", Type: reflect.TypeFor[int64](), Tag: `gorm:"column:id;primaryKey;autoIncrement:false"`},
		{Name: "Value", Type: typ, Tag: reflect.StructTag(`gorm:"column:value;` + f.tag + `"`)},
	})
}

func scalarCase(f scalarField, nullable bool) caseDefinition {
	name := "types." + f.name
	if nullable {
		name += ".pointer_null"
	}
	return caseDefinition{name: name, mode: f.mode, description: "GORM field roundtrip and ColumnTypes: " + f.typ.String() + " " + f.tag, run: func(s *scenario) error {
		modelType := f.modelType(nullable)
		model := reflect.New(modelType).Interface()
		s.stage = "create_table"
		table, err := s.createTable(model)
		if err != nil {
			return err
		}
		s.stage = "column_metadata"
		if err = s.metadata(table, model); err != nil {
			return err
		}
		if err = validateScalarMetadata(s, f); err != nil {
			return err
		}
		values := append([]any(nil), f.values...)
		if nullable {
			values = append([]any{nil}, values...)
		}
		for i, value := range values {
			row := reflect.New(modelType)
			row.Elem().Field(0).SetInt(int64(i + 1))
			if value != nil {
				v := reflect.ValueOf(value)
				if nullable {
					p := reflect.New(f.typ)
					p.Elem().Set(v)
					v = p
				}
				row.Elem().Field(1).Set(v)
			}
			s.stage = fmt.Sprintf("insert_row_%d", i+1)
			if err = s.db.Table(table).Create(row.Interface()).Error; err != nil {
				return err
			}
			got := reflect.New(modelType)
			s.stage = fmt.Sprintf("read_row_%d", i+1)
			if err = s.db.Table(table).First(got.Interface(), int64(i+1)).Error; err != nil {
				return err
			}
			actual := got.Elem().Field(1)
			if nullable {
				if isSQLNull(value) {
					if !actual.IsNil() {
						return checkFailure("SQL NULL became a non-nil pointer")
					}
					continue
				}
				if actual.IsNil() {
					return checkFailure("non-NULL value became a nil pointer")
				}
				actual = actual.Elem()
			}
			compare := f.compare
			if compare == nil {
				compare = equalScalar
			}
			if f.name == "mysql_time" && s.mysqlProfile == 5 {
				value, _, _ = strings.Cut(value.(string), ".")
			}
			s.stage = fmt.Sprintf("compare_row_%d", i+1)
			if !compare(value, actual.Interface()) {
				return checkFailure("field roundtrip changed value or precision")
			}
		}
		return nil
	}}
}

func isSQLNull(value any) bool {
	if value == nil {
		return true
	}
	if valuer, ok := value.(driver.Valuer); ok {
		v, err := valuer.Value()
		return err == nil && v == nil
	}
	return false
}

func validateScalarMetadata(s *scenario, f scalarField) error {
	for _, column := range s.columns {
		if canonical(column.Name) != "VALUE" {
			continue
		}
		if column.Type == "" {
			return checkFailure("database type metadata is empty")
		}
		if normalizeDatabaseType(column.Type) != expectedDatabaseType(f, s.mode) {
			return checkFailure("database column type differs from the field/type tag")
		}
		if column.Nullable == nil || !*column.Nullable {
			return checkFailure("nullable column metadata missing or incorrect")
		}
		if f.tag == "size:64" && (column.Length == nil || *column.Length != 64) {
			return checkFailure("column length metadata differs from size tag")
		}
		if strings.Contains(f.tag, "(38,9)") && (column.Precision == nil || *column.Precision != 38 || column.Scale == nil || *column.Scale != 9) {
			return checkFailure("decimal precision/scale metadata differs from type tag")
		}
		return nil
	}
	return checkFailure("value column missing from metadata")
}

func equalScalar(want, got any) bool {
	if w, ok := want.(time.Time); ok {
		g, ok := got.(time.Time)
		return ok && w.Equal(g)
	}
	if w, ok := want.(sql.NullTime); ok {
		g, valid := got.(sql.NullTime)
		return valid && w.Valid == g.Valid && (!w.Valid || w.Time.Equal(g.Time))
	}
	return reflect.DeepEqual(want, got)
}

var typeParameters = regexp.MustCompile(`\([^)]*\)`)

func normalizeDatabaseType(name string) string {
	return strings.Join(strings.Fields(typeParameters.ReplaceAllString(strings.ToUpper(name), "")), " ")
}

func expectedDatabaseType(f scalarField, mode string) string {
	if strings.HasPrefix(f.tag, "type:") {
		return normalizeDatabaseType(strings.TrimPrefix(f.tag, "type:"))
	}
	if mode == modeOracle {
		switch f.typ.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return "NUMBER"
		case reflect.Float32:
			return "BINARY_FLOAT"
		case reflect.Float64:
			return "BINARY_DOUBLE"
		case reflect.String:
			if f.name == "long_string" {
				return "CLOB"
			}
			return "VARCHAR2"
		case reflect.Slice:
			if f.tag == "size:32" {
				return "RAW"
			}
			return "BLOB"
		}
		switch f.name {
		case "null_bool", "null_int16", "null_int32", "null_int64":
			return "NUMBER"
		case "null_float64":
			return "BINARY_DOUBLE"
		case "null_string":
			return "VARCHAR2"
		default:
			return "TIMESTAMP"
		}
	}
	switch f.typ.Kind() {
	case reflect.Bool, reflect.Int8, reflect.Uint8:
		return "TINYINT"
	case reflect.Int16, reflect.Uint16:
		return "SMALLINT"
	case reflect.Int32, reflect.Uint32:
		return "INT"
	case reflect.Int, reflect.Uint, reflect.Int64, reflect.Uint64:
		return "BIGINT"
	case reflect.Float32:
		return "FLOAT"
	case reflect.Float64:
		return "DOUBLE"
	case reflect.String:
		if f.tag == "" {
			return "LONGTEXT"
		}
		return "VARCHAR"
	case reflect.Slice:
		if f.tag == "size:32" {
			return "VARBINARY"
		}
		return "LONGBLOB"
	}
	switch f.name {
	case "null_bool":
		return "TINYINT"
	case "null_int16":
		return "SMALLINT"
	case "null_int32":
		return "INT"
	case "null_int64":
		return "BIGINT"
	case "null_float64":
		return "DOUBLE"
	case "null_string":
		return "VARCHAR"
	default:
		return "DATETIME"
	}
}
func equalDecimal(want, got any) bool {
	w, ok := new(big.Rat).SetString(string(want.(obtypes.Number)))
	g, valid := new(big.Rat).SetString(string(got.(obtypes.Number)))
	return ok && valid && w.Cmp(g) == 0
}
func equalJSON(want, got any) bool {
	var w, g any
	return json.Unmarshal([]byte(want.(string)), &w) == nil && json.Unmarshal([]byte(got.(string)), &g) == nil && reflect.DeepEqual(w, g)
}
func equalTimestampTZ(want, got any) bool {
	w, err := want.(obtypes.TimestampTZ).Time()
	g, e := got.(obtypes.TimestampTZ).Time()
	return err == nil && e == nil && w.Equal(g)
}
func equalTimestampLTZ(want, got any) bool {
	w, err := want.(obtypes.TimestampLTZ).Time()
	g, e := got.(obtypes.TimestampLTZ).Time()
	return err == nil && e == nil && w.Equal(g)
}

// Empty strings have different SQL semantics in Oracle and deserve a separate assertion.
func runEmptyString(s *scenario) error {
	type row struct {
		ID    int64   `gorm:"primaryKey;autoIncrement:false"`
		Value *string `gorm:"size:32"`
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	value := ""
	r := row{ID: 1, Value: &value}
	if err = s.db.Table(table).Create(&r).Error; err != nil {
		return err
	}
	var got row
	if err = s.db.Table(table).First(&got, 1).Error; err != nil {
		return err
	}
	if s.mode == modeOracle {
		return expect(got.Value == nil, "Oracle empty VARCHAR2 must read as SQL NULL")
	}
	return expect(got.Value != nil && *got.Value == "", "MySQL empty VARCHAR must stay non-NULL")
}

// ROWID is generated by Oracle; never fabricate or insert one.
func runRowID(s *scenario) error {
	type row struct {
		ID int64 `gorm:"primaryKey;autoIncrement:false"`
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	if err = s.db.Table(table).Create(&row{ID: 1}).Error; err != nil {
		return err
	}
	var id obtypes.RowID
	if err = s.db.Raw("SELECT ROWID FROM ? WHERE ? = ?", clause.Table{Name: table}, clause.Column{Name: "id"}, 1).Row().Scan(&id); err != nil {
		return err
	}
	var count int64
	err = s.db.Raw("SELECT COUNT(*) FROM ? WHERE ROWID = ?", clause.Table{Name: table}, id).Scan(&count).Error
	if err != nil {
		return err
	}
	return expect(count == 1 && id != "", "ROWID lookup did not identify inserted row")
}
