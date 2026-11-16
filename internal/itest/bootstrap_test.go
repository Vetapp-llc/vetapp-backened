package itest

import (
	"fmt"
	"gorm.io/gorm"
	"net/url"
	"vetapp-backend/internal/models"
)

// Opt-in empty schema for isolated release tests. Never imports a production
// dump, and refuses remote hosts or a database without the test suffix.
func bootstrapReleaseTestDB(db *gorm.DB, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Path != "/vetapp_release_test" {
		return fmt.Errorf("bootstrap requires local vetapp_release_test")
	}
	if err := db.AutoMigrate(&models.User{}, &models.Pet{}, &models.Procedure{}, &models.Payment{}, &models.Shop{}, &models.Price{}, &models.Allergy{}, &models.Appointment{}, &models.HomeProcedure{}, &models.HomeProcedureDone{}, &models.Subscription{}, &models.OTP{}, &models.Package{}); err != nil {
		return err
	}
	return db.Exec(`CREATE TABLE IF NOT EXISTS alergy (id BIGSERIAL PRIMARY KEY); CREATE TABLE IF NOT EXISTS analysefile (id BIGSERIAL PRIMARY KEY, location TEXT, caseid TEXT);`).Error
}
