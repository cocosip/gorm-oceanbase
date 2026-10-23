package main

import (
	"errors"
	"reflect"
	"strings"
	"time"

	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	changedName  = "changed"
	firstName    = "first"
	nameColumn   = "name"
	retainedName = "preserved"
	companyName  = "company"
)

type generatedRow struct {
	ID     uint
	Name   string `gorm:"size:64"`
	Rating *int   `gorm:"default:(50)"`
}

func validateGeneratedRows(want, got []generatedRow) error {
	if len(want) != len(got) {
		return checkFailure("generated batch rows missing")
	}
	persisted := make(map[uint]string, len(got))
	for _, row := range got {
		persisted[row.ID] = row.Name
	}
	for _, row := range want {
		if name, ok := persisted[row.ID]; !ok || name != row.Name {
			return checkFailure("generated key does not match persisted row")
		}
	}
	return nil
}

func equalIntegerKey(value any, want uint) bool {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() >= 0 && uint64(v.Int()) == uint64(want)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == uint64(want)
	}
	return false
}

func runGeneratedBatch(s *scenario) error {
	table, err := s.createTable(&generatedRow{})
	if err != nil {
		return err
	}
	rows := []generatedRow{{Name: firstName}, {Name: "second"}, {Name: "third"}}
	if err = s.db.Table(table).Create(&rows).Error; err != nil {
		return err
	}
	seen := map[uint]bool{}
	for _, row := range rows {
		if row.ID == 0 || seen[row.ID] {
			return checkFailure("batch generated keys missing or duplicated")
		}
		seen[row.ID] = true
	}
	var found []generatedRow
	if err = s.db.Table(table).Order(clause.Column{Name: "id"}).Find(&found).Error; err != nil {
		return err
	}
	if err = validateGeneratedRows(rows, found); err != nil {
		return err
	}
	for _, row := range found {
		if row.Rating == nil || *row.Rating != 50 {
			return checkFailure("database default not applied")
		}
	}
	input := map[string]any{nameColumn: "explicit-null", "rating": nil}
	if err = s.db.Table(table).Model(&generatedRow{}).Create(input).Error; err != nil {
		return err
	}
	if input["id"] == nil {
		return checkFailure("generated map key not backfilled")
	}
	var nullRow generatedRow
	if err = s.db.Table(table).Where(clause.Eq{Column: clause.Column{Name: nameColumn}, Value: "explicit-null"}).First(&nullRow).Error; err != nil {
		return err
	}
	if nullRow.Rating != nil {
		return checkFailure("explicit NULL was replaced by default")
	}
	if !equalIntegerKey(input["id"], nullRow.ID) {
		return checkFailure("map key does not match persisted row")
	}
	return s.metadata(table, &generatedRow{})
}

func runDefaultOnly(s *scenario) error {
	type key struct{ ID uint }
	table, err := s.createTable(&key{})
	if err != nil {
		return err
	}
	rows := []key{{}, {}, {}}
	if err = s.db.Table(table).Create(&rows).Error; err != nil {
		return err
	}
	seen := map[uint]bool{}
	for _, row := range rows {
		if row.ID == 0 || seen[row.ID] {
			return checkFailure("default-only batch keys missing or duplicated")
		}
		seen[row.ID] = true
	}
	var persisted []key
	if err = s.db.Table(table).Find(&persisted).Error; err != nil {
		return err
	}
	if len(persisted) != len(rows) {
		return checkFailure("default-only batch lost rows")
	}
	for _, row := range persisted {
		if !seen[row.ID] {
			return checkFailure("default-only key does not match persisted row")
		}
	}
	return nil
}

type explicitRow struct {
	ID   int64  `gorm:"primaryKey;autoIncrement:false"`
	Name string `gorm:"size:64"`
	Flag bool
}

func runCRUD(s *scenario) error {
	table, err := s.createTable(&explicitRow{})
	if err != nil {
		return err
	}
	rows := []explicitRow{{ID: 1, Name: firstName, Flag: true}, {ID: 2, Name: "second", Flag: true}, {ID: 3, Name: "third", Flag: false}}
	if err = s.db.Table(table).Create(&rows).Error; err != nil {
		return err
	}
	var found []explicitRow
	if err = s.db.Table(table).Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}}).Offset(1).Limit(1).Find(&found).Error; err != nil {
		return err
	}
	if len(found) != 1 || found[0].ID != 2 {
		return checkFailure("pagination/order returned wrong row")
	}
	update := s.db.Table(table).Where(clause.Eq{Column: clause.Column{Name: "id"}, Value: 2}).Updates(map[string]any{nameColumn: changedName, "flag": false})
	if update.Error != nil {
		return update.Error
	}
	var row explicitRow
	if err = s.db.Table(table).First(&row, 2).Error; err != nil {
		return err
	}
	if row.Name != changedName || row.Flag || update.RowsAffected != 1 {
		return checkFailure("map update lost zero value or affected row count")
	}
	if err = s.db.Table(table).Delete(&row).Error; err != nil {
		return err
	}
	if err = s.db.Table(table).First(&row, 2).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		return checkFailure("deleted row still found or query failed unexpectedly")
	}
	return nil
}

