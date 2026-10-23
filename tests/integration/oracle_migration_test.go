//go:build integration

package integration

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestOracleMigrationColumnChanges(t *testing.T) {
	type initial struct {
		ID     int64   `gorm:"primaryKey;autoIncrement:false"`
		Amount float64 `gorm:"precision:12;scale:2"`
		Name   string  `gorm:"size:64;not null;default:old"`
	}
	type changed struct {
		ID     int64   `gorm:"primaryKey;autoIncrement:false"`
		Amount float64 `gorm:"precision:14;scale:4"`
		Name   *string `gorm:"size:64;unique"`
	}
	type ordinary struct {
		ID     int64   `gorm:"primaryKey;autoIncrement:false"`
		Amount float64 `gorm:"precision:14;scale:4"`
		Name   *string `gorm:"size:64"`
	}
	const table = "oracle_review_column_changes"
	db := openOracle(t)
	if err := db.Table(table).Migrator().CreateTable(&initial{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
	// Keep ten integer digits when increasing scale; OceanBase rejects narrowing.
	existing := initial{ID: 4, Amount: 1234567890.12}
	if err := db.Table(table).Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.Raw("SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual").Scan(&owner).Error; err != nil {
		t.Fatal(err)
	}
	qualified := db.Table(`"` + strings.ReplaceAll(owner, `"`, `""`) + `".` + table).Session(&gorm.Session{})
	if !qualified.Migrator().HasTable(&initial{}) {
		t.Fatal("qualified table not found")
	}
	for i := 0; i < 2; i++ {
		if err := qualified.AutoMigrate(&changed{}); err != nil {
			t.Fatal(err)
		}
	}
	columns, err := qualified.Migrator().ColumnTypes(&changed{})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		switch column.Name() {
		case "amount":
			if p, s, ok := column.DecimalSize(); !ok || p != 14 || s != 4 {
				t.Fatalf("NUMBER precision/scale=%d,%d known=%v", p, s, ok)
			}
		case "name":
			if nullable, ok := column.Nullable(); !ok || !nullable {
				t.Fatal("NOT NULL was not removed")
			}
			if value, ok := column.DefaultValue(); ok {
				t.Fatalf("default was not removed: %s", value)
			}
		}
	}
	var retained changed
	if err := qualified.First(&retained, existing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.Amount != existing.Amount || retained.Name == nil || *retained.Name != existing.Name {
		t.Fatalf("migration changed existing data: %+v", retained)
	}
	row := changed{ID: 1, Amount: 1.2345}
	if err := qualified.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	var found changed
	if err := qualified.First(&found, 1).Error; err != nil {
		t.Fatal(err)
	}
	if found.Name != nil || found.Amount != row.Amount {
		t.Fatalf("column changes did not persist: %+v", found)
	}
	name := "duplicate"
	if err := qualified.Create(&changed{ID: 2, Name: &name}).Error; err != nil {
		t.Fatal(err)
	}
	if err := qualified.Create(&changed{ID: 3, Name: &name}).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("unique constraint not enforced: %v", err)
	}
	if err := qualified.AutoMigrate(&ordinary{}); err != nil {
		t.Fatal(err)
	}
	if err := qualified.Create(&ordinary{ID: 3, Name: &name}).Error; err != nil {
		t.Fatalf("unique constraint was not removed: %v", err)
	}
}
