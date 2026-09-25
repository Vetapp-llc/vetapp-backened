// Package itest runs the HTTP API end to end — real router, real
// handlers, real Postgres — against a disposable database.
//
// Set TEST_DATABASE_URL to a Postgres database holding the production
// schema (never production itself: every test truncates the tables it
// uses). Without it the package is skipped, so plain `go test ./...`
// stays database-free.
//
//	TEST_DATABASE_URL=postgres://postgres@127.0.0.1:55432/vetapp_test?sslmode=disable go test ./internal/itest/
package itest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database/migrations"
	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/router"
	"vetapp-backend/internal/services"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	clinicA = "404404404"
	clinicB = "405284393"

	vetA    uint = 9001 // vet at clinic A
	vetB    uint = 9002 // vet at clinic B
	admin   uint = 9003
	owner   uint = 9004
	vetA2   uint = 9005 // second vet at clinic A
	petA    uint = 90001
	petB    uint = 90002
	ownerID      = "01024065601" // owner personal ID
)

var (
	smsMu         sync.Mutex
	smsSent       []string
	storedObjects sync.Map
	db            *gorm.DB
	srv           *httptest.Server
	auth          *services.AuthService
)

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		fmt.Println("itest: TEST_DATABASE_URL not set, skipping integration tests")
		os.Exit(0)
	}
	if strings.Contains(dsn, "supabase") {
		fmt.Println("itest: refusing to run against a Supabase database")
		os.Exit(1)
	}

	if os.Getenv("ITEST_LOG") == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	var err error
	db, err = gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		panic(err)
	}
	if err := migrations.Run(db); err != nil {
		panic(err)
	}

	// Fake Supabase Storage: enough of the object API for upload, signed
	// download URLs and delete.
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/storage/v1/object/sign/"):
			key := strings.TrimPrefix(r.URL.Path, "/storage/v1/object/sign/")
			fmt.Fprintf(w, `{"signedURL":"/object/sign/%s?token=test"}`, key)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/storage/v1/object/"):
			storedObjects.Store(strings.TrimPrefix(r.URL.Path, "/storage/v1/object/"), true)
			w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete:
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer storage.Close()

	// Fake SMSOffice: records every destination it is asked to text.
	smsGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		smsMu.Lock()
		smsSent = append(smsSent, r.URL.Query().Get("destination"))
		smsMu.Unlock()
		w.Write([]byte(`{"Success":true,"Message":"ok"}`))
	}))
	defer smsGateway.Close()

	cfg := &config.Config{
		SupabaseURL: storage.URL, SupabaseServiceKey: "test", SupabaseBucket: "procedure-files",
		JWTSecret: "itest-secret-0123456789abcdef0123456789", JWTRefreshSecret: "itest-refresh-0123456789abcdef012345",
		AESSalt: "ITESTSALT", SMSURL: smsGateway.URL, SMSApiKey: "test", SMSSender: "V E T A P P",
		IPayURL: "http://127.0.0.1:9", PaymentProvider: "ipay", BaseURL: "http://localhost",
	}
	auth = services.NewAuthService(cfg)
	r := router.Setup(db, auth, services.NewSMSService(cfg), services.NewEmailService(cfg),
		services.NewIPayService(cfg), services.NewBOGService(cfg), services.NewStorageService(cfg),
		cfg.PaymentProvider, cfg.BaseURL, cfg.BaseURL)
	srv = httptest.NewServer(r)
	code := m.Run()
	srv.Close()
	os.Exit(code)
}

// reset empties every table the tests touch and loads the base fixtures.
func reset(t *testing.T) {
	t.Helper()
	smsMu.Lock()
	smsSent = nil
	smsMu.Unlock()
	db.Exec(`DELETE FROM sms_runs`)
	middlewareReset()
	must(t, db.Exec(`TRUNCATE app_changes, idempotency_keys, memberlogin_members, pets, vaccination, paymethod, shop, prices,
		eals, alergy, operationdate, homepro, pro, procedure_files, payments_ipay, analysefile RESTART IDENTITY CASCADE`).Error)
	users := []models.User{
		{ID: vetA, FirstName: "ვეტი ა", LastName: "11111111111", Email: "vet-a@test", Zip: clinicA, GroupID: models.RoleVet, Status: "T"},
		{ID: vetA2, FirstName: "ვეტი ა2", LastName: "11111111112", Email: "vet-a2@test", Zip: clinicA, GroupID: models.RoleVet, Status: "T"},
		{ID: vetB, FirstName: "ვეტი ბ", LastName: "22222222222", Email: "vet-b@test", Zip: clinicB, GroupID: models.RoleVet, Status: "T"},
		{ID: admin, FirstName: "ადმინი", Email: "admin@test", GroupID: models.RoleAdmin, Status: "T"},
		{ID: owner, FirstName: "დავით აბაიაძე", LastName: ownerID, Email: "owner@test", Phone: "599000000", GroupID: models.RoleOwner, Status: "T"},
	}
	for _, u := range users {
		must(t, db.Create(&u).Error)
	}
	pets := []models.Pet{
		{ID: petA, UUID: ownerID, Name: "იოში", Pet: "ძაღლი", Sex: "ხვადი", Vet: clinicA, Status: 1, FirstName: "დავით აბაიაძე"},
		{ID: petB, UUID: ownerID, Name: "კატა", Pet: "კატა", Sex: "ძუ", Vet: clinicB, Status: 1, FirstName: "დავით აბაიაძე"},
	}
	for _, p := range pets {
		must(t, db.Create(&p).Error)
	}
	// New rows get ids in the app range, as in production (migration 016).
	for _, tbl := range []string{"memberlogin_members", "pets", "vaccination"} {
		must(t, db.Exec(fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s','id'), 1000000000)`, tbl)).Error)
	}
	// The fixtures stand for rows copied from MySQL: not app changes.
	must(t, db.Exec(`TRUNCATE app_changes`).Error)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// token returns an access token for a fixture user.
func token(t *testing.T, id uint) string {
	t.Helper()
	var u models.User
	must(t, db.First(&u, id).Error)
	pair, err := auth.GenerateTokenPair(&u)
	must(t, err)
	return pair.AccessToken
}

type resp struct {
	Code int
	Body []byte
}

func (r resp) json(t *testing.T, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

// call performs an HTTP request as the given user (0 = anonymous).
func call(t *testing.T, as uint, method, path string, body interface{}) resp {
	t.Helper()
	return callWithHeader(t, as, method, path, body, "", "")
}

// callWithHeader is call with one extra request header.
func callWithHeader(t *testing.T, as uint, method, path string, body interface{}, hk, hv string) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		must(t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	must(t, err)
	req.Header.Set("Content-Type", "application/json")
	if hk != "" {
		req.Header.Set(hk, hv)
	}
	if as != 0 {
		req.Header.Set("Authorization", "Bearer "+token(t, as))
	}
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{Code: res.StatusCode, Body: b}
}

func expect(t *testing.T, r resp, code int) {
	t.Helper()
	if r.Code != code {
		t.Fatalf("status %d, want %d; body: %s", r.Code, code, r.Body)
	}
}

// insertProc writes a procedure row directly, bypassing the API.
func insertProc(t *testing.T, p models.Procedure) models.Procedure {
	t.Helper()
	must(t, db.Create(&p).Error)
	return p
}

// middlewareReset clears the account cache so fixtures reloaded under the
// same ids are not judged by a previous test's state.
func middlewareReset() {
	for _, id := range []uint{vetA, vetA2, vetB, admin, owner, 9010, 9101, 9102} {
		middleware.InvalidateAccount(id)
	}
}
