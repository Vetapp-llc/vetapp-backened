package router

import (
	"net/http"

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

// Setup creates and configures the Chi router with all routes.
//
// `emailService` may be a service whose .Enabled() reports false (no
// RESEND_API_KEY configured) — in that case email-verification calls
// gracefully degrade to a "pending" response rather than 5xx.
//
// `emailVerifyBaseURL` is the absolute URL prefix the email-verification
// link in outgoing emails points back to. Pass cfg.EmailVerifyBaseURL
// or fall back to baseURL when empty.
func Setup(db *gorm.DB, authService *services.AuthService, smsService *services.SMSService, emailService *services.EmailService, ipayService *services.IPayService, baseURL, emailVerifyBaseURL string) *chi.Mux {
	r := chi.NewRouter()

	// --- Global middleware ---
	r.Use(chimw.RealIP)
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(middleware.Logger)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
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
	subHandler := handlers.NewSubscriptionHandler(db, ipayService, baseURL)
	notifHandler := handlers.NewNotificationHandler(db, smsService)
	publicHandler := handlers.NewPublicHandler(db)

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

		// Auth - authenticated
		r.Get("/auth/me", authHandler.Me)
		r.Put("/auth/me", authHandler.UpdateMe)
		// In-app password rotation (settings → security → change password).
		// Distinct from the public /auth/password-reset, which is the
		// "I forgot my password" OTP recovery flow.
		r.Post("/auth/change-password", authHandler.ChangePassword)
		r.Post("/auth/email/verify/send", authHandler.SendEmailVerification)

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

		// Notifications - admin only
		r.Route("/notifications", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleAdmin))
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

		// Staff - admin only
		r.Route("/staff", func(r chi.Router) {
			r.Use(middleware.RequireRole(models.RoleAdmin))
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
			// Diseases / allergies live in the separate `eals` table, not
			// in the `vaccination` table — exposed via its own endpoint
			// so the mobile app doesn't have to encode the legacy
			// "tp=999 means allergies" hack.
			r.Get("/pets/{id}/diseases", ownerPortalHandler.Diseases)
			r.Get("/pets/{id}/code", ownerPortalHandler.GenerateCode)
			r.Get("/calendar", ownerPortalHandler.Calendar)
			r.Get("/visits", ownerPortalHandler.Visits)
		})
	})

	return r
}
