package main

import (
	"errors"
	"reflect"
	"strings"

	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func indexModel(index string) any {
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "ID", Type: reflect.TypeFor[int64](), Tag: `gorm:"primaryKey;autoIncrement:false"`},
		{Name: "Code", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(`gorm:"size:32;uniqueIndex:` + index + `"`)},
		{Name: "Region", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(`gorm:"size:32;uniqueIndex:` + index + `"`)},
		{Name: "Name", Type: reflect.TypeFor[string](), Tag: `gorm:"size:64"`},
	})
	return reflect.New(typ).Interface()
}

type uniqueRow struct {
	ID     int64  `gorm:"primaryKey;autoIncrement:false"`
	Code   string `gorm:"size:32"`
	Region string `gorm:"size:32"`
	Name   string `gorm:"size:64"`
}

func runIndexes(s *scenario) error {
	table, err := s.createTable(&uniqueRow{})
	if err != nil {
		return err
	}
	index := s.name()
	model := indexModel(index)
	m := s.db.Table(table).Migrator()
	if err = m.CreateIndex(model, index); err != nil {
		return err
	}
	if !m.HasIndex(model, index) {
		return checkFailure("created index not found")
	}
	indexes, err := m.GetIndexes(model)
	if err != nil {
		return err
	}
	found := false
	for _, idx := range indexes {
		info := indexInfo{Name: idx.Name(), Columns: idx.Columns()}
		if unique, ok := idx.Unique(); ok {
			info.Unique = &unique
		}
		s.indexes = append(s.indexes, info)
		if canonical(idx.Name()) == canonical(index) {
			found = true
			if info.Unique == nil || !*info.Unique || len(info.Columns) != 2 {
				return checkFailure("unique composite index metadata incorrect")
			}
		}
	}
	if !found {
		return checkFailure("GetIndexes omitted created index")
	}
	row := uniqueRow{ID: 1, Code: "c", Region: "r", Name: "old"}
	if err = s.db.Table(table).Create(&row).Error; err != nil {
		return err
	}
	row.ID, row.Name = 2, changedName
	if err = s.db.Table(table).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "region"}, {Name: "code"}}, UpdateAll: true}).Create(&row).Error; err != nil {
		return err
	}
	var got uniqueRow
	if err = s.db.Table(table).First(&got, 1).Error; err != nil {
		return err
	}
	if got.Name != changedName {
		return checkFailure("unique target UpdateAll did not update row")
	}
	row.ID = 3
	err = s.db.Table(table).Create(&row).Error
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		return checkFailure("unique index violation was not translated")
	}
	renamed := s.name()
	if err = m.RenameIndex(model, index, renamed); err != nil {
		return err
	}
	if !m.HasIndex(model, renamed) {
		return checkFailure("index rename not reflected in metadata")
	}
	if err = s.assertAbsent("index", table, index); err != nil {
		return err
	}
	if err = m.DropIndex(model, renamed); err != nil {
		return err
	}
	return s.assertAbsent("index", table, renamed)
}

func runMigration(s *scenario) error {
	type initial struct {
		ID   int64  `gorm:"primaryKey;autoIncrement:false"`
		Name string `gorm:"size:32"`
	}
	type expanded struct {
		ID    int64   `gorm:"primaryKey;autoIncrement:false"`
		Name  string  `gorm:"size:64"`
		Extra *string `gorm:"size:32"`
	}
	table, err := s.createTable(&initial{})
	if err != nil {
		return err
	}
	if err = s.db.Table(table).Create(&initial{ID: 1, Name: retainedName}).Error; err != nil {
		return err
	}
	if err = s.db.Table(table).AutoMigrate(&expanded{}); err != nil {
		return err
	}
	if err = s.db.Table(table).AutoMigrate(&expanded{}); err != nil {
		return err
	}
	if err = s.metadata(table, &expanded{}); err != nil {
		return err
	}
	if len(s.columns) != 3 {
		return checkFailure("incremental migration lost/duplicated columns")
	}
	if err = requireWidenedName(s.columns); err != nil {
		return err
	}
	var row expanded
	if err = s.db.Table(table).First(&row, 1).Error; err != nil {
		return err
	}
	if row.Name != retainedName || row.Extra != nil {
		return checkFailure("migration changed existing data")
	}
	m := s.db.Table(table).Migrator()
	if err = m.RenameColumn(&expanded{}, "Extra", "renamed"); err != nil {
		return err
	}
	if !m.HasColumn(&expanded{}, "renamed") {
		return checkFailure("renamed column missing")
	}
	if err = m.DropColumn(&expanded{}, "renamed"); err != nil {
		return err
	}
	return s.assertAbsent("column", table, "renamed")
}

