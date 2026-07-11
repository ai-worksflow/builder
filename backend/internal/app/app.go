package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/auth"
	documentcollaboration "github.com/worksflow/builder/backend/internal/collaboration"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/conversation"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/dataruntime"
	"github.com/worksflow/builder/backend/internal/delivery"
	"github.com/worksflow/builder/backend/internal/designimport"
	"github.com/worksflow/builder/backend/internal/events"
	"github.com/worksflow/builder/backend/internal/generation"
	worksgithub "github.com/worksflow/builder/backend/internal/github"
	"github.com/worksflow/builder/backend/internal/health"
	"github.com/worksflow/builder/backend/internal/httpapi"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/transport"
	"github.com/worksflow/builder/backend/internal/logging"
	"github.com/worksflow/builder/backend/internal/platform"
	"github.com/worksflow/builder/backend/internal/realtime"
	"github.com/worksflow/builder/backend/internal/storage/content"
	workflowruntime "github.com/worksflow/builder/backend/internal/workflow"
	"github.com/worksflow/builder/backend/migrations"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	dependencies, err := platform.Connect(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeDependencies(dependencies, cfg.HTTP.ShutdownTimeout, logger)

	startupCtx, cancelStartup := context.WithTimeout(ctx, cfg.Startup.Timeout)
	defer cancelStartup()
	if cfg.Startup.Migrate {
		if err := migrations.Up(startupCtx, dependencies.PostgresSQL); err != nil {
			return fmt.Errorf("apply database migrations: %w", err)
		}
		logger.Info("database migrations are current")
	}
	upgradedMinimumLoops, err := workflowruntime.UpgradeExistingMinimumLoops(
		startupCtx,
		dependencies.Postgres,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("provision minimum workflow versions: %w", err)
	}
	if upgradedMinimumLoops > 0 {
		logger.Info("minimum workflow versions upgraded", "count", upgradedMinimumLoops)
	}
	installedSelectionFlows, err := workflowruntime.ProvisionBlueprintSelectionFlows(
		startupCtx,
		dependencies.Postgres,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("provision Blueprint selection workflows: %w", err)
	}
	if installedSelectionFlows > 0 {
		logger.Info("Blueprint selection workflows installed", "count", installedSelectionFlows)
	}
	contentStore := content.NewMongoStore(dependencies.MongoDB, cfg.Content.MaxBytes)
	if cfg.Startup.EnsureMongoIndexes {
		if err := contentStore.EnsureIndexes(startupCtx); err != nil {
			return fmt.Errorf("ensure MongoDB indexes: %w", err)
		}
		logger.Info("MongoDB indexes are current")
	}
	if cfg.Startup.EnsureNATSStream {
		if err := events.EnsureEventStream(startupCtx, dependencies.JetStream); err != nil {
			return fmt.Errorf("ensure NATS event stream: %w", err)
		}
		logger.Info("NATS event stream is current")
	} else if _, err := dependencies.JetStream.StreamInfo(events.DefaultStreamName, nats.Context(startupCtx)); err != nil {
		return fmt.Errorf("required NATS event stream is not provisioned: %w", err)
	}

	passwordHasher, err := auth.NewPasswordHasher(auth.PasswordParams{
		Memory: uint32(cfg.Security.Argon2.MemoryKiB), Iterations: uint32(cfg.Security.Argon2.Iterations),
		Parallelism: uint8(cfg.Security.Argon2.Parallelism), SaltLength: uint32(cfg.Security.Argon2.SaltBytes),
		KeyLength: uint32(cfg.Security.Argon2.KeyBytes),
	})
	if err != nil {
		return fmt.Errorf("configure password hashing: %w", err)
	}
	encryptionKey, err := dataruntime.ParseEncryptionKey(cfg.Secrets.EncryptionKey)
	if err != nil {
		return fmt.Errorf("parse platform encryption key: %w", err)
	}
	authService, err := auth.NewService(dependencies.Postgres, dependencies.Redis, passwordHasher, auth.ServiceConfig{
		TTL: cfg.Security.Session.TTL, CachePrefix: cfg.Security.Session.CachePrefix,
		IdempotencyTTL: cfg.Idempotency.TTL, ReplayKey: encryptionKey,
	})
	if err != nil {
		return fmt.Errorf("create auth service: %w", err)
	}
	accessControl, err := core.NewAccessControl(dependencies.Postgres)
	if err != nil {
		return fmt.Errorf("create access control: %w", err)
	}
	githubAPI, err := worksgithub.NewAPIClient(cfg.GitHub.APIBaseURL, cfg.GitHub.RequestTimeout, nil)
	if err != nil {
		return fmt.Errorf("create GitHub API client: %w", err)
	}
	githubCredentials, err := worksgithub.NewRedisCredentialStore(dependencies.Redis, encryptionKey, cfg.GitHub.RedisPrefix)
	if err != nil {
		return fmt.Errorf("create GitHub credential store: %w", err)
	}
	githubService, err := worksgithub.NewService(githubAPI, githubCredentials, accessControl, dependencies.Postgres, logger, cfg.GitHub.CredentialTTL)
	if err != nil {
		return fmt.Errorf("create GitHub service: %w", err)
	}
	githubHandler, err := transport.NewGitHubHandler(githubService, worksgithub.MaxRequestBytes)
	if err != nil {
		return fmt.Errorf("create GitHub transport: %w", err)
	}
	dataService, err := dataruntime.NewPlatformService(dataruntime.PlatformDependencies{
		Database: dependencies.Postgres, Access: accessControl, EncryptionKey: encryptionKey,
	})
	if err != nil {
		return fmt.Errorf("create data runtime service: %w", err)
	}
	dataHandler, err := transport.NewDataHandler(transport.DataDependencies{Service: dataService, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes})
	if err != nil {
		return fmt.Errorf("create data runtime transport: %w", err)
	}
	publicDataService, err := dataruntime.NewPlatformPublicRuntime(dataruntime.PublicRuntimePlatformDependencies{
		Database: dependencies.Postgres, Access: accessControl, EncryptionKey: encryptionKey,
	})
	if err != nil {
		return fmt.Errorf("create public data runtime: %w", err)
	}
	publicDataLimiter, err := dataruntime.NewRedisPublicRateLimiter(dependencies.Redis, dataruntime.RedisPublicRateLimiterOptions{})
	if err != nil {
		return fmt.Errorf("create public data rate limiter: %w", err)
	}
	publicDataHandler, err := transport.NewPublicDataHandler(transport.PublicDataDependencies{
		Service: publicDataService, RateLimiter: publicDataLimiter,
		MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create public data transport: %w", err)
	}
	qualitySandbox, err := delivery.NewContainerSandbox(delivery.ContainerSandboxConfig{
		RuntimeBinary: cfg.Delivery.SandboxRuntime, DaemonHost: cfg.Delivery.SandboxHost,
		WorkspaceRoot: cfg.Delivery.QualityTempRoot,
		NodeImage:     cfg.Delivery.SandboxNodeImage,
		GoImage:       cfg.Delivery.SandboxGoImage, Timeout: cfg.Delivery.SandboxTimeout,
		OutputLimit: cfg.Delivery.SandboxOutputMax, MemoryLimit: cfg.Delivery.SandboxMemory,
		CPULimit: cfg.Delivery.SandboxCPUs, PIDsLimit: cfg.Delivery.SandboxPIDs,
		ResolverNetwork: cfg.Delivery.ResolverNetwork, ResolverNPMRegistry: cfg.Delivery.ResolverNPMRegistry,
		ResolverGoProxy: cfg.Delivery.ResolverGoProxy, ResolverGoSumDB: cfg.Delivery.ResolverGoSumDB,
		ResolverTimeout: cfg.Delivery.ResolverTimeout, ResolverOutputLimit: cfg.Delivery.ResolverOutputMax,
		ResolverMemoryLimit: cfg.Delivery.ResolverMemory, ResolverCPULimit: cfg.Delivery.ResolverCPUs,
		ResolverPIDsLimit: cfg.Delivery.ResolverPIDs,
	})
	if err != nil {
		return fmt.Errorf("create delivery quality sandbox: %w", err)
	}
	publishProvider, err := delivery.NewLocalStaticProvider(cfg.Delivery.PublishRoot, cfg.Delivery.PublishBaseURL)
	if err != nil {
		return fmt.Errorf("create delivery publish provider: %w", err)
	}
	deliveryServices, err := delivery.NewPlatformServices(delivery.PlatformDependencies{
		Database: dependencies.Postgres, Contents: contentStore, Access: accessControl,
		Sandbox: qualitySandbox, QualityRoot: cfg.Delivery.QualityTempRoot, Provider: publishProvider,
		Environments:  delivery.DataRuntimeEnvironmentResolver{Source: dataService},
		PublicRuntime: publicDataService,
	})
	if err != nil {
		return fmt.Errorf("create delivery services: %w", err)
	}
	deliveryHandler, err := transport.NewDeliveryHandler(transport.DeliveryDependencies{
		Quality: deliveryServices.Quality, Export: deliveryServices.Export, Publish: deliveryServices.Publish,
		StaticAssets: deliveryServices.StaticAssets, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create delivery transport: %w", err)
	}
	projectService, err := core.NewProjectService(
		dependencies.Postgres, contentStore, accessControl,
		workflowruntime.MinimumLoopProjectInitializer{}, conversation.ProjectInitializer{},
	)
	if err != nil {
		return fmt.Errorf("create project service: %w", err)
	}
	memberService, err := core.NewMemberService(dependencies.Postgres, accessControl)
	if err != nil {
		return fmt.Errorf("create member service: %w", err)
	}
	artifactService, err := core.NewArtifactService(dependencies.Postgres, contentStore, accessControl)
	if err != nil {
		return fmt.Errorf("create artifact service: %w", err)
	}
	traceService, err := core.NewTraceService(dependencies.Postgres, accessControl, contentStore)
	if err != nil {
		return fmt.Errorf("create trace service: %w", err)
	}
	reviewService, err := core.NewReviewService(dependencies.Postgres, contentStore, accessControl)
	if err != nil {
		return fmt.Errorf("create review service: %w", err)
	}
	commentService, err := core.NewCommentService(dependencies.Postgres, accessControl)
	if err != nil {
		return fmt.Errorf("create comment service: %w", err)
	}
	baselineService, err := core.NewBaselineService(dependencies.Postgres, contentStore, accessControl)
	if err != nil {
		return fmt.Errorf("create requirement baseline service: %w", err)
	}
	impactService, err := core.NewImpactService(dependencies.Postgres, contentStore, accessControl)
	if err != nil {
		return fmt.Errorf("create impact service: %w", err)
	}
	proposalService, err := core.NewProposalService(dependencies.Postgres, contentStore, accessControl)
	if err != nil {
		return fmt.Errorf("create proposal service: %w", err)
	}
	designImportService, err := designimport.NewService(
		dependencies.Postgres, contentStore, accessControl, artifactService, proposalService,
		designimport.ServiceConfig{MaxSnapshotContentBytes: cfg.Content.MaxBytes},
	)
	if err != nil {
		return fmt.Errorf("create design import service: %w", err)
	}
	designImportHandler, err := transport.NewDesignImportHandler(transport.DesignImportDependencies{
		Service: designImportService, MaxJSONBodyBytes: designimport.MaxRequestBytes,
	})
	if err != nil {
		return fmt.Errorf("create design import transport: %w", err)
	}
	workbenchService, err := core.NewWorkbenchService(dependencies.Postgres, contentStore, accessControl)
	if err != nil {
		return fmt.Errorf("create workbench service: %w", err)
	}
	implementationService, err := core.NewImplementationService(dependencies.Postgres, contentStore, accessControl)
	if err != nil {
		return fmt.Errorf("create implementation service: %w", err)
	}
	activityService, err := core.NewActivityService(dependencies.Postgres, dependencies.Redis, accessControl, 45*time.Second)
	if err != nil {
		return fmt.Errorf("create activity service: %w", err)
	}
	aiProvider, err := ai.NewOpenAIProvider(ai.OpenAIConfig{
		APIKey: cfg.AI.APIKey, BaseURL: cfg.AI.BaseURL, DefaultModel: cfg.AI.DefaultModel,
		Timeout: cfg.AI.Timeout, MaxInputBytes: cfg.AI.MaxInputBytes, MaxOutputBytes: cfg.AI.MaxOutputBytes,
		MaxRetries: cfg.AI.MaxRetries, Organization: cfg.AI.Organization, Project: cfg.AI.Project,
	}, nil)
	if err != nil {
		return fmt.Errorf("create AI provider: %w", err)
	}
	generationService, err := generation.NewService(
		dependencies.Postgres, contentStore, aiProvider, proposalService, workbenchService, implementationService,
		generation.ServiceConfig{ClaimLease: cfg.AI.Timeout + 5*time.Minute},
	)
	if err != nil {
		return fmt.Errorf("create generation service: %w", err)
	}
	documentCollaborationService, err := documentcollaboration.NewService(
		dependencies.Postgres,
		contentStore,
		accessControl,
		artifactService,
		proposalService,
		generationService,
		documentcollaboration.WithDownstreamCommandLease(cfg.AI.Timeout+5*time.Minute),
	)
	if err != nil {
		return fmt.Errorf("create document collaboration service: %w", err)
	}
	workflowStore, err := workflowruntime.NewGORMStore(dependencies.Postgres, workflowruntime.CoreContentStoreAdapter{Store: contentStore}, nil)
	if err != nil {
		return fmt.Errorf("create workflow store: %w", err)
	}
	workflowEngine, err := workflowruntime.NewPlatformEngine(workflowruntime.PlatformDependencies{
		Store: workflowStore, CoreProposals: proposalService, Generation: generationService,
		Workbench: workbenchService, ArtifactInputs: workflowruntime.CoreArtifactInputValidator{Database: dependencies.Postgres, Contents: contentStore},
		HumanEditOutput:     workflowruntime.CoreHumanEditOutputValidator{Artifacts: artifactService, Proposals: proposalService},
		TargetArtifacts:     workflowruntime.CoreTargetArtifactInitializer{Artifacts: artifactService},
		RequirementBaseline: baselineService,
		WorkbenchCompletion: workflowruntime.CoreWorkbenchCompletionValidator{Database: dependencies.Postgres},
		ReviewGate:          workflowruntime.CoreReviewGateVerifier{Database: dependencies.Postgres},
		Access:              accessControl, FanOut: workflowruntime.ContextFanOutResolver{ValueKey: "deliverySlices"},
		BlueprintPages:  workflowruntime.CoreBlueprintPageFanOutResolver{Artifacts: artifactService, Proposals: proposalService},
		Quality:         deliveryServices.WorkflowQuality,
		QualityManifest: workflowruntime.CoreQualityManifestResolver{Database: dependencies.Postgres},
		Publisher:       deliveryServices.WorkflowPublisher,
		DefaultModel:    cfg.AI.DefaultModel,
	})
	if err != nil {
		return fmt.Errorf("create workflow engine: %w", err)
	}
	workflowEngine.LeaseDuration = cfg.Workflow.LeaseDuration
	workflowFacade := workflowruntime.Facade{Engine: workflowEngine, Store: workflowStore, Access: accessControl}
	workflowHandler, err := transport.NewWorkflowHandler(transport.WorkflowDependencies{Facade: workflowFacade, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes})
	if err != nil {
		return fmt.Errorf("create workflow transport: %w", err)
	}
	conversationService, err := conversation.NewService(conversation.ServiceDependencies{
		Database: dependencies.Postgres, Access: accessControl, Workflow: workflowFacade, Manifests: workflowStore, AIProvider: aiProvider,
		Generation: generationService, DefaultImplementationModel: cfg.AI.DefaultModel,
		Workbench:         workbenchService,
		CommandClaimLease: cfg.AI.Timeout + 5*time.Minute,
	})
	if err != nil {
		return fmt.Errorf("create conversation control-plane service: %w", err)
	}
	conversationHandler, err := transport.NewConversationHandler(transport.ConversationDependencies{
		Service: conversationService, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create conversation control-plane transport: %w", err)
	}
	apiTransport := transport.NewServer(transport.Services{
		Auth: authService, Projects: projectService, Members: memberService, Access: accessControl,
		Artifacts: artifactService, Traces: traceService, Reviews: reviewService, Comments: commentService,
		Baselines: baselineService, Impacts: impactService, Proposals: proposalService,
		Workbench: workbenchService, Implementation: implementationService, Activity: activityService,
		Generation: generationService, Collaboration: documentCollaborationService,
	}, cfg, logger)
	idempotencyRequestLimit := cfg.HTTP.MaxJSONBodyBytes
	if idempotencyRequestLimit < worksgithub.MaxRequestBytes {
		idempotencyRequestLimit = worksgithub.MaxRequestBytes
	}
	if idempotencyRequestLimit < designimport.MaxRequestBytes {
		idempotencyRequestLimit = designimport.MaxRequestBytes
	}
	idempotency, err := worksmiddleware.NewIdempotencyRepository(dependencies.Postgres, worksmiddleware.IdempotencyConfig{
		TTL: cfg.Idempotency.TTL, LockTTL: cfg.Idempotency.LockTTL,
		MaxRequestBytes: idempotencyRequestLimit, MaxResponseBytes: cfg.Idempotency.MaxResponseBytes,
	})
	if err != nil {
		return fmt.Errorf("create idempotency repository: %w", err)
	}

	runtimeCtx, stopRuntime := context.WithCancel(context.Background())
	var runtimeWorkers sync.WaitGroup
	defer func() {
		stopRuntime()
		runtimeWorkers.Wait()
	}()
	hub := realtime.NewHub(cfg.WebSocket.SendBuffer)
	runtimeWorkers.Add(1)
	go func() {
		defer runtimeWorkers.Done()
		hub.Run(runtimeCtx)
	}()
	if cfg.Content.ReconcileEnabled {
		reconciler, err := content.NewReconciler(dependencies.Postgres, contentStore, content.ReconcileConfig{
			GracePeriod: cfg.Content.PendingGrace, OrphanTTL: cfg.Content.OrphanTTL,
			BatchSize: int64(cfg.Content.ReconcileBatch),
		})
		if err != nil {
			return fmt.Errorf("create content reconciler: %w", err)
		}
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			runContentReconciler(runtimeCtx, reconciler, cfg.Content.ReconcileInterval, logger)
		}()
	}
	if cfg.Workflow.WorkerEnabled {
		workerID := cfg.Workflow.WorkerID
		if workerID == "" {
			workerID = cfg.ServiceName + "-" + uuid.NewString()
		}
		worker := workflowruntime.Worker{Engine: workflowEngine, WorkerID: workerID, Heartbeat: cfg.Workflow.Heartbeat}
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			runWorkflowWorker(runtimeCtx, worker, cfg.Workflow.PollInterval, logger)
		}()
	}

	var authenticator realtime.Authenticator = transport.NewRealtimeAuthenticator(authService, cfg.Security)
	var subscriptionAuthorizer realtime.SubscriptionAuthorizer = transport.NewRealtimeSubscriptionAuthorizer(accessControl, dependencies.Postgres)
	if cfg.WebSocket.AllowAnonymous {
		authenticator = realtime.AnonymousAuthenticator{}
		subscriptionAuthorizer = realtime.AllowAllSubscriptions{}
		logger.Warn("anonymous WebSocket access is enabled")
	}
	historyReader, err := realtime.NewNATSHistoryReader(dependencies.JetStream, events.DefaultStreamName)
	if err != nil {
		return fmt.Errorf("create realtime history reader: %w", err)
	}
	websocketHandler := realtime.NewHandler(hub, authenticator, subscriptionAuthorizer, logger, cfg.WebSocket, historyReader)
	fanout := realtime.NewNATSFanout(dependencies.JetStream, hub, logger)
	fanoutSupervisor, err := realtime.NewFanoutSupervisor(fanout, logger, realtime.FanoutSupervisorConfig{})
	if err != nil {
		return fmt.Errorf("create NATS realtime fanout supervisor: %w", err)
	}
	runtimeWorkers.Add(1)
	go func() {
		defer runtimeWorkers.Done()
		fanoutSupervisor.Run(runtimeCtx)
	}()
	if cfg.Outbox.Enabled {
		publisher, err := events.NewOutboxPublisher(dependencies.Postgres, dependencies.JetStream, logger, events.OutboxConfig{
			BatchSize: cfg.Outbox.BatchSize, PollInterval: cfg.Outbox.PollInterval,
			ClaimTTL: cfg.Outbox.ClaimTTL, MaxAttempts: cfg.Outbox.MaxAttempts,
			PublishWait: cfg.Outbox.PublishWait,
		})
		if err != nil {
			return fmt.Errorf("create outbox publisher: %w", err)
		}
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if err := publisher.Run(runtimeCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("outbox publisher stopped", "error", err)
			}
		}()
	}

	readinessChecks := dependencies.Checks()
	qualitySandboxCheck := "quality_sandbox"
	if !qualitySandbox.ImagesDigestPinned() {
		qualitySandboxCheck = "quality_sandbox_mutable_images_development_only"
		logger.Warn("sandbox images use mutable development tags; quality runs are not reproducible across image refreshes",
			"node_image", cfg.Delivery.SandboxNodeImage, "go_image", cfg.Delivery.SandboxGoImage)
	}
	readinessChecks[qualitySandboxCheck] = qualitySandbox.Readiness
	readinessChecks["publish_storage"] = publishProvider.Readiness
	readinessChecks["nats_event_stream"] = func(checkCtx context.Context) error {
		_, checkErr := dependencies.JetStream.StreamInfo(events.DefaultStreamName, nats.Context(checkCtx))
		return checkErr
	}
	readinessChecks["realtime_fanout"] = fanoutSupervisor.Readiness
	readinessChecks["workflow_execution_profiles"] = workflowEngine.Readiness
	readiness := health.NewReadiness(cfg.Dependencies.ReadinessTimeout, readinessChecks)
	router, err := httpapi.NewRouter(cfg, logger, httpapi.RouterOptions{
		Readiness: readiness, WebSocket: websocketHandler,
		Transport: apiTransport, Authentication: authService, Idempotency: idempotency,
		Workflow: workflowHandler, Conversation: conversationHandler, GitHub: githubHandler, Data: dataHandler,
		PublicData: publicDataHandler, Delivery: deliveryHandler, DesignImports: designImportHandler,
	})
	if err != nil {
		stopRuntime()
		return fmt.Errorf("create HTTP router: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.HTTP.Address(),
		Handler:           router,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		MaxHeaderBytes:    cfg.HTTP.MaxHeaderBytes,
		ErrorLog:          logging.StandardLogger(logger),
	}
	server.RegisterOnShutdown(stopRuntime)

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "address", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case serveErr := <-serverErrors:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			stopRuntime()
			return fmt.Errorf("serve HTTP: %w", serveErr)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		stopRuntime()
		_ = server.Close()
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	stopRuntime()
	select {
	case serveErr := <-serverErrors:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP during shutdown: %w", serveErr)
		}
	case <-time.After(cfg.HTTP.ShutdownTimeout):
		return errors.New("HTTP server did not stop before shutdown timeout")
	}
	logger.Info("HTTP server stopped")
	return nil
}

func runWorkflowWorker(ctx context.Context, worker workflowruntime.Worker, pollInterval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		err := worker.RunOnce(ctx)
		if err != nil && !errors.Is(err, workflowruntime.ErrNoRunnableNode) && !errors.Is(err, context.Canceled) {
			logger.Error("workflow node execution failed", "worker_id", worker.WorkerID, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func closeDependencies(dependencies *platform.Dependencies, timeout time.Duration, logger *slog.Logger) {
	closeCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := dependencies.Close(closeCtx); err != nil {
		logger.Error("dependency shutdown failed", "error", err)
	}
}

func runContentReconciler(ctx context.Context, reconciler *content.Reconciler, interval time.Duration, logger *slog.Logger) {
	run := func() {
		stats, err := reconciler.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("content reconciliation failed", "error", err)
			return
		}
		if stats.Finalized > 0 || stats.Aborted > 0 {
			logger.Info("content reconciliation completed", "examined", stats.Examined, "finalized", stats.Finalized, "aborted", stats.Aborted)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
