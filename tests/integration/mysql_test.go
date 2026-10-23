//go:build integration

// Package integration holds tests that require a real OceanBase tenant.
// Run them explicitly; without a DSN every case skips instead of passing.
package integration

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

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

// openMySQLProfile opens the same tenant with an explicit MySQL client
// profile, used to compare the TIME fraction behaviour of profiles 5 and 8.
func openMySQLProfile(t *testing.T, profile int) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(oceanbase.New(oceanbase.Config{
		DSN:                    mysqlDSN(t),
		CompatibleMysqlVersion: profile,
	}), &gorm.Config{TranslateError: true})
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

// mysqlPrecisionRow pins explicit column precisions; the round trip verifies
// ParseTime time.Time scanning plus fraction truncation per column precision.
type mysqlPrecisionRow struct {
	ID    uint      `gorm:"primaryKey"`
	Milli time.Time `gorm:"type:datetime(3)"`
	Micro time.Time `gorm:"type:datetime(6)"`
}

func TestMySQLDatetimePrecisionRoundTrip(t *testing.T) {
	db := openMySQL(t)
	if err := db.AutoMigrate(&mysqlPrecisionRow{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&mysqlPrecisionRow{}) })

	stamp := time.Date(2024, 6, 1, 12, 30, 45, 123456789, time.UTC)
	row := mysqlPrecisionRow{Milli: stamp, Micro: stamp}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}

	var found mysqlPrecisionRow
	if err := db.First(&found, row.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if !found.Milli.Equal(stamp.Truncate(time.Millisecond)) {
		t.Fatalf("datetime(3) = %v, want %v", found.Milli, stamp.Truncate(time.Millisecond))
	}
	if !found.Micro.Equal(stamp.Truncate(time.Microsecond)) {
		t.Fatalf("datetime(6) = %v, want %v", found.Micro, stamp.Truncate(time.Microsecond))
	}
}

// mysqlTimeSlot uses a TIME column; the fraction of the scanned time.Time
// differs between MySQL client profiles (design.md §4.2, R6).
type mysqlTimeSlot struct {
	ID    uint      `gorm:"primaryKey"`
	Start time.Time `gorm:"type:time(3)"`
}

func TestMySQLTimeFractionProfileDiff(t *testing.T) {
	db8 := openMySQLProfile(t, 8)
	if err := db8.AutoMigrate(&mysqlTimeSlot{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db8.Migrator().DropTable(&mysqlTimeSlot{}) })

	if err := db8.Exec("INSERT INTO mysql_time_slots (start) VALUES (?)", "12:30:45.123").Error; err != nil {
		t.Fatalf("insert time literal: %v", err)
	}

	// The stored fraction is .123 on the server regardless of the client
	// profile, so a missing fraction below is a client conversion result.
	var storedFraction string
	if err := db8.Raw("SELECT TIME_FORMAT(start, '%f') FROM mysql_time_slots LIMIT 1").Scan(&storedFraction).Error; err != nil {
		t.Fatalf("TIME_FORMAT: %v", err)
	}
	if storedFraction != "123000" {
		t.Fatalf("stored TIME fraction = %q, want 123000", storedFraction)
	}

	var slot8 mysqlTimeSlot
	if err := db8.First(&slot8).Error; err != nil {
		t.Fatalf("First (profile 8): %v", err)
	}
	if slot8.Start.Nanosecond() != 123*int(time.Millisecond) {
		t.Fatalf("profile 8 nanos = %d, want 123000000 (fraction kept)", slot8.Start.Nanosecond())
	}

	db5 := openMySQLProfile(t, 5)
	var slot5 mysqlTimeSlot
	if err := db5.First(&slot5).Error; err != nil {
		t.Fatalf("First (profile 5): %v", err)
	}
	if slot5.Start.Nanosecond() != 0 {
		t.Fatalf("profile 5 nanos = %d, want 0 (fraction truncated)", slot5.Start.Nanosecond())
	}
}

