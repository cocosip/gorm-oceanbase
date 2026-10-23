package main

import (
	"errors"
	"reflect"
	"strconv"

	"github.com/cocosip/go-oceanbase/dberrors"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func constraintError(err error, mode string, mysqlCode, oracleCode uint32) error {
	want := mysqlCode
	if mode == modeOracle {
		want = oracleCode
	}
	var server *dberrors.Error
	if errors.As(err, &server) && server.Code == want {
		return nil
	}
	if err != nil {
		return err
	}
	return checkFailure("constraint accepted invalid value")
}

type foreignParent struct {
	ID int64 `gorm:"primaryKey;autoIncrement:false"`
}
type foreignChild struct {
	ID       int64 `gorm:"primaryKey;autoIncrement:false"`
	ParentID int64
	Parent   foreignParent
}

func runForeignKey(s *scenario) error {
	db := s.db.Session(&gorm.Session{NewDB: true})
	db.NamingStrategy = schema.NamingStrategy{TablePrefix: s.prefix + "_", SingularTable: true, IdentifierMaxLength: 30}
	for _, model := range []any{&foreignParent{}, &foreignChild{}} {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return err
		}
		if err := s.createNamedTable(db, stmt.Schema.Table, model); err != nil {
			return err
		}
	}
	if err := db.Migrator().CreateConstraint(&foreignChild{}, "Parent"); err != nil {
		return err
	}
	if !db.Migrator().HasConstraint(&foreignChild{}, "Parent") {
		return checkFailure("created foreign key missing")
	}
	if err := db.Create(&foreignParent{ID: 1}).Error; err != nil {
		return err
	}
	if err := db.Omit("Parent").Create(&foreignChild{ID: 1, ParentID: 1}).Error; err != nil {
		return err
	}
	err := db.Omit("Parent").Create(&foreignChild{ID: 2, ParentID: 2}).Error
	if !errors.Is(err, gorm.ErrForeignKeyViolated) {
		return checkFailure("foreign key violation not translated")
	}
	if err = db.Migrator().DropConstraint(&foreignChild{}, "Parent"); err != nil {
		return err
	}
	stmt := &gorm.Statement{DB: db}
	if err = stmt.Parse(&foreignChild{}); err != nil {
		return err
	}
	constraint := stmt.Schema.Relationships.Relations["Parent"].ParseConstraint()
	check := &scenario{db: db, mode: s.mode}
	return check.assertAbsent("constraint", stmt.Schema.Table, constraint.Name)
}

func runCheckConstraint(s *scenario) error {
	type row struct {
		ID    int64 `gorm:"primaryKey;autoIncrement:false"`
		Score int
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	name := s.name()
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "ID", Type: reflect.TypeFor[int64](), Tag: `gorm:"primaryKey;autoIncrement:false"`},
		{Name: "Score", Type: reflect.TypeFor[int](), Tag: reflect.StructTag("gorm:" + strconv.Quote("check:"+name+","+s.quote("score")+" >= 0"))},
	})
	model := reflect.New(typ).Interface()
	m := s.db.Table(table).Migrator()
	if err = m.CreateConstraint(model, name); err != nil {
		return err
	}
	if !m.HasConstraint(model, name) {
		return checkFailure("created check constraint missing")
	}
	if err = s.db.Table(table).Create(&row{ID: 1, Score: 1}).Error; err != nil {
		return err
	}
	if err = constraintError(s.db.Table(table).Create(&row{ID: 2, Score: -1}).Error, s.mode, 3819, 2290); err != nil {
		return err
	}
	if err = m.DropConstraint(model, name); err != nil {
		return err
	}
	return s.assertAbsent("constraint", table, name)
}
