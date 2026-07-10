package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/health"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
)

type RouterOptions struct {
	Readiness      *health.Readiness
	WebSocket      http.Handler
	Transport      *transport.Server
	Authentication worksmiddleware.SessionAuthenticator
	Idempotency    *worksmiddleware.IdempotencyRepository
	Workflow       *transport.WorkflowHandler
	GitHub         *transport.GitHubHandler
	Data           *transport.DataHandler
	Delivery       *transport.DeliveryHandler
}

func NewRouter(cfg config.Config, logger *slog.Logger, options RouterOptions) (*gin.Engine, error) {
	if logger == nil || options.Readiness == nil {
		return nil, errors.New("logger and readiness are required")
	}
	if (options.Transport == nil) != (options.Authentication == nil) {
		return nil, errors.New("API transport and authentication must be configured together")
	}
	if cfg.Environment == config.EnvironmentProduction {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	router := gin.New()
	if err := router.SetTrustedProxies(cfg.HTTP.TrustedProxies); err != nil {
		return nil, err
	}
	router.Use(
		worksmiddleware.RequestID(),
		worksmiddleware.AccessLog(logger),
		worksmiddleware.Recovery(logger),
		worksmiddleware.SecurityHeaders(cfg.Security.EnableHSTS),
		worksmiddleware.CORS(cfg.CORS),
	)

	router.GET("/health/live", func(context *gin.Context) {
		context.Header("Cache-Control", "no-store")
		context.JSON(http.StatusOK, gin.H{
			"service": cfg.ServiceName,
			"status":  "ok",
			"time":    time.Now().UTC().Format(time.RFC3339Nano),
		})
	})
	router.GET("/health/ready", func(context *gin.Context) {
		context.Header("Cache-Control", "no-store")
		report := options.Readiness.Check(context.Request.Context())
		status := http.StatusOK
		state := "ready"
		if !report.Healthy {
			status = http.StatusServiceUnavailable
			state = "not_ready"
		}
		context.JSON(status, gin.H{
			"service": cfg.ServiceName,
			"status":  state,
			"checks":  report.Checks,
		})
	})
	if options.WebSocket != nil {
		router.GET("/v1/ws", func(context *gin.Context) {
			options.WebSocket.ServeHTTP(context.Writer, context.Request)
			context.Abort()
		})
	}
	if options.Delivery != nil {
		if err := transport.RegisterDeliveryPublicRoutes(router, options.Delivery); err != nil {
			return nil, err
		}
	}
	if options.Transport != nil && options.Authentication != nil {
		api := options.Transport
		captureIdempotency := worksmiddleware.CaptureIdempotencyKey(false)
		persistIdempotency := func(context *gin.Context) { context.Next() }
		if options.Idempotency != nil {
			persistIdempotency = worksmiddleware.PersistIdempotency(options.Idempotency)
		}
		authenticate := worksmiddleware.RequireAuthentication(options.Authentication, cfg.Security)
		csrf := worksmiddleware.RequireCSRF(cfg.Security)
		ifMatch := worksmiddleware.RequireIfMatch()

		router.POST("/v1/session/register", captureIdempotency, api.RegisterSession)
		router.POST("/v1/session", captureIdempotency, api.LoginSession)
		// Compatibility aliases for the first PlatformClient revision.
		router.POST("/v1/session/sign-up", captureIdempotency, api.RegisterSession)
		router.POST("/v1/session/sign-in", captureIdempotency, api.LoginSession)
		router.GET("/v1/session", api.GetSession)
		router.POST("/v1/session/refresh", authenticate, csrf, captureIdempotency, api.RefreshSession)
		router.DELETE("/v1/session", csrf, api.LogoutSession)

		protected := router.Group("/v1", authenticate)
		protected.GET("/projects", api.ListProjects)
		protected.POST("/projects", csrf, captureIdempotency, persistIdempotency, api.CreateProject)
		protected.GET("/projects/:projectId", api.GetProject)
		protected.PATCH("/projects/:projectId", csrf, ifMatch, captureIdempotency, persistIdempotency, api.UpdateProject)
		protected.DELETE("/projects/:projectId", csrf, ifMatch, captureIdempotency, persistIdempotency, api.ArchiveProject)
		protected.GET("/projects/:projectId/authorization", api.AuthorizeProject)

		protected.GET("/projects/:projectId/members", api.ListMembers)
		protected.POST("/projects/:projectId/members", csrf, captureIdempotency, persistIdempotency, api.AddMember)
		protected.PATCH("/projects/:projectId/members/:userId", csrf, ifMatch, captureIdempotency, persistIdempotency, api.UpdateMember)
		protected.DELETE("/projects/:projectId/members/:userId", csrf, ifMatch, captureIdempotency, persistIdempotency, api.RemoveMember)
		protected.POST("/projects/:projectId/invitations", csrf, captureIdempotency, persistIdempotency, api.CreateInvitation)
		protected.POST("/invitations/accept", csrf, captureIdempotency, persistIdempotency, api.AcceptInvitation)

		transport.RegisterBusinessRoutes(protected, api, persistIdempotency)
		if options.Workflow != nil {
			workflowMutation := []gin.HandlerFunc{csrf, worksmiddleware.CaptureIdempotencyKey(true), persistIdempotency}
			if err := transport.RegisterWorkflowRoutes(protected, options.Workflow, workflowMutation...); err != nil {
				return nil, err
			}
		}
		if options.GitHub != nil {
			githubMutation := []gin.HandlerFunc{csrf, worksmiddleware.CaptureIdempotencyKey(true), persistIdempotency}
			if err := transport.RegisterGitHubRoutes(protected, options.GitHub, githubMutation...); err != nil {
				return nil, err
			}
		}
		if options.Data != nil {
			dataMutation := []gin.HandlerFunc{csrf, worksmiddleware.CaptureIdempotencyKey(true), persistIdempotency}
			if err := transport.RegisterDataRoutes(protected, options.Data, dataMutation...); err != nil {
				return nil, err
			}
		}
		if options.Delivery != nil {
			deliveryMutation := []gin.HandlerFunc{csrf, worksmiddleware.CaptureIdempotencyKey(true), persistIdempotency}
			if err := transport.RegisterDeliveryRoutes(protected, options.Delivery, deliveryMutation...); err != nil {
				return nil, err
			}
		}
	}
	router.NoRoute(func(context *gin.Context) {
		problem.Write(context, problem.New(http.StatusNotFound, "route_not_found", "Route not found", "The requested route was not found."))
	})
	return router, nil
}