func TestMySQLConcurrentAccess(t *testing.T) {
	db := openMySQL(t)
	if err := db.AutoMigrate(&user{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&user{}) })

	const workers, perWorker = 8, 10
	var (
		wg   sync.WaitGroup
		errs = make(chan error, workers)
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				row := user{Name: fmt.Sprintf("concurrent-%d-%d", worker, j)}
				if err := db.Create(&row).Error; err != nil {
					errs <- fmt.Errorf("worker %d create: %w", worker, err)
					return
				}
				if err := db.Model(&user{}).Where("id = ?", row.ID).
					Update("name", fmt.Sprintf("concurrent-%d-%d-done", worker, j)).Error; err != nil {
					errs <- fmt.Errorf("worker %d update: %w", worker, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var count int64
	if err := db.Model(&user{}).Where("name LIKE ?", "concurrent-%").Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != workers*perWorker {
		t.Fatalf("concurrent rows = %d, want %d", count, workers*perWorker)
	}
}

// migrateUserV1/V2 share one table; V2 adds a column to exercise the
// incremental side of AutoMigrate.
type migrateUserV1 struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:64"`
}

func (migrateUserV1) TableName() string { return "mysql_migrate_users" }

type migrateUserV2 struct {
	migrateUserV1
	Email string `gorm:"size:128"`
}

func (migrateUserV2) TableName() string { return "mysql_migrate_users" }

func TestMySQLAutoMigrateIncremental(t *testing.T) {
	db := openMySQL(t)
	if err := db.AutoMigrate(&migrateUserV1{}); err != nil {
		t.Fatalf("AutoMigrate v1: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&migrateUserV2{}) })

	created := migrateUserV1{Name: integrationUserName}
	if err := db.Create(&created).Error; err != nil {
		t.Fatalf("Create v1: %v", err)
	}

	if err := db.AutoMigrate(&migrateUserV2{}); err != nil {
		t.Fatalf("AutoMigrate v2: %v", err)
	}
	if !db.Migrator().HasColumn(&migrateUserV2{}, "email") {
		t.Fatal("incremental AutoMigrate must add the email column")
	}

	if err := db.Model(&migrateUserV2{}).Where("id = ?", created.ID).
		Update("email", "alice@example.com").Error; err != nil {
		t.Fatalf("Update email: %v", err)
	}
	var found migrateUserV2
	if err := db.First(&found, created.ID).Error; err != nil {
		t.Fatalf("First v2: %v", err)
	}
	if found.Name != integrationUserName || found.Email != "alice@example.com" {
		t.Fatalf("row after incremental migrate = %+v", found)
	}
}

type mysqlCompany struct {
	ID    uint
	Name  string
	Tasks []mysqlTask `gorm:"foreignKey:CompanyID"`
}

type mysqlEmployee struct {
	ID        uint
	Name      string
	CompanyID *uint
	Company   *mysqlCompany `gorm:"foreignKey:CompanyID"`
}

type mysqlTask struct {
	ID        uint
	Title     string
	CompanyID uint
	Company   *mysqlCompany `gorm:"foreignKey:CompanyID"`
}

type mysqlSpeaker struct {
	ID        uint
	Name      string
	Languages []mysqlLanguage `gorm:"many2many:mysql_speaker_languages"`
}

type mysqlLanguage struct {
	ID   uint
	Name string
}

func TestMySQLAssociations(t *testing.T) {
	db := openMySQL(t)
	if err := db.AutoMigrate(
		&mysqlCompany{}, &mysqlEmployee{}, &mysqlTask{},
		&mysqlSpeaker{}, &mysqlLanguage{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Migrator().DropTable(&mysqlEmployee{}, &mysqlTask{},
			&mysqlSpeaker{}, &mysqlLanguage{}, &mysqlCompany{})
		_ = db.Exec("DROP TABLE IF EXISTS mysql_speaker_languages").Error
	})

	// belongs-to: the company key must be backfilled before the foreign key.
	employee := mysqlEmployee{Name: integrationUserName, Company: &mysqlCompany{Name: "acme"}}
	if err := db.Create(&employee).Error; err != nil {
		t.Fatalf("Create belongs-to: %v", err)
	}
	if employee.Company == nil || employee.Company.ID == 0 ||
		employee.CompanyID == nil || *employee.CompanyID != employee.Company.ID {
		t.Fatalf("belongs-to keys not backfilled: %+v", employee)
	}

	// has-many: fresh children carry the parent key.
	company := mysqlCompany{Name: "globex", Tasks: []mysqlTask{{Title: "t1"}, {Title: "t2"}}}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("Create has-many: %v", err)
	}
	for _, task := range company.Tasks {
		if task.CompanyID != company.ID {
			t.Fatalf("task foreign key = %d, want %d", task.CompanyID, company.ID)
		}
	}

	// many-to-many: the join table is written after both keys exist.
	speaker := mysqlSpeaker{Name: "bob", Languages: []mysqlLanguage{{Name: "go"}, {Name: "sql"}}}
	if err := db.Create(&speaker).Error; err != nil {
		t.Fatalf("Create many2many: %v", err)
	}
	var joinCount int64
	if err := db.Table("mysql_speaker_languages").
		Where("mysql_speaker_id = ?", speaker.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("join table count: %v", err)
	}
	if joinCount != 2 {
		t.Fatalf("join rows = %d, want 2", joinCount)
	}

	// Preload reads the associations back.
	var loaded mysqlSpeaker
	if err := db.Preload("Languages").First(&loaded, speaker.ID).Error; err != nil {
		t.Fatalf("Preload First: %v", err)
	}
	if len(loaded.Languages) != 2 {
		t.Fatalf("preloaded languages = %d, want 2", len(loaded.Languages))
	}
}
