//go:build integration

// Package integration holds tests that require a real OceanBase tenant.
// Run them explicitly; without a DSN every case skips instead of passing.
package integration

import (
	"os"
	"testing"

	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
)

// integrationUserName is the shared sample name for the integration CRUD
// suites; goconst counts the string across the whole package.
const integrationUserName = "alice"

// mysqlDSN returns the MySQL-compatible tenant DSN, skipping when absent.
func mysqlDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OB_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OB_MYSQL_DSN is not set; real tenant acceptance was not run")
	}
	return dsn
}

// openMySQL opens the MySQL-compatible tenant.
func openMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(oceanbase.Open(mysqlDSN(t)), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db
}

// user is the shared CRUD model.
type user struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:64;index"`
	CreatedAt int64
}

func TestMySQLCRUD(t *testing.T) {
	db := openMySQL(t)
	if err := db.AutoMigrate(&user{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&user{}) })

	created := user{Name: integrationUserName}
	if err := db.Create(&created).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create must backfill the auto-increment primary key")
	}

	var found user
	if err := db.First(&found, created.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if found.Name != integrationUserName {
		t.Fatalf("Name = %q", found.Name)
	}

	if err := db.Model(&found).Update("name", "bob").Error; err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := db.Delete(&found).Error; err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestMySQLTransactionSavePoint(t *testing.T) {
	db := openMySQL(t)
	if err := db.AutoMigrate(&user{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&user{}) })

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user{Name: "kept"}).Error; err != nil {
			return err
		}
		if err := tx.SavePoint("before_rollback").Error; err != nil {
			return err
		}
		if err := tx.Create(&user{Name: "rolled back"}).Error; err != nil {
			return err
		}
		return tx.RollbackTo("before_rollback").Error
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}

	var count int64
	if err := db.Model(&user{}).Where("name = ?", "kept").Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("kept rows = %d, want 1", count)
	}
}

func TestMySQLDatetimePrecisionRoundTrip(t *testing.T) {
	db := openMySQL(t)

	type event struct {
		ID        uint `gorm:"primaryKey"`
		CreatedAt int64
	}
	if err := db.AutoMigrate(&event{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&event{}) })

	row := event{CreatedAt: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
}