func runUpsert(s *scenario) error {
	table, err := s.createTable(&explicitRow{})
	if err != nil {
		return err
	}
	row := explicitRow{ID: 1, Name: firstName}
	if err = s.db.Table(table).Create(&row).Error; err != nil {
		return err
	}
	row.Name = changedName
	if err = s.db.Table(table).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return err
	}
	row.Name = "ignored"
	if err = s.db.Table(table).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	var found explicitRow
	if err = s.db.Table(table).First(&found, 1).Error; err != nil {
		return err
	}
	return expect(found.Name == changedName, "upsert update/DoNothing semantics changed")
}

func runTransaction(s *scenario) error {
	table, err := s.createTable(&explicitRow{})
	if err != nil {
		return err
	}
	abort := errors.New("intentional rollback")
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(table).Create(&explicitRow{ID: 1, Name: "rolled-back"}).Error; err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		return err
	}
	if err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(table).Create(&explicitRow{ID: 2, Name: "kept"}).Error; err != nil {
			return err
		}
		if err := tx.SavePoint("before_extra").Error; err != nil {
			return err
		}
		if err := tx.Table(table).Create(&explicitRow{ID: 3, Name: "extra"}).Error; err != nil {
			return err
		}
		return tx.RollbackTo("before_extra").Error
	}); err != nil {
		return err
	}
	var rows []explicitRow
	if err = s.db.Table(table).Find(&rows).Error; err != nil {
		return err
	}
	return expect(len(rows) == 1 && rows[0].ID == 2, "rollback/savepoint/commit retained wrong rows")
}

