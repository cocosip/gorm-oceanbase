//go:build integration

// Oracle compatibility mode acceptance tests. They require a real OceanBase
// Oracle tenant via OB_ORACLE_DSN; without it every case skips instead of
// passing (skip is never counted as acceptance, design.md §10).
package integration

import (
	"errors"
	"math"
	"os"
	"testing"

	obtypes "github.com/cocosip/go-oceanbase/types"
	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// oracleDSN returns the Oracle-compatible tenant DSN, skipping when absent.
func oracleDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OB_ORACLE_DSN")
	if dsn == "" {
		t.Skip("OB_ORACLE_DSN is not set; real tenant acceptance was not run")
	}
	return dsn
}

// openOracle opens the Oracle-compatible tenant with error translation on.
func openOracle(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(oceanbase.Open(oracleDSN(t)), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db
}

// oracleArticle is the shared CRUD model; ID is an IDENTITY column.
type oracleArticle struct {
	ID     uint
	Title  string
	Rating int `gorm:"default:(50)"`
}

func TestOracleCRUDAndMigrate(t *testing.T) {
	db := openOracle(t)
	if err := db.AutoMigrate(&oracleArticle{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&oracleArticle{}) })

	created := oracleArticle{Title: "news", Rating: 8}
	if err := db.Create(&created).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create must backfill the IDENTITY primary key via RETURNING")
	}

	// Result columns come back upper case and must map onto the model fields.
	var found oracleArticle
	if err := db.First(&found, created.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if found.Title != "news" || found.Rating != 8 {
		t.Fatalf("row = %+v", found)
	}

	if err := db.Model(&found).Update("title", "edited").Error; err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := db.Delete(&found).Error; err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestOracleDefaultAndExplicitNull(t *testing.T) {
	db := openOracle(t)
	if err := db.AutoMigrate(&oracleArticle{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&oracleArticle{}) })

	// A zero value on the default-tagged field takes the database default.
	row := oracleArticle{Title: "defaults"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var found oracleArticle
	if err := db.First(&found, row.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if found.Rating != 50 {
		t.Fatalf("Rating = %d, want the database default 50", found.Rating)
	}

	// A map nil stays an explicit bound NULL, not the default.
	if err := db.Model(&oracleArticle{}).Create(map[string]interface{}{
		"title": "explicit", "rating": nil,
	}).Error; err != nil {
		t.Fatalf("Create map: %v", err)
	}
	var nullRow oracleArticle
	if err := db.Where("title = ?", "explicit").First(&nullRow).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if nullRow.Rating != 0 {
		t.Fatalf("Rating = %d, want the explicit NULL kept as zero", nullRow.Rating)
	}
}

func TestOracleBatchGeneratedKeysRowByRow(t *testing.T) {
	db := openOracle(t)
	if err := db.AutoMigrate(&oracleArticle{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&oracleArticle{}) })

	articles := []oracleArticle{{Title: "a"}, {Title: "b"}, {Title: "c"}}
	if err := db.Create(&articles).Error; err != nil {
		t.Fatalf("Create slice: %v", err)
	}
	seen := map[uint]bool{}
	for i, a := range articles {
		if a.ID == 0 {
			t.Fatalf("row %d did not get a generated key", i)
		}
		if seen[a.ID] {
			t.Fatalf("duplicate generated key %d", a.ID)
		}
		seen[a.ID] = true
	}
}

func TestOracleMergeUpsertBoundaries(t *testing.T) {
	db := openOracle(t)
	if err := db.AutoMigrate(&oracleArticle{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&oracleArticle{}) })

	existing := oracleArticle{ID: 7, Title: "v1", Rating: 1}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Provided key: MERGE upsert updates the row.
	upsert := oracleArticle{ID: 7, Title: "v2", Rating: 2}
	if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&upsert).Error; err != nil {
		t.Fatalf("Merge upsert: %v", err)
	}
	var found oracleArticle
	if err := db.First(&found, 7).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if found.Title != "v2" || found.Rating != 2 {
		t.Fatalf("row after merge = %+v", found)
	}

	// Assignments touching the conflict target are refused, never dropped.
	err := db.Clauses(clause.OnConflict{
		DoUpdates: clause.AssignmentColumns([]string{"id", "title"}),
	}).Create(&oracleArticle{ID: 7, Title: "x"}).Error
	if err == nil {
		t.Fatal("ON-column update must be refused")
	}

	// DoNothing keeps the existing row.
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&oracleArticle{ID: 7, Title: "ignored"}).Error; err != nil {
		t.Fatalf("DoNothing merge: %v", err)
	}
	var after oracleArticle
	if err := db.First(&after, 7).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if after.Title != "v2" {
		t.Fatalf("DoNothing merge overwrote the row: %+v", after)
	}
}

func TestOracleErrorTranslation(t *testing.T) {
	db := openOracle(t)
	if err := db.AutoMigrate(&oracleArticle{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&oracleArticle{}) })

	first := oracleArticle{ID: 42, Title: "dup"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A plain insert of an existing key fails with ORA-00001, which the
	// dialector translates for gorm.Config{TranslateError: true}.
	dup := oracleArticle{ID: 42, Title: "dup"}
	if err := db.Create(&dup).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate insert err = %v, want gorm.ErrDuplicatedKey", err)
	}
}