func requireWidenedName(columns []columnInfo) error {
	for _, column := range columns {
		if canonical(column.Name) == "NAME" {
			return expect(column.Length != nil && *column.Length == 64, "column length metadata differs from expected size")
		}
	}
	return checkFailure("column missing from length metadata")
}

func runFieldConstraints(s *scenario) error {
	type row struct {
		ID       int64   `gorm:"primaryKey;autoIncrement:false"`
		Required string  `gorm:"size:32;not null"`
		Optional *string `gorm:"size:64"`
		Rating   int     `gorm:"default:7"`
		Ignored  string  `gorm:"-"`
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	if err = s.metadata(table, &row{}); err != nil {
		return err
	}
	if len(s.columns) != 4 {
		return checkFailure("ignored field was migrated or a column is missing")
	}
	seen := map[string]columnInfo{}
	for _, column := range s.columns {
		seen[canonical(column.Name)] = column
	}
	required, ok := seen["REQUIRED"]
	if !ok || required.Nullable == nil || *required.Nullable {
		return checkFailure("NOT NULL metadata missing or incorrect")
	}
	optional := seen["OPTIONAL"]
	if optional.Nullable == nil || !*optional.Nullable || optional.Length == nil || *optional.Length != 64 {
		return checkFailure("nullable/length metadata incorrect")
	}
	value := row{ID: 1, Required: "present", Ignored: "ignored"}
	if err = s.db.Table(table).Create(&value).Error; err != nil {
		return err
	}
	var got row
	if err = s.db.Table(table).First(&got, 1).Error; err != nil {
		return err
	}
	if got.Rating != 7 || got.Optional != nil || got.Ignored != "" {
		return checkFailure("literal default/NULL/ignored field roundtrip incorrect")
	}
	err = s.db.Table(table).Model(&row{}).Create(map[string]any{"id": 2, "required": nil}).Error
	return constraintError(err, s.mode, 1048, 1400)
}

func runCompositePrimaryKey(s *scenario) error {
	type row struct {
		A     int64  `gorm:"primaryKey;autoIncrement:false"`
		B     string `gorm:"primaryKey;size:32"`
		Value string `gorm:"size:32"`
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	values := []row{{A: 1, B: "x", Value: "one"}, {A: 1, B: "y", Value: "two"}}
	if err = s.db.Table(table).Create(&values).Error; err != nil {
		return err
	}
	changed := row{A: 1, B: "x", Value: changedName}
	if err = s.db.Table(table).Clauses(clause.OnConflict{UpdateAll: true}).Create(&changed).Error; err != nil {
		return err
	}
	var rows []row
	if err = s.db.Table(table).Order(clause.Column{Name: "b"}).Find(&rows).Error; err != nil {
		return err
	}
	return expect(len(rows) == 2 && rows[0].Value == changedName && rows[1].Value == "two", "composite primary-key upsert matched wrong rows")
}

func runOracleSequence(s *scenario) error {
	name := s.name()
	m, ok := s.db.Migrator().(oceanbase.OracleSequenceMigrator)
	if !ok {
		return checkFailure("Oracle sequence migrator extension missing")
	}
	if err := m.CreateSequence(name); err != nil {
		return err
	}
	s.objects = append(s.objects, name)
	s.cleanups = append(s.cleanups, cleanupAction{name: name, run: func(db *gorm.DB) error { return db.Exec("DROP SEQUENCE ?", clause.Column{Name: name}).Error }})
	if !m.HasSequence(name) {
		return checkFailure("created sequence missing")
	}
	var id int64
	if err := s.db.Raw("SELECT " + s.quote(name) + ".NEXTVAL FROM DUAL").Scan(&id).Error; err != nil {
		return err
	}
	table, err := s.createTable(&explicitRow{})
	if err != nil {
		return err
	}
	row := explicitRow{ID: id, Name: "sequence-key"}
	if err = s.db.Table(table).Create(&row).Error; err != nil {
		return err
	}
	var got explicitRow
	if err = s.db.Table(table).First(&got, id).Error; err != nil {
		return err
	}
	return expect(id > 0 && got.ID == id, "sequence-generated explicit primary key changed")
}

func runComments(s *scenario) error {
	type row struct {
		ID   int64  `gorm:"primaryKey;autoIncrement:false"`
		Note string `gorm:"size:32;comment:acceptance field comment"`
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	if err = s.metadata(table, &row{}); err != nil {
		return err
	}
	for _, column := range s.columns {
		if canonical(column.Name) == "NOTE" {
			return expect(column.Comment != nil && strings.Contains(*column.Comment, "acceptance field comment"), "column comment metadata missing")
		}
	}
	return checkFailure("commented field missing")
}