func runIdentifiers(s *scenario) error {
	type row struct {
		ID     int64  `gorm:"primaryKey;autoIncrement:false"`
		Custom string `gorm:"column:customValue;size:64"`
		Quoted string `gorm:"column:\"MiXeD\";size:64"`
	}
	if s.mode == modeMySQL {
		return runMySQLIdentifiers(s)
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	want := row{ID: 1, Custom: "custom-value", Quoted: retainedName}
	if err = s.db.Table(table).Create(&want).Error; err != nil {
		return err
	}
	var got row
	if err = s.db.Table(table).First(&got, 1).Error; err != nil {
		return err
	}
	if got != want {
		return checkFailure("custom or quoted DBName did not scan")
	}
	var scanned row
	if err = s.db.Table(table).Model(&row{}).Scan(&scanned).Error; err != nil {
		return err
	}
	if scanned != want {
		return checkFailure("Model.Scan lost custom columns")
	}
	return s.metadata(table, &row{})
}

func runMySQLIdentifiers(s *scenario) error {
	type row struct {
		ID       int64  `gorm:"primaryKey;autoIncrement:false"`
		Custom   string `gorm:"column:customValue;size:64"`
		Reserved string `gorm:"column:order;size:64"`
	}
	table, err := s.createTable(&row{})
	if err != nil {
		return err
	}
	want := row{ID: 1, Custom: "custom-value", Reserved: "quoted-reserved"}
	if err = s.db.Table(table).Create(&want).Error; err != nil {
		return err
	}
	var got row
	if err = s.db.Table(table).First(&got, 1).Error; err != nil {
		return err
	}
	return expect(want == got, "MySQL custom/reserved column names failed")
}

type associationCompany struct {
	ID   uint
	Name string `gorm:"size:64"`
}
type associationChild struct {
	ID                  uint
	AssociationParentID uint
	Name                string `gorm:"size:64"`
}
type associationProfile struct {
	ID                  uint
	AssociationParentID uint
	Bio                 string `gorm:"size:64"`
}
type associationLanguage struct {
	ID   uint
	Name string `gorm:"size:64"`
}
type associationParent struct {
	ID        uint
	Name      string `gorm:"size:64"`
	CompanyID uint
	Company   associationCompany `gorm:"foreignKey:CompanyID"`
	Children  []associationChild
	Profile   associationProfile
	Languages []associationLanguage `gorm:"many2many:links"`
}

func runAssociations(s *scenario) error {
	db := s.db.Session(&gorm.Session{NewDB: true})
	db.NamingStrategy = schema.NamingStrategy{TablePrefix: s.prefix + "_", SingularTable: true, IdentifierMaxLength: 30}
	models := []any{&associationCompany{}, &associationParent{}, &associationChild{}, &associationProfile{}, &associationLanguage{}}
	for _, model := range models {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return err
		}
		if err := s.createNamedTable(db, stmt.Schema.Table, model); err != nil {
			return err
		}
	}
	// The association tag applies the run prefix to the join table too.
	join := db.NamingStrategy.JoinTableName("links")
	type link struct {
		AssociationParentID   uint `gorm:"primaryKey;autoIncrement:false"`
		AssociationLanguageID uint `gorm:"primaryKey;autoIncrement:false"`
	}
	if err := s.createNamedTable(db, join, &link{}); err != nil {
		return err
	}
	parent := associationParent{Name: "parent", Company: associationCompany{Name: companyName}, Children: []associationChild{{Name: "child"}}, Profile: associationProfile{Bio: "profile"}, Languages: []associationLanguage{{Name: "language"}}}
	if err := db.Create(&parent).Error; err != nil {
		return err
	}
	if parent.ID == 0 || parent.CompanyID == 0 || len(parent.Children) != 1 || parent.Children[0].AssociationParentID != parent.ID || parent.Profile.AssociationParentID != parent.ID {
		return checkFailure("association generated keys/foreign keys not backfilled")
	}
	var got associationParent
	if err := db.Preload("Company").Preload("Children").Preload("Profile").Preload("Languages").First(&got, parent.ID).Error; err != nil {
		return err
	}
	if got.Company.Name != companyName || len(got.Children) != 1 || got.Profile.Bio != "profile" || len(got.Languages) != 1 {
		return checkFailure("association Preload lost related rows")
	}
	got.Profile.Bio = changedName
	if err := db.Session(&gorm.Session{FullSaveAssociations: true}).Save(&got).Error; err != nil {
		return err
	}
	var joined associationParent
	if err := db.Joins("Company").Joins("Profile").First(&joined, parent.ID).Error; err != nil {
		return err
	}
	return expect(joined.Company.Name == companyName && joined.Profile.Bio == changedName, "joined association scan or FullSaveAssociations failed")
}

type hookRow struct {
	ID        uint
	Name      string `gorm:"size:64"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
	Ignored   string `gorm:"-"`
}

//nolint:unparam // GORM hook signature requires error.
func (r *hookRow) BeforeCreate(_ *gorm.DB) error { r.Name = strings.ToUpper(r.Name); return nil }

func runHooksAndSoftDelete(s *scenario) error {
	table, err := s.createTable(&hookRow{})
	if err != nil {
		return err
	}
	row := hookRow{Name: "hook", Ignored: "must-not-be-stored"}
	if err = s.db.Table(table).Create(&row).Error; err != nil {
		return err
	}
	if row.ID == 0 || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		return checkFailure("generated key or automatic timestamps missing")
	}
	var got hookRow
	if err = s.db.Table(table).First(&got, row.ID).Error; err != nil {
		return err
	}
	if got.Name != "HOOK" || got.Ignored != "" {
		return checkFailure("hook or ignored field contract failed")
	}
	if err = s.db.Table(table).Delete(&row).Error; err != nil {
		return err
	}
	err = s.db.Table(table).First(&got, row.ID).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return checkFailure("soft-deleted row remains visible")
	}
	if err = s.db.Table(table).Unscoped().First(&got, row.ID).Error; err != nil {
		return err
	}
	return expect(got.DeletedAt.Valid, "soft delete did not persist DeletedAt")
}

func runOracleUpsertRejection(s *scenario) error {
	table, err := s.createTable(&generatedRow{})
	if err != nil {
		return err
	}
	row := generatedRow{Name: "new"}
	err = s.db.Table(table).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: nameColumn}}, DoNothing: true}).Create(&row).Error
	if !errors.Is(err, oceanbase.ErrUpsertUnsupported) {
		return checkFailure("non-unique MERGE target was not rejected")
	}
	var count int64
	if err = s.db.Table(table).Count(&count).Error; err != nil {
		return err
	}
	return expect(count == 0, "rejected upsert wrote rows")
}