func TestOracleNumberBoundaries(t *testing.T) {
	db := openOracle(t)

	type wide struct {
		ID       uint
		Signed   int64
		Unsigned uint64
	}
	if err := db.AutoMigrate(&wide{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Migrator().DropTable(&wide{}) })

	row := wide{Signed: math.MaxInt64, Unsigned: math.MaxUint64}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}

	var found wide
	if err := db.First(&found, row.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if found.Signed != math.MaxInt64 || found.Unsigned != math.MaxUint64 {
		t.Fatalf("boundaries lost: %+v", found)
	}
}

func TestOracleObjectRequiresServerPreprocessing(t *testing.T) {
	db := openOracle(t)

	// A named object type cannot be created by AutoMigrate and does not exist
	// on a fresh tenant: the DDL must fail instead of degrading the type.
	type objectHolder struct {
		ID     uint
		Object obtypes.Object `gorm:"type:NO_SUCH_OBJECT_T"`
	}
	if err := db.AutoMigrate(&objectHolder{}); err == nil {
		t.Cleanup(func() { _ = db.Migrator().DropTable(&objectHolder{}) })
		t.Fatal("creating a missing object type must fail, not degrade silently")
	}
}

type oracleCompany struct {
	ID    uint
	Name  string
	Tasks []oracleTask `gorm:"foreignKey:CompanyID"`
}

type oracleEmployee struct {
	ID        uint
	Name      string
	CompanyID *uint
	Company   *oracleCompany `gorm:"foreignKey:CompanyID"`
}

type oracleTask struct {
	ID        uint
	Title     string
	CompanyID uint
	Company   *oracleCompany `gorm:"foreignKey:CompanyID"`
}

type oracleSpeaker struct {
	ID        uint
	Name      string
	Languages []oracleLanguage `gorm:"many2many:oracle_speaker_languages;foreignKey:ID;joinForeignKey:SpeakerID;references:ID;joinReferences:LanguageID"`
}

type oracleLanguage struct {
	ID   uint
	Name string
}

func TestOracleAssociationCreates(t *testing.T) {
	db := openOracle(t)
	if err := db.AutoMigrate(
		&oracleCompany{}, &oracleEmployee{}, &oracleTask{},
		&oracleSpeaker{}, &oracleLanguage{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Migrator().DropTable(&oracleEmployee{}, &oracleTask{},
			&oracleSpeaker{}, &oracleLanguage{}, &oracleCompany{})
		_ = db.Exec("DROP TABLE oracle_speaker_languages").Error
	})

	// belongs-to with a new company: the company key must exist before the
	// foreign key is written.
	employee := oracleEmployee{Name: integrationUserName, Company: &oracleCompany{Name: "acme"}}
	if err := db.Create(&employee).Error; err != nil {
		t.Fatalf("Create belongs-to: %v", err)
	}
	if employee.Company == nil || employee.Company.ID == 0 || employee.CompanyID == nil || *employee.CompanyID != employee.Company.ID {
		t.Fatalf("belongs-to keys not backfilled: %+v", employee)
	}

	// has-many with fresh children.
	company := oracleCompany{Name: "globex", Tasks: []oracleTask{{Title: "t1"}, {Title: "t2"}}}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("Create has-many: %v", err)
	}
	for _, task := range company.Tasks {
		if task.CompanyID != company.ID {
			t.Fatalf("task foreign key = %d, want %d", task.CompanyID, company.ID)
		}
	}

	// many-to-many writes the join table after both keys exist.
	speaker := oracleSpeaker{Name: "bob", Languages: []oracleLanguage{{Name: "go"}, {Name: "sql"}}}
	if err := db.Create(&speaker).Error; err != nil {
		t.Fatalf("Create many2many: %v", err)
	}
	var count int64
	if err := db.Table("oracle_speaker_languages").
		Where("speaker_id = ?", speaker.ID).Count(&count).Error; err != nil {
		t.Fatalf("join table count: %v", err)
	}
	if count != 2 {
		t.Fatalf("join rows = %d, want 2", count)
	}

	// Save(slice) with mixed old and new records splits by primary key state.
	mixed := []oracleCompany{{ID: company.ID, Name: "globex2"}, {Name: "initech"}}
	if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&mixed).Error; err != nil {
		t.Fatalf("mixed Save(slice): %v", err)
	}
	var names []string
	if err := db.Model(&oracleCompany{}).Order("id").Pluck("name", &names).Error; err != nil {
		t.Fatalf("Pluck: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("companies = %v, want the updated and the new one", names)
	}

	// FullSaveAssociations upserts association rows on save.
	employee.Name = "alice2"
	employee.Company.Name = "acme2"
	if err := db.Session(&gorm.Session{FullSaveAssociations: true}).Save(&employee).Error; err != nil {
		t.Fatalf("FullSaveAssociations save: %v", err)
	}
	var companyName string
	if err := db.Model(&oracleCompany{}).Where("id = ?", *employee.CompanyID).
		Pluck("name", &companyName).Error; err != nil {
		t.Fatalf("Pluck company: %v", err)
	}
	if companyName != "acme2" {
		t.Fatalf("associated company name = %q, want acme2", companyName)
	}
}
