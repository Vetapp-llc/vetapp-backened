package router

import (
	"net/http"
	"os"
	"strings"

	"vetapp-backend/internal/handlers"
	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	_ "vetapp-backend/docs" // swagger generated docs

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	httpSwagger "github.com/swaggo/http-swagger/v2"
	"gorm.io/gorm"
)

// allowedOrigins reads CORS_ORIGINS as a comma-separated list. Falls
// back to localhost dev origins when unset so local Next.js + Expo
// development continues to work without setup. Production must set the
// env var explicitly — wildcard + AllowCredentials is invalid per the
// CORS spec, and lying to browsers about it produces silent failures.
func allowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ORIGINS"))
	if raw == "" {
		return []string{
			// 3002 is where vetapp-web's dev script binds; 3000/3001 are
			// kept because Next.js falls back to them and other projects
			// commonly occupy 3000 on the same machine.
			"http://localhost:3002",
			"http://localhost:3000",
			"http://localhost:3001",
			// Expo dev server / web preview.
			"http://localhost:8081",
			"http://localhost:19006",
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Setup creates and configures the Chi router with all routes.
//
// `emailService` may be a service whose .Enabled() reports false (no
// RESEND_API_KEY configured) — in that case email-verification calls
// gracefully degrade to a "pending" response rather than 5xx.
//
// `emailVerifyBaseURL` is the absolute URL prefix the email-verification
// link in outgoing emails points back to. Pass cfg.EmailVerifyBaseURL
// or fall back to baseURL when empty.
// `bogService` may be nil or unconfigured; NewSubscriptionHandler then
// falls back to the legacy iPay gateway rather than failing checkouts.
// `paymentProvider` selects which gateway NEW checkouts use ("bog" or
// "ipay") — see config.PaymentProvider.
func Setup(db *gorm.DB, authService *services.AuthService, smsService *services.SMSService, emailService *services.EmailService, ipayService *services.IPayService, bogService *services.BOGService, storageService *services.StorageService, paymentProvider, baseURL, emailVerifyBaseURL string) *chi.Mux {
	r := chi.NewRouter()

	// --- Global middleware ---
	r.Use(chimw.RealIP)
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(middleware.Logger)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins(),
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// --- Initialize handlers ---
	// Resolve the public URL the email-verification link points to:
	// EMAIL_VERIFY_BASE_URL takes precedence so a deployment behind a
	// reverse proxy can advertise a different host than BASE_URL.
	publicEmailURL := emailVerifyBaseURL
	if publicEmailURL == "" {
		publicEmailURL = baseURL
	}
	authHandler := handlers.NewAuthHandler(db, authService, smsService, emailService, publicEmailURL)
	petHandler := handlers.NewPetHandler(db)
	procHandler := handlers.NewProcedureHandler(db)
	ownerHandler := handlers.NewOwnerHandler(db)
	statsHandler := handlers.NewStatsHandler(db)
	allergyHandler := handlers.NewAllergyHandler(db)
	apptHandler := handlers.NewAppointmentHandler(db)
	paymentHandler := handlers.NewPaymentHandler(db)
	priceHandler := handlers.NewPriceHandler(db)
	shopHandler := handlers.NewShopHandler(db)
	staffHandler := handlers.NewStaffHandler(db, authService)
	ownerPortalHandler := handlers.NewOwnerPortalHandler(db)
	subHandler := handlers.NewSubscriptionHandler(db, ipayService, bogService, paymentProvider, baseURL)
	notifHandler := handlers.NewNotificationHandler(db, smsService)
	publicHandler := handlers.NewPublicHandler(db)
	procFileHandler := handlers.NewProcedureFileHandler(db, storageService)
	adminHandler := handlers.NewAdminHandler(db)
	clinicFileHandler := handlers.NewClinicProcedureFileHandler(db, storageService)

	// --- Swagger UI ---
	r.Get("/swagger/*", httpSwagger.WrapHandler)

	// --- Health check ---
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	// --- Public routes (no auth) ---
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", authHandler.Login)
		r.Post("/register", authHandler.Register)
		r.Post("/refresh", authHandler.Refresh)
		r.Post("/otp/send", authHandler.OTPSend)
		r.Post("/otp/verify", authHandler.OTPVerify)
		r.Post("/password-reset", authHandler.PasswordReset)
		// Email-verification CONFIRMATION is reached by clicking a link
		// in an email — there's no JWT to attach. Token in the query
		// string is the proof of identity. Renders an HTML page on
		// success/failure rather than JSON so the user sees something
		// readable directly in their browser.
		r.Get("/email/verify/confirm", authHandler.ConfirmEmailVerification)

		// Authenticated auth routes must live inside THIS subrouter.
		// Chi resolves a request against the first subrouter mounted at
		// a matching prefix: with /api/auth mounted here, a request to
		// /api/auth/me never reaches the r.Route("/api", ...) block
		// below, so registering them there made them permanently 404
		// regardless of the token supplied.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Auth(authService))
			r.Use(middleware.ActiveAccount(db))
			r.Get("/me", authHandler.Me)
			r.Put("/me", authHandler.UpdateMe)
			// In-app password rotation (settings → security → change
			// password). Distinct from the public /auth/password-reset,
			// which is the "I forgot my password" OTP recovery flow.
			r.Post("/change-password", authHandler.ChangePassword)
			r.Post("/email/verify/send", authHandler.SendEmailVerification)
		})
	})

	// Subscription packages (public), callback (public webhook)
	r.Get("/api/subscriptions/packages", subHandler.Packages)
	r.Post("/api/subscriptions/callback", subHandler.Callback)

	// Public pet profile (no auth — used by QR code scanning)
	r.Get("/api/public/pets/code/{code}", publicHandler.LookupByCode)
	r.Get("/api/public/pets/{id}", publicHandler.GetPet)
	r.Get("/api/public/pets/{id}/procedures", publicHandler.GetPetProcedures)

	// --- Protected routes (JWT required) ---
	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.Auth(authService))
		r.Use(middleware.ActiveAccount(db))

		// Auth - authenticated routes are registered on the /api/auth
		// subrouter above (see the comment there); mounting them here
		// would be shadowed by it.

		// Pets - vet/admin only
		r.Route("/pets", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", petHandler.List)
			r.Post("/", petHandler.Create)
			r.Get("/{id}", petHandler.Get)
			r.Put("/{id}", petHandler.Update)
			r.Delete("/{id}", petHandler.Delete)
			r.Get("/{id}/history", petHandler.History)
			r.Get("/{id}/certificate", petHandler.Certificate)
			// Lab results and scans attached by the clinic (vet/upload77.php).
			r.Get("/{id}/procedures/{procId}/files", clinicFileHandler.List)
			r.Post("/{id}/procedures/{procId}/files", clinicFileHandler.Upload)
			r.Get("/{id}/procedures/{procId}/files/{fileId}", clinicFileHandler.Download)
			r.Delete("/{id}/procedures/{procId}/files/{fileId}", clinicFileHandler.Delete)
		})

		// Owners - vet/admin only
		r.Route("/owners", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", ownerHandler.List)
			r.Get("/{personalId}", ownerHandler.Get)
		})

		// Procedure reference data — constant lookup lists (vaccine
		// brands, dewormer drugs, ectoparasite products, test panels).
		// Authenticated but no role gate, so the owner mobile app can
		// populate its dropdowns when self-recording procedures.
		r.Get("/procedures/types", procHandler.Types)
		r.Get("/procedures/forms", procHandler.Forms)
		r.Get("/procedures/vaccine-options", procHandler.VaccineOptions)
		r.Get("/procedures/test-options", procHandler.TestOptions)
		r.Get("/procedures/dehel-options", procHandler.DehelOptions)
		r.Get("/procedures/ecto-options", procHandler.EctoOptions)

		// Procedures - vet/admin only (writes + clinic register)
		r.Route("/procedures", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", procHandler.List)
			r.Post("/", procHandler.Create)
			r.Get("/{id}", procHandler.Get)
			r.Put("/{id}", procHandler.Update)
			r.Delete("/{id}", procHandler.Delete)
		})

		// Stats - vet/admin (clinic), admin-only (admin)
		r.Route("/stats", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
				r.Get("/clinic", statsHandler.Clinic)
				r.Get("/clinic/daily", statsHandler.DailyClinic)
				r.Get("/clinic/monthly", statsHandler.MonthlyClinic)
				r.Get("/clinic/yearly", statsHandler.YearlyClinic)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireRole(models.RoleAdmin))
				r.Get("/admin", statsHandler.Admin)
			})
		})

		// Subscriptions - owner only (checkout)
		r.Route("/subscriptions", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleOwner))
			r.Post("/checkout", subHandler.Checkout)
			r.Post("/apple-verify", subHandler.AppleVerify)
		})

		// Super-admin account pages (superadmin/owners.php, dep.php, trans.php)
		r.Route("/admin", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleAdmin))
			r.Get("/members", adminHandler.Members)
			r.Delete("/members/{id}", adminHandler.DisableMember)
			r.Get("/transactions", adminHandler.Transactions)
		})

		// Owners who used this clinic's promo code (vet/promo.php)
		r.With(middleware.RequireRole(models.RoleVet, models.RoleAdmin)).Get("/promo", adminHandler.Promo)

		// Notifications - admin only
		r.Route("/notifications", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleAdmin))
			r.Get("/sms/reminders", notifHandler.PreviewReminders)
			r.Post("/sms/reminders", notifHandler.SendReminders)
		})

		// Allergies - vet/admin only
		r.Route("/allergies", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", allergyHandler.List)
			r.Post("/", allergyHandler.Create)
			r.Delete("/{id}", allergyHandler.Delete)
		})

		// Appointments - vet/admin only
		r.Route("/appointments", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", apptHandler.List)
			r.Post("/", apptHandler.Create)
			r.Get("/slots", apptHandler.Slots)
			r.Put("/{id}", apptHandler.Update)
			r.Delete("/{id}", apptHandler.Delete)
			r.Put("/{id}/slot", apptHandler.AssignSlot)
		})

		// Payments - vet/admin only
		r.Route("/payments", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Post("/record", paymentHandler.Record)
			r.Get("/daily", paymentHandler.Daily)
			r.Get("/history", paymentHandler.History)
		})

		// Prices - vet/admin only
		r.Route("/prices", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", priceHandler.List)
			r.Post("/", priceHandler.Create)
			r.Put("/{id}", priceHandler.Update)
			r.Delete("/{id}", priceHandler.Delete)
		})

		// Shop (retail sales) - vet/admin only
		r.Route("/shop", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", shopHandler.List)
			r.Post("/", shopHandler.Create)
			r.Put("/{id}", shopHandler.Update)
			r.Delete("/{id}", shopHandler.Delete)
		})

		// Staff - each clinic manages its own vets (vet/vets.php), admins any
		r.Route("/staff", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleVet, models.RoleAdmin))
			r.Get("/", staffHandler.List)
			r.Post("/", staffHandler.Create)
			r.Put("/{id}", staffHandler.Update)
			r.Delete("/{id}", staffHandler.Delete)
		})

		// Owner portal - owner only (mobile app)
		r.Route("/owner", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleOwner))
			r.Get("/pets", ownerPortalHandler.ListPets)
			r.Post("/pets", ownerPortalHandler.CreatePet)
			r.Get("/pets/{id}", ownerPortalHandler.GetPet)
			r.Put("/pets/{id}", ownerPortalHandler.UpdatePet)
			r.Get("/pets/{id}/procedures", ownerPortalHandler.Procedures)
			r.Post("/pets/{id}/procedures", ownerPortalHandler.CreateProcedure)
			r.Delete("/pets/{id}/procedures/{procId}", ownerPortalHandler.DeleteProcedure)
			// Attachments (lab results, scans). Scoped under the
			// procedure so ownership is checked from the path on every
			// call — see ProcedureFileHandler.
			r.Get("/pets/{id}/procedures/{procId}/files", procFileHandler.List)
			r.Post("/pets/{id}/procedures/{procId}/files", procFileHandler.Upload)
			r.Get("/pets/{id}/procedures/{procId}/files/{fileId}", procFileHandler.Download)
			r.Delete("/pets/{id}/procedures/{procId}/files/{fileId}", procFileHandler.Delete)
			// Diseases / allergies live in the separate `eals` table, not
			// in the `vaccination` table — exposed via its own endpoint
			// so the mobile app doesn't have to encode the legacy
			// "tp=999 means allergies" hack.
			r.Get("/pets/{id}/diseases", ownerPortalHandler.Diseases)
			r.Get("/pets/{id}/code", ownerPortalHandler.GenerateCode)
			// At-home treatments (owner/homep.php)
			r.Get("/pets/{id}/home-procedures", ownerPortalHandler.HomeProcedures)
			r.Post("/pets/{id}/home-procedures", ownerPortalHandler.CreateHomeProcedure)
			r.Post("/pets/{id}/home-procedures/{hpId}/done", ownerPortalHandler.MarkHomeProcedureDone)
			r.Delete("/pets/{id}/home-procedures/{hpId}", ownerPortalHandler.DeleteHomeProcedure)
			r.Get("/calendar", ownerPortalHandler.Calendar)
			r.Get("/visits", ownerPortalHandler.Visits)
		})
	})

	return r
}
