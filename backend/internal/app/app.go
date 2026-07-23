package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/worksflow/builder/backend/internal/agent"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/automation"
	documentcollaboration "github.com/worksflow/builder/backend/internal/collaboration"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/constructor"
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
	"github.com/worksflow/builder/backend/internal/lsp"
	"github.com/worksflow/builder/backend/internal/platform"
	"github.com/worksflow/builder/backend/internal/qualificationrelease"
	"github.com/worksflow/builder/backend/internal/realtime"
	"github.com/worksflow/builder/backend/internal/release"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"github.com/worksflow/builder/backend/internal/templates"
	"github.com/worksflow/builder/backend/internal/verification"
	workflowruntime "github.com/worksflow/builder/backend/internal/workflow"
	"github.com/worksflow/builder/backend/migrations"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) (runErr error) {
	dependencies, err := platform.Connect(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeDependencies(dependencies, cfg.HTTP.ShutdownTimeout, logger)

	startupCtx, cancelStartup := context.WithTimeout(ctx, cfg.Startup.Timeout)
	defer cancelStartup()
	if err := migrations.VerifyCurrent(startupCtx, dependencies.PostgresSQL); err != nil {
		return fmt.Errorf("verify PostgreSQL schema migration head: %w", err)
	}
	if err := platform.VerifyPostgresAPIRolePosture(
		startupCtx,
		dependencies.PostgresSQL,
		cfg.Environment,
	); err != nil {
		return fmt.Errorf("verify PostgreSQL API role posture: %w", err)
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
	verificationStore, err := verification.NewPostgresStore(dependencies.Postgres, contentStore)
	if err != nil {
		return fmt.Errorf("create verification store: %w", err)
	}
	releaseStore, err := release.NewStore(dependencies.Postgres, contentStore, verificationStore)
	if err != nil {
		return fmt.Errorf("create ReleaseBundle store: %w", err)
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
	var platformCredentials []worksgithub.PlatformCredentialProvider
	if cfg.GitHub.PlatformAppEnabled() {
		privateKey, readErr := os.ReadFile(cfg.GitHub.PrivateKeyFile)
		if readErr != nil {
			return fmt.Errorf("read GitHub App private key: %w", readErr)
		}
		provider, providerErr := worksgithub.NewInstallationTokenProvider(
			githubAPI,
			cfg.GitHub.AppID,
			cfg.GitHub.InstallationID,
			cfg.GitHub.Organization,
			privateKey,
		)
		if providerErr != nil {
			return fmt.Errorf("create GitHub App installation provider: %w", providerErr)
		}
		platformCredentials = append(platformCredentials, provider)
	}
	githubService, err := worksgithub.NewService(
		githubAPI,
		githubCredentials,
		accessControl,
		dependencies.Postgres,
		logger,
		cfg.GitHub.CredentialTTL,
		platformCredentials...,
	)
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
		Environments:   delivery.DataRuntimeEnvironmentResolver{Source: dataService},
		PublicRuntime:  publicDataService,
		ReleaseBundles: releaseStore,
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
	templateRegistry, err := templates.NewRegistry(dependencies.Postgres)
	if err != nil {
		return fmt.Errorf("create template registry: %w", err)
	}
	templateRegistryHandler, err := transport.NewTemplateRegistryHandler(transport.TemplateRegistryDependencies{Registry: templateRegistry})
	if err != nil {
		return fmt.Errorf("create template registry transport: %w", err)
	}
	templateResolver, err := constructor.NewTemplateRegistryResolver(templateRegistry)
	if err != nil {
		return fmt.Errorf("create constructor template resolver: %w", err)
	}
	constructorService, err := constructor.NewService(
		dependencies.Postgres, contentStore, accessControl, workbenchService, templateResolver,
	)
	if err != nil {
		return fmt.Errorf("create constructor service: %w", err)
	}
	constructorHandler, err := transport.NewConstructorHandler(transport.ConstructorDependencies{
		Service: constructorService, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create constructor transport: %w", err)
	}
	implementationService, err := core.NewImplementationService(
		dependencies.Postgres, contentStore, accessControl, constructorService,
	)
	if err != nil {
		return fmt.Errorf("create implementation service: %w", err)
	}
	treeStore, err := repository.NewTreeStore(contentStore)
	if err != nil {
		return fmt.Errorf("create repository tree store: %w", err)
	}
	fileStore, err := repository.NewFileStore(contentStore)
	if err != nil {
		return fmt.Errorf("create repository file store: %w", err)
	}
	fileCatalog, err := repository.NewGORMFileBlobCatalog(dependencies.Postgres)
	if err != nil {
		return fmt.Errorf("create repository file catalog: %w", err)
	}
	fileBlobs, err := repository.NewFileBlobService(fileCatalog, fileStore, time.Now)
	if err != nil {
		return fmt.Errorf("create repository file service: %w", err)
	}
	literalIndexStore, err := repository.NewGORMExactTreeLiteralIndexStore(dependencies.Postgres)
	if err != nil {
		return fmt.Errorf("create repository exact-tree literal index store: %w", err)
	}
	searchAdmission, err := repository.NewRedisExactTreeSearchAdmission(
		dependencies.Redis,
		repositorySearchAdmissionOptions(cfg.Repository.SearchAdmission),
	)
	if err != nil {
		return fmt.Errorf("create repository exact-tree search admission: %w", err)
	}
	literalIndex, err := repository.NewAdmittedExactTreeLiteralIndexService(
		literalIndexStore,
		fileBlobs,
		repository.ExactTreeLiteralIndexAdmissionConfig{
			ProjectQuota: repository.ExactTreeLiteralIndexProjectQuota{
				MaxTrees:        cfg.Repository.SearchIndex.MaxTrees,
				MaxSourceBytes:  cfg.Repository.SearchIndex.MaxSourceBytes,
				MaxActiveBuilds: cfg.Repository.SearchIndex.MaxActiveBuilds,
			},
			FirstBuilderAdmission: searchAdmission,
		},
	)
	if err != nil {
		return fmt.Errorf("create repository exact-tree literal index service: %w", err)
	}
	templateSources, err := repository.NewGitTemplateSourceMaterializer(repository.GitTemplateSourceOptions{
		GitBinary: cfg.TemplateSource.GitBinary, CacheRoot: cfg.TemplateSource.CacheRoot,
		AllowedHosts: cfg.TemplateSource.AllowedHosts, FetchTimeout: cfg.TemplateSource.FetchTimeout,
	})
	if err != nil {
		return fmt.Errorf("create exact TemplateRelease source materializer: %w", err)
	}
	candidateStore, err := repository.NewGORMCandidateStore(dependencies.Postgres, treeStore)
	if err != nil {
		return fmt.Errorf("create repository Candidate store: %w", err)
	}
	candidateBootstrap, err := repository.NewCandidateBootstrapService(
		dependencies.Postgres, contentStore, fileBlobs, treeStore, candidateStore,
		sandboxAccessAdapter{access: accessControl},
		repositoryBuildContractGate{constructor: constructorService}, time.Now,
		repository.WithTemplateSourceMaterializer(templateSources),
		repository.WithCandidateSearchLiteralIndex(literalIndex),
		repository.WithExactTreeSearchAdmission(searchAdmission),
	)
	if err != nil {
		return fmt.Errorf("create repository Candidate bootstrap service: %w", err)
	}
	candidateControls, err := repository.NewCandidateControlStore(dependencies.Postgres, candidateStore)
	if err != nil {
		return fmt.Errorf("create repository Candidate control store: %w", err)
	}
	pathPolicies, err := repository.NewRegistryPathPolicyResolver(templateRegistry)
	if err != nil {
		return fmt.Errorf("create repository path policy resolver: %w", err)
	}
	sandboxAccess := sandboxAccessAdapter{access: accessControl}
	mutations, err := repository.NewMutationService(
		candidateStore, treeStore, fileBlobs, pathPolicies, sandboxAccess, time.Now,
	)
	if err != nil {
		return fmt.Errorf("create repository mutation service: %w", err)
	}
	rebaseStore, err := repository.NewCandidateRebaseStore(dependencies.Postgres, candidateStore)
	if err != nil {
		return fmt.Errorf("create repository Candidate rebase store: %w", err)
	}
	rebases, err := repository.NewCandidateRebaseService(
		candidateBootstrap, rebaseStore, candidateControls, candidateStore,
		mutations, fileBlobs, sandboxAccess, time.Now,
	)
	if err != nil {
		return fmt.Errorf("create repository Candidate rebase service: %w", err)
	}
	repositoryHandler, err := transport.NewRepositoryHandler(transport.RepositoryDependencies{
		Service: candidateBootstrap, Rebases: rebases, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create repository Candidate transport: %w", err)
	}
	sandboxStore, err := sandbox.NewStore(dependencies.Postgres)
	if err != nil {
		return fmt.Errorf("create SandboxSession store: %w", err)
	}
	var workspaceMaterializer *sandbox.WorkspaceMaterializer
	var workspaceSynchronizers []sandbox.WorkspaceMutationSynchronizer
	if cfg.Sandbox.Enabled {
		workspaceMaterializer, err = sandbox.NewWorkspaceMaterializer(cfg.Sandbox.WorkspaceRoot, fileBlobs)
		if err != nil {
			return fmt.Errorf("create sandbox workspace materializer: %w", err)
		}
		workspaceSynchronizers = append(workspaceSynchronizers, workspaceMaterializer)
	}
	sandboxFacade, err := sandbox.NewFacade(
		sandboxStore, candidateControls, mutations, fileBlobs, sandboxAccess, workspaceSynchronizers...,
	)
	if err != nil {
		return fmt.Errorf("create sandbox façade: %w", err)
	}
	candidateFreeze, err := sandbox.NewCandidateFreezeService(
		sandboxStore, candidateControls, implementationService, fileBlobs, sandboxAccess,
	)
	if err != nil {
		return fmt.Errorf("create Candidate freeze-to-Proposal service: %w", err)
	}
	connectionTicketStore, err := sandbox.NewRedisConnectionTicketStore(
		dependencies.Redis, cfg.Sandbox.RedisPrefix+"connection-ticket:", time.Now,
	)
	if err != nil {
		return fmt.Errorf("create sandbox connection ticket store: %w", err)
	}
	connectionTickets, err := sandbox.NewConnectionTicketService(
		connectionTicketStore, sandboxStore, sandboxAccess, cfg.Sandbox.ConnectionTicketTTL, time.Now,
	)
	if err != nil {
		return fmt.Errorf("create sandbox connection ticket service: %w", err)
	}
	sandboxStreamEvents, err := sandbox.NewRedisStreamEventStore(
		dependencies.Redis, cfg.Sandbox.RedisPrefix+"stream:",
		cfg.Sandbox.StreamMaxEvents, cfg.Sandbox.StreamRetention, time.Now,
	)
	if err != nil {
		return fmt.Errorf("create sandbox stream event store: %w", err)
	}
	var sandboxProvisioning *sandbox.ProvisioningService
	var sandboxControl *sandbox.ControlService
	var sandboxProcesses *sandbox.ProcessService
	var sandboxTerminals *sandbox.TerminalService
	var sandboxPorts *sandbox.PortService
	var sandboxDeadlineWorker *sandbox.DeadlineWorkerService
	var interactiveRuntime *sandbox.ContainerRuntime
	if cfg.Sandbox.Enabled {
		sessionConfiguration, resolverErr := sandbox.NewTemplateSessionConfigurationResolver(templateRegistry)
		if resolverErr != nil {
			return fmt.Errorf("create sandbox template configuration resolver: %w", resolverErr)
		}
		interactiveRuntime, err = sandbox.NewContainerRuntime(sandbox.ContainerRuntimeConfig{
			RuntimeBinary: cfg.Sandbox.RuntimeBinary, DaemonHost: cfg.Sandbox.DaemonHost,
			WorkspaceRoot: cfg.Sandbox.WorkspaceRoot, RunnerImage: cfg.Sandbox.RunnerImage,
			GatewayNetwork:     cfg.Sandbox.GatewayNetwork,
			GatewayBindAddress: cfg.Sandbox.GatewayBindAddress,
			StartupTimeout:     cfg.Sandbox.StartupTimeout, CommandTimeout: cfg.Sandbox.CommandTimeout,
			OutputLimit: cfg.Sandbox.RuntimeOutputMax,
		})
		if err != nil {
			return fmt.Errorf("create interactive sandbox runtime: %w", err)
		}
		defer func() {
			if closeErr := interactiveRuntime.Close(); closeErr != nil {
				logger.Warn("close interactive sandbox runtime client", "error", closeErr)
			}
		}()
		runtimeReadinessCtx, cancelRuntimeReadiness := context.WithTimeout(ctx, cfg.Sandbox.CommandTimeout)
		err = interactiveRuntime.Readiness(runtimeReadinessCtx)
		cancelRuntimeReadiness()
		if err != nil {
			return fmt.Errorf("verify interactive sandbox runtime: %w", err)
		}
		processStore, processStoreErr := sandbox.NewPostgresProcessStore(dependencies.Postgres)
		if processStoreErr != nil {
			return fmt.Errorf("create sandbox process store: %w", processStoreErr)
		}
		interactiveDependencyResolver, resolverErr := delivery.NewContainerSandbox(delivery.ContainerSandboxConfig{
			RuntimeBinary: cfg.Delivery.SandboxRuntime, DaemonHost: cfg.Delivery.SandboxHost,
			WorkspaceRoot: cfg.Sandbox.WorkspaceRoot,
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
		if resolverErr != nil {
			return fmt.Errorf("create interactive dependency resolver: %w", resolverErr)
		}
		interactiveDependencies, resolverErr := sandbox.NewProcessDependencyPreparer(interactiveDependencyResolver)
		if resolverErr != nil {
			return fmt.Errorf("create interactive dependency preparer: %w", resolverErr)
		}
		sandboxProcesses, err = sandbox.NewProcessService(
			sandboxStore, candidateControls, sessionConfiguration, workspaceMaterializer,
			interactiveRuntime, processStore, sandboxAccess, sandboxStreamEvents,
			int64(cfg.Sandbox.RuntimeOutputMax), interactiveDependencies,
		)
		if err != nil {
			return fmt.Errorf("create sandbox process service: %w", err)
		}
		terminalStore, terminalStoreErr := sandbox.NewPostgresTerminalStore(dependencies.Postgres)
		if terminalStoreErr != nil {
			return fmt.Errorf("create sandbox terminal store: %w", terminalStoreErr)
		}
		sandboxTerminals, err = sandbox.NewTerminalService(
			sandboxStore, candidateControls, workspaceMaterializer, interactiveRuntime,
			terminalStore, sandboxAccess, sandboxStreamEvents, int64(cfg.Sandbox.RuntimeOutputMax),
		)
		if err != nil {
			return fmt.Errorf("create sandbox terminal service: %w", err)
		}
		previewGrantStore, previewStoreErr := sandbox.NewRedisPreviewGrantStore(
			dependencies.Redis, cfg.Sandbox.RedisPrefix+"preview-grant:", time.Now,
		)
		if previewStoreErr != nil {
			return fmt.Errorf("create sandbox preview grant store: %w", previewStoreErr)
		}
		sandboxPorts, err = sandbox.NewPortService(
			sandboxStore, candidateControls, workspaceMaterializer, interactiveRuntime,
			sandboxAccess, previewGrantStore, cfg.Sandbox.PreviewPublicOrigin,
			cfg.Sandbox.PreviewDialHost, cfg.Sandbox.PreviewTicketTTL, cfg.Sandbox.PreviewProbeTimeout,
		)
		if err != nil {
			return fmt.Errorf("create sandbox port service: %w", err)
		}
		defer func() {
			if closeErr := sandboxTerminals.Close(); closeErr != nil {
				logger.Warn("close interactive sandbox terminals", "error", closeErr)
			}
		}()
		lifecycle, lifecycleErr := sandbox.NewLifecycleService(
			sandboxStore, workspaceMaterializer, interactiveRuntime, sandboxStreamEvents,
		)
		if lifecycleErr != nil {
			return fmt.Errorf("create sandbox lifecycle service: %w", lifecycleErr)
		}
		resourceFencer, fencerErr := sandbox.NewRuntimeEpochFencerGroup(sandboxProcesses, sandboxTerminals)
		if fencerErr != nil {
			return fmt.Errorf("create sandbox runtime resource fencer: %w", fencerErr)
		}
		sandboxControl, err = sandbox.NewControlService(
			sandboxStore, candidateControls, workspaceMaterializer, interactiveRuntime,
			sandboxAccess, sandboxStreamEvents, resourceFencer,
		)
		if err != nil {
			return fmt.Errorf("create sandbox lifecycle control service: %w", err)
		}
		lifecycleWorkerID := cfg.Sandbox.LifecycleWorkerID
		if lifecycleWorkerID == "" {
			lifecycleWorkerID = cfg.ServiceName + "-sandbox-lifecycle-" + uuid.NewString()
		}
		deadlineWorker, workerErr := sandbox.NewDeadlineWorker(
			sandboxStore, sandboxStore, candidateControls, sandboxControl,
			sandbox.DeadlineWorkerConfig{
				WorkerID: lifecycleWorkerID, LeaseDuration: cfg.Sandbox.LifecycleLease,
				RetryDelay: cfg.Sandbox.LifecycleRetry,
			},
		)
		if workerErr != nil {
			return fmt.Errorf("create sandbox lifecycle deadline worker: %w", workerErr)
		}
		sandboxDeadlineWorker, workerErr = sandbox.NewDeadlineWorkerService(
			deadlineWorker, cfg.Sandbox.LifecyclePoll, logger,
		)
		if workerErr != nil {
			return fmt.Errorf("create sandbox lifecycle deadline worker service: %w", workerErr)
		}
		runnerDigest := sandboxRunnerDigest(cfg.Sandbox.RunnerImage)
		sandboxProvisioning, err = sandbox.NewProvisioningService(
			sandboxStore, candidateControls, sessionConfiguration, sandboxAccess,
			sandbox.ProvisioningPolicy{
				RunnerImageDigest: runnerDigest,
				Quota: sandbox.Quota{
					CPUMillis: cfg.Sandbox.CPUMillis, MemoryBytes: cfg.Sandbox.MemoryBytes,
					WorkspaceBytes: cfg.Sandbox.WorkspaceBytes, PIDLimit: cfg.Sandbox.PIDLimit,
					PreviewPortLimit: cfg.Sandbox.PreviewPortLimit,
				},
				TTL: sandbox.TTLPolicy{
					IdleHibernateAfter: cfg.Sandbox.IdleHibernateAfter, MaxRuntime: cfg.Sandbox.MaxRuntime,
				},
			},
			time.Now,
			lifecycle,
		)
		if err != nil {
			return fmt.Errorf("create sandbox provisioning service: %w", err)
		}
	} else {
		logger.Warn("interactive sandbox provisioning is disabled")
	}
	sandboxPlatform, err := sandbox.NewPlatformServiceWithOptions(
		sandboxFacade, sandboxProvisioning,
		sandbox.PlatformServiceOptions{
			Tickets: connectionTickets, Control: sandboxControl, Process: sandboxProcesses,
			Terminal: sandboxTerminals, Port: sandboxPorts, Freeze: candidateFreeze,
		},
	)
	if err != nil {
		return fmt.Errorf("create sandbox platform service: %w", err)
	}
	sandboxStreamHandler, err := transport.NewSandboxStreamHandler(transport.SandboxStreamDependencies{
		Tickets: connectionTickets, Events: sandboxStreamEvents, Terminals: sandboxTerminals,
		Activity: sandboxStore,
		Logger:   logger, Config: cfg.WebSocket,
	})
	if err != nil {
		return fmt.Errorf("create sandbox stream transport: %w", err)
	}
	sandboxHandler, err := transport.NewSandboxHandler(transport.SandboxDependencies{
		Service: sandboxPlatform, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create sandbox transport: %w", err)
	}
	var lspTicketHandler *transport.LSPTicketHandler
	var lspWebSocket http.Handler
	var languageServerRuntime *lsp.ContainerRuntime
	if cfg.LSP.Enabled {
		profileSource, profileErr := lsp.NewRegistryProfileSource(templateRegistry)
		if profileErr != nil {
			return fmt.Errorf("create LSP approved profile source: %w", profileErr)
		}
		authority, authorityErr := lsp.NewAuthoritySource(
			sandboxStore, candidateControls, profileSource, time.Now,
		)
		if authorityErr != nil {
			return fmt.Errorf("create LSP Sandbox/Repository authority: %w", authorityErr)
		}
		grantStore, grantStoreErr := lsp.NewRedisTicketGrantStore(
			dependencies.Redis, cfg.LSP.RedisPrefix+"ticket:", time.Now,
		)
		if grantStoreErr != nil {
			return fmt.Errorf("create LSP one-time ticket store: %w", grantStoreErr)
		}
		ticketService, ticketErr := lsp.NewTicketService(
			grantStore, authority, sandboxAccess, cfg.LSP.TicketTTL, time.Now,
		)
		if ticketErr != nil {
			return fmt.Errorf("create LSP ticket service: %w", ticketErr)
		}
		rateLimiter, rateErr := lsp.NewRedisTicketRateLimiter(
			dependencies.Redis,
			lsp.RedisTicketRateLimiterOptions{
				Prefix: cfg.LSP.RedisPrefix + "rate:", RefillPerSecond: cfg.LSP.RefillPerSecond,
				Burst: cfg.LSP.Burst,
			},
		)
		if rateErr != nil {
			return fmt.Errorf("create LSP Redis admission limiter: %w", rateErr)
		}
		auditSink, auditErr := lsp.NewPostgresTicketAuditSink(dependencies.Postgres)
		if auditErr != nil {
			return fmt.Errorf("create LSP durable security audit sink: %w", auditErr)
		}
		securedTickets, securedTicketErr := lsp.NewSecuredTicketService(
			ticketService, rateLimiter, auditSink, time.Now,
		)
		if securedTicketErr != nil {
			return fmt.Errorf("create secured LSP ticket service: %w", securedTicketErr)
		}
		lspTicketHandler, err = transport.NewLSPTicketHandler(transport.LSPTicketDependencies{
			Tickets: securedTickets, Projects: sandboxPlatform, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
		})
		if err != nil {
			return fmt.Errorf("create LSP ticket transport: %w", err)
		}

		serviceRoots, serviceRootErr := lsp.NewRegistryRuntimeServiceRootSource(templateRegistry)
		if serviceRootErr != nil {
			return fmt.Errorf("create LSP exact TemplateService root source: %w", serviceRootErr)
		}
		lspSnapshots, snapshotErr := lsp.NewRuntimeWorkspaceSnapshotMaterializer(
			cfg.Sandbox.WorkspaceRoot, fileBlobs,
		)
		if snapshotErr != nil {
			return fmt.Errorf("create LSP immutable workspace snapshot materializer: %w", snapshotErr)
		}
		bindingSource, bindingErr := lsp.NewRuntimeBindingSource(
			authority, sandboxStore, candidateControls, lspSnapshots,
			fileBlobs, serviceRoots, time.Now,
		)
		if bindingErr != nil {
			return fmt.Errorf("create LSP exact runtime binding source: %w", bindingErr)
		}
		languageServerRuntime, err = lsp.NewContainerRuntime(lsp.ContainerRuntimeConfig{
			RuntimeBinary: cfg.LSP.RuntimeBinary, DaemonHost: cfg.LSP.DaemonHost,
			CommandTimeout: cfg.LSP.CommandTimeout, CLIOutputBytes: cfg.LSP.CLIOutputMax,
		})
		if err != nil {
			return fmt.Errorf("create LSP container runtime: %w", err)
		}
		defer func(runtime *lsp.ContainerRuntime) {
			if closeErr := runtime.Close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close LSP container runtime: %w", closeErr))
			}
		}(languageServerRuntime)
		lspReadinessCtx, cancelLSPReadiness := context.WithTimeout(startupCtx, cfg.LSP.CommandTimeout)
		readinessErr := languageServerRuntime.Readiness(lspReadinessCtx)
		cancelLSPReadiness()
		if readinessErr != nil {
			return fmt.Errorf("verify LSP container daemon: %w", readinessErr)
		}
		requestLimiter, requestLimiterErr := lsp.NewRedisGatewayRequestRateLimiter(
			dependencies.Redis, cfg.LSP.RedisPrefix+"request-rate:",
		)
		if requestLimiterErr != nil {
			return fmt.Errorf("create LSP Gateway request limiter: %w", requestLimiterErr)
		}
		gatewayAudit, gatewayAuditErr := lsp.NewPostgresGatewayAuditSink(dependencies.Postgres)
		if gatewayAuditErr != nil {
			return fmt.Errorf("create LSP Gateway durable audit sink: %w", gatewayAuditErr)
		}
		editorLeases, editorLeasesErr := lsp.NewRedisGatewayEditorLeaseStore(
			dependencies.Redis, cfg.LSP.RedisPrefix+"editor-lease:",
		)
		if editorLeasesErr != nil {
			return fmt.Errorf("create LSP Gateway editor lease store: %w", editorLeasesErr)
		}
		gatewaySecurity, gatewaySecurityErr := lsp.NewGatewaySecurity(
			requestLimiter, gatewayAudit, editorLeases,
		)
		if gatewaySecurityErr != nil {
			return fmt.Errorf("create LSP Gateway security boundary: %w", gatewaySecurityErr)
		}
		gateway, gatewayErr := lsp.NewGateway(
			bindingSource, languageServerRuntime, bindingSource, gatewaySecurity,
		)
		if gatewayErr != nil {
			return fmt.Errorf("create bound LSP gateway: %w", gatewayErr)
		}
		webSocketGateway, webSocketGatewayErr := transport.NewWebSocketLSPGateway(gateway, cfg.LSP.WriteWait)
		if webSocketGatewayErr != nil {
			return fmt.Errorf("create LSP WebSocket gateway adapter: %w", webSocketGatewayErr)
		}
		lspWebSocket, err = transport.NewLSPWebSocketHandler(transport.LSPWebSocketDependencies{
			Tickets: securedTickets, Gateway: webSocketGateway,
			Config: transport.LSPWebSocketConfig{
				AllowedOrigins: cfg.LSP.AllowedOrigins, TrustedProxies: cfg.HTTP.TrustedProxies,
				RequireTLS: cfg.LSP.RequireTLS, BindTimeout: cfg.LSP.BindTimeout, WriteWait: cfg.LSP.WriteWait,
			},
		})
		if err != nil {
			return fmt.Errorf("create LSP WebSocket transport: %w", err)
		}
		logger.Info("server-owned LSP gateway enabled")
	}
	verificationPlanning, err := verification.NewPostgresCandidatePlanSource(dependencies.Postgres, contentStore)
	if err != nil {
		return fmt.Errorf("create Candidate verification planning source: %w", err)
	}
	verificationControl, err := verification.NewControlService(
		verificationStore, verificationPlanning, sandboxAccess,
	)
	if err != nil {
		return fmt.Errorf("create Candidate verification control service: %w", err)
	}
	canonicalVerificationPlanning, err := verification.NewPostgresCanonicalPlanSource(dependencies.Postgres, contentStore)
	if err != nil {
		return fmt.Errorf("create Canonical verification planning source: %w", err)
	}
	canonicalVerificationControl, err := verification.NewCanonicalControlService(
		verificationStore, canonicalVerificationPlanning, sandboxAccess,
	)
	if err != nil {
		return fmt.Errorf("create Canonical verification control service: %w", err)
	}
	releaseService, err := release.NewService(releaseStore, sandboxAccess)
	if err != nil {
		return fmt.Errorf("create ReleaseBundle service: %w", err)
	}
	releaseDeliveryReadService, err := release.NewDeliveryService(releaseStore, accessControl)
	if err != nil {
		return fmt.Errorf("create release delivery read service: %w", err)
	}
	var releaseDeliveryMutationService *release.DeliveryService
	var releaseDeliveryWorker *release.ReconciledDeliveryWorkerService
	var releaseDeliveryProvider *release.HTTPDeliveryOperationProvider
	var reconciledReleaseStore *release.ReconciledDeliveryStore
	var qualificationReleaseWorker *qualificationrelease.WorkerService
	var qualificationReleaseStore *qualificationrelease.PostgresStore
	var qualificationReleaseSource *qualificationrelease.PostgresCandidateSource
	var qualificationReleaseObserver *qualificationrelease.PostgresControllerObserver
	var qualificationReleaseRuntimeBinding workflowruntime.WorkerRunner
	var workflowQualificationActivationRuntime *WorkflowQualificationActivationRuntime
	if cfg.Delivery.ReleaseWorkerEnabled {
		controllerIdentity := release.DeliveryControllerIdentity{
			SchemaVersion: release.DeliveryControllerIdentitySchemaVersion,
			ID:            cfg.Delivery.ReleaseControllerID, Version: cfg.Delivery.ReleaseControllerVersion,
			Protocol:       cfg.Delivery.ReleaseControllerProtocol,
			TrustKeyDigest: cfg.Delivery.ReleaseControllerTrustKeyDigest,
		}
		releaseDeliveryProvider, err = release.NewHTTPDeliveryOperationProvider(release.HTTPDeliveryOperationProviderConfig{
			BaseURL: cfg.Delivery.ReleaseControllerURL, BearerToken: cfg.Delivery.ReleaseControllerToken,
			RequestTimeout: cfg.Delivery.ReleaseRequestTimeout, MaxResponseBytes: cfg.Delivery.ReleaseResponseMax,
			ExpectedIdentity: controllerIdentity,
		}, nil)
		if err != nil {
			return fmt.Errorf("create reconciled release delivery provider: %w", err)
		}
		reconciledStore, storeErr := release.NewReconciledDeliveryStore(releaseStore, controllerIdentity)
		if storeErr != nil {
			return fmt.Errorf("create reconciled release delivery store: %w", storeErr)
		}
		reconciledReleaseStore = reconciledStore
		// Do not start a worker (or expose delivery mutations) until both the
		// durable schema/active-operation authority and the remote pinned
		// identity are proven. In particular, an authority rotation must drain
		// operations under the old configuration instead of first claiming them
		// with the new worker and leaving their leases to churn forever.
		releaseReadinessCtx, cancelReleaseReadiness := context.WithTimeout(ctx, cfg.Dependencies.ReadinessTimeout)
		if err := reconciledStore.Readiness(releaseReadinessCtx); err != nil {
			cancelReleaseReadiness()
			return fmt.Errorf("verify reconciled release delivery store before worker startup: %w", err)
		}
		if err := releaseDeliveryProvider.Readiness(releaseReadinessCtx); err != nil {
			cancelReleaseReadiness()
			return fmt.Errorf("verify release delivery controller before worker startup: %w", err)
		}
		cancelReleaseReadiness()
		releaseDeliveryMutationService, err = release.NewDeliveryService(reconciledStore, accessControl)
		if err != nil {
			return fmt.Errorf("create release delivery control service: %w", err)
		}
		// Use the authority-aware store for reads too while execution is
		// enabled, without coupling read route registration to the worker.
		releaseDeliveryReadService = releaseDeliveryMutationService
		workerID := cfg.Delivery.ReleaseWorkerID
		if workerID == "" {
			workerID = cfg.ServiceName + "-release-delivery-" + uuid.NewString()
		}
		releaseWorker, workerErr := release.NewReconciledDeliveryWorker(
			reconciledStore, releaseDeliveryProvider, workerID,
			cfg.Delivery.ReleaseLeaseDuration, cfg.Delivery.ReleaseReconcileDelay,
		)
		if workerErr != nil {
			return fmt.Errorf("create release delivery worker: %w", workerErr)
		}
		releaseDeliveryWorker, workerErr = release.NewReconciledDeliveryWorkerService(
			releaseWorker, cfg.Delivery.ReleasePollInterval,
		)
		if workerErr != nil {
			return fmt.Errorf("create release delivery worker service: %w", workerErr)
		}
		releaseDeliveryWorker.SetErrorHandler(func(workerErr error) {
			logger.Error("release delivery operation will be retried or requires reconciliation", "error", workerErr)
		})
	}
	if cfg.QualificationRelease.Enabled {
		qualifiedCtx, cancelQualified := context.WithTimeout(ctx, cfg.Dependencies.ConnectTimeout)
		qualifiedDatabase, poolErr := qualificationrelease.OpenPostgresPool(
			qualifiedCtx,
			qualificationrelease.PostgresPoolConfig{
				DSN: cfg.QualificationRelease.PostgresDSN, Schema: cfg.Postgres.Schema,
				MaxOpenConns:    cfg.QualificationRelease.MaxOpenConns,
				MaxIdleConns:    cfg.QualificationRelease.MaxIdleConns,
				ConnMaxLifetime: cfg.Postgres.ConnMaxLifetime,
				ConnMaxIdleTime: cfg.Postgres.ConnMaxIdleTime,
			},
		)
		cancelQualified()
		if poolErr != nil {
			return fmt.Errorf("connect qualified release operator: %w", poolErr)
		}
		defer func() {
			if closeErr := qualifiedDatabase.Close(); closeErr != nil {
				logger.Warn("close qualified release operator pool", "error", closeErr)
			}
		}()
		qualificationReleaseStore, err = qualificationrelease.NewPostgresStore(
			qualifiedDatabase,
			qualificationrelease.PostgresStoreConfig{
				MaxTransactionRetries: cfg.QualificationRelease.MaxTransactionRetries,
			},
		)
		if err != nil {
			return fmt.Errorf("create qualified release operator store: %w", err)
		}
		qualificationReleaseSource, err = qualificationrelease.NewPostgresCandidateSource(dependencies.PostgresSQL)
		if err != nil {
			return fmt.Errorf("create qualified release Workflow candidate source: %w", err)
		}
		qualificationReleaseObserver, err = qualificationrelease.NewPostgresControllerObserver(dependencies.PostgresSQL)
		if err != nil {
			return fmt.Errorf("create qualified release Controller observer: %w", err)
		}
		expectedController := qualificationrelease.ControllerIdentity{
			SchemaVersion: qualificationrelease.ControllerIdentitySchemaVersion,
			ID:            cfg.Delivery.ReleaseControllerID, Version: cfg.Delivery.ReleaseControllerVersion,
			Protocol:       cfg.Delivery.ReleaseControllerProtocol,
			TrustKeyDigest: cfg.Delivery.ReleaseControllerTrustKeyDigest,
		}
		qualifiedReadinessCtx, cancelReadiness := context.WithTimeout(ctx, cfg.Dependencies.ReadinessTimeout)
		if err := qualificationReleaseStore.Readiness(
			qualifiedReadinessCtx, expectedController, dependencies.PostgresSQL,
		); err != nil {
			cancelReadiness()
			return fmt.Errorf("verify qualified release operator and Controller bootstrap: %w", err)
		}
		if err := qualificationReleaseSource.Readiness(qualifiedReadinessCtx); err != nil {
			cancelReadiness()
			return fmt.Errorf("verify qualified release Workflow candidate source: %w", err)
		}
		if err := qualificationReleaseObserver.Readiness(qualifiedReadinessCtx); err != nil {
			cancelReadiness()
			return fmt.Errorf("verify qualified release Controller observer: %w", err)
		}
		cancelReadiness()
		qualifiedPublisher, publisherErr := qualificationrelease.NewQualifiedReleaseControllerPublisher(
			qualificationReleaseStore, qualificationReleaseObserver,
			qualificationrelease.PublisherConfig{
				LeaseDuration: cfg.QualificationRelease.LeaseDuration,
				PollInterval:  cfg.QualificationRelease.ControllerPollInterval,
			},
		)
		if publisherErr != nil {
			return fmt.Errorf("create qualified Release Controller publisher: %w", publisherErr)
		}
		workerID := cfg.QualificationRelease.WorkerID
		if workerID == "" {
			workerID = "qualification-release-" + uuid.NewString()
		}
		qualificationReleaseWorker, err = qualificationrelease.NewWorkerService(
			qualificationReleaseSource, qualifiedPublisher, workerID,
			cfg.QualificationRelease.WorkerConcurrency,
			cfg.QualificationRelease.SchedulerPollInterval,
		)
		if err != nil {
			return fmt.Errorf("create qualified release worker service: %w", err)
		}
		qualificationReleaseWorker.SetErrorHandler(func(workerErr error) {
			logger.Error("qualified release operation will be reclaimed or requires intervention", "error", workerErr)
		})
		qualificationReleaseRuntimeBinding = workflowruntime.NewQualifiedReleaseControllerRuntimeBinding()
	}
	if cfg.WorkflowQualificationActivation.WorkerEnabled {
		activationRuntime, operatorDatabase, activationErr := newWorkflowQualificationActivationRuntimeFromConfig(
			ctx,
			cfg,
			dependencies.PostgresSQL,
			dependencies.JetStream,
		)
		if activationErr != nil {
			return fmt.Errorf("create workflow qualification activation runtime: %w", activationErr)
		}
		workflowQualificationActivationRuntime = activationRuntime
		defer func() {
			if closeErr := operatorDatabase.Close(); closeErr != nil {
				logger.Warn("close workflow input authority operator pool", "error", closeErr)
			}
		}()
	}
	releaseHandler, err := transport.NewReleaseHandler(transport.ReleaseDependencies{
		Service: releaseService, DeliveryRead: releaseDeliveryReadService,
		DeliveryMutation: releaseDeliveryMutationService, MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create ReleaseBundle transport: %w", err)
	}
	verificationHandler, err := transport.NewVerificationHandler(transport.VerificationDependencies{
		Service: verificationControl, Canonical: canonicalVerificationControl, Sessions: sandboxPlatform,
		MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
	})
	if err != nil {
		return fmt.Errorf("create Candidate verification transport: %w", err)
	}
	var verificationWorker *verification.CandidateWorkerService
	var canonicalVerificationWorker *verification.CanonicalWorkerService
	if cfg.Verification.WorkerEnabled {
		verificationExecutor, executorErr := verification.NewDockerCandidateExecutor(
			verification.DockerCandidateExecutorConfig{
				RuntimeBinary: cfg.Verification.RuntimeBinary, DaemonHost: cfg.Verification.DaemonHost,
				WorkspaceRoot: cfg.Verification.WorkspaceRoot, Memory: cfg.Verification.Memory,
				CPUs: cfg.Verification.CPUs, PIDs: cfg.Verification.PIDs,
				OutputLimit: cfg.Verification.OutputMax, TempBytes: cfg.Verification.TempBytes,
				User: "10001:10001",
			},
			contentStore,
			nil,
		)
		if executorErr != nil {
			return fmt.Errorf("create Candidate verification executor: %w", executorErr)
		}
		verificationMaterializer, materializerErr := verification.NewCandidateWorkspaceMaterializer(
			dependencies.Postgres, treeStore, fileBlobs, cfg.Verification.WorkspaceRoot,
			verificationExecutor,
		)
		if materializerErr != nil {
			return fmt.Errorf("create Candidate verification materializer: %w", materializerErr)
		}
		workerID := cfg.Verification.WorkerID
		if workerID == "" {
			workerID = cfg.ServiceName + "-verification-" + uuid.NewString()
		}
		candidateWorker, workerErr := verification.NewCandidateWorker(
			verificationStore, verificationMaterializer, verificationExecutor,
			verification.CandidateWorkerConfig{
				ActorID: verification.VerificationWorkerActorID, WorkerID: workerID,
				LeaseDuration:     cfg.Verification.LeaseDuration,
				HeartbeatInterval: cfg.Verification.Heartbeat,
			},
			nil,
			nil,
		)
		if workerErr != nil {
			return fmt.Errorf("create Candidate verification worker: %w", workerErr)
		}
		verificationWorker, workerErr = verification.NewCandidateWorkerService(
			candidateWorker, cfg.Verification.PollInterval, logger,
		)
		if workerErr != nil {
			return fmt.Errorf("create Candidate verification worker service: %w", workerErr)
		}
		canonicalWorkspaceSource, sourceErr := verification.NewPostgresCanonicalWorkspaceSource(
			dependencies.Postgres, contentStore,
		)
		if sourceErr != nil {
			return fmt.Errorf("create Canonical verification workspace source: %w", sourceErr)
		}
		canonicalMaterializer, materializerErr := verification.NewCanonicalWorkspaceMaterializer(
			canonicalWorkspaceSource, cfg.Verification.WorkspaceRoot, verificationExecutor,
		)
		if materializerErr != nil {
			return fmt.Errorf("create Canonical verification materializer: %w", materializerErr)
		}
		canonicalArtifacts, collectorErr := verification.NewContentCanonicalArtifactCollector(contentStore)
		if collectorErr != nil {
			return fmt.Errorf("create Canonical release artifact collector: %w", collectorErr)
		}
		canonicalWorker, canonicalWorkerErr := verification.NewCanonicalWorker(
			verificationStore, canonicalMaterializer, verificationExecutor, canonicalArtifacts,
			verification.CandidateWorkerConfig{
				ActorID: verification.VerificationWorkerActorID, WorkerID: workerID,
				LeaseDuration:     cfg.Verification.LeaseDuration,
				HeartbeatInterval: cfg.Verification.Heartbeat,
			},
			nil,
			nil,
		)
		if canonicalWorkerErr != nil {
			return fmt.Errorf("create Canonical verification worker: %w", canonicalWorkerErr)
		}
		canonicalVerificationWorker, canonicalWorkerErr = verification.NewCanonicalWorkerService(
			canonicalWorker, cfg.Verification.PollInterval, logger,
		)
		if canonicalWorkerErr != nil {
			return fmt.Errorf("create Canonical verification worker service: %w", canonicalWorkerErr)
		}
	}
	var agentHandler *transport.AgentHandler
	var agentStreamRelay *agent.StreamRelay
	var agentExecutionWorker *agent.ExecutionWorker
	var agentModelGateway http.Handler
	if cfg.Agent.Enabled {
		_, qualifiedSchemaHash, schemaErr := agent.QualifiedOutputSchema()
		if schemaErr != nil {
			return fmt.Errorf("qualify Agent output schema: %w", schemaErr)
		}
		if cfg.Agent.OutputSchemaHash != qualifiedSchemaHash {
			return fmt.Errorf(
				"qualify Agent output schema: configured hash %q does not match embedded hash %q",
				cfg.Agent.OutputSchemaHash, qualifiedSchemaHash,
			)
		}
		_, qualifiedPromptHash := agent.QualifiedPromptTemplate()
		if cfg.Agent.PromptHash != qualifiedPromptHash {
			return fmt.Errorf(
				"qualify Agent prompt template: configured hash %q does not match embedded hash %q",
				cfg.Agent.PromptHash, qualifiedPromptHash,
			)
		}
		agentStore, storeErr := agent.NewPostgresStore(dependencies.Postgres)
		if storeErr != nil {
			return fmt.Errorf("create Agent control store: %w", storeErr)
		}
		agentEvidence, evidenceErr := agent.NewEvidenceStore(contentStore)
		if evidenceErr != nil {
			return fmt.Errorf("create Agent immutable evidence store: %w", evidenceErr)
		}
		agentReview, reviewErr := agent.NewReviewService(agentStore, agentEvidence, sandboxAccess)
		if reviewErr != nil {
			return fmt.Errorf("create Agent evidence review service: %w", reviewErr)
		}
		agentTreeResolver, treeErr := agent.NewPostgresCandidateTreeResolver(dependencies.Postgres, treeStore)
		if treeErr != nil {
			return fmt.Errorf("create Agent exact Candidate tree resolver: %w", treeErr)
		}
		agentPatchFiles, patchFilesErr := agent.NewPatchFileReviewService(
			agentReview, agentStore, agentTreeResolver, fileBlobs,
		)
		if patchFilesErr != nil {
			return fmt.Errorf("create Agent patch file review service: %w", patchFilesErr)
		}
		agentMerge, mergeErr := agent.NewPatchMergeService(
			agentStore, agentReview, agentTreeResolver, sandboxFacade,
			agentStore, sandboxAccess, time.Now,
		)
		if mergeErr != nil {
			return fmt.Errorf("create Agent patch merge service: %w", mergeErr)
		}
		agentUndo, undoErr := agent.NewPatchUndoService(
			agentStore, treeStore, sandboxFacade, agentStore, sandboxAccess, time.Now,
		)
		if undoErr != nil {
			return fmt.Errorf("create Agent patch undo service: %w", undoErr)
		}
		agentHistory, historyErr := agent.NewPatchHistoryService(agentStore, agentStore, sandboxAccess)
		if historyErr != nil {
			return fmt.Errorf("create Agent patch history service: %w", historyErr)
		}
		planningSource, sourceErr := agent.NewPostgresPlanningSource(
			dependencies.Postgres,
			contentStore,
			candidateStore,
			fileBlobs,
			pathPolicies,
			agent.PostgresPlanningSourceConfig{
				OutputSchemaHash: cfg.Agent.OutputSchemaHash,
				AllowedTools:     cfg.Agent.AllowedTools,
				Budgets: agent.TaskBudgets{
					WallTimeSeconds: int64(cfg.Agent.WallTime / time.Second),
					MaxInputTokens:  cfg.Agent.MaxInputTokens,
					MaxOutputTokens: cfg.Agent.MaxOutputTokens,
					MaxCommands:     cfg.Agent.MaxCommands,
					MaxLogBytes:     cfg.Agent.MaxLogBytes,
					MaxPatchBytes:   cfg.Agent.MaxPatchBytes,
				},
				MaxContextFiles: cfg.Agent.MaxContextFiles,
			},
		)
		if sourceErr != nil {
			return fmt.Errorf("create Agent authoritative PlanningSource: %w", sourceErr)
		}
		planner, plannerErr := agent.NewDeterministicPlanner(planningSource, time.Now)
		if plannerErr != nil {
			return fmt.Errorf("create Agent deterministic planner: %w", plannerErr)
		}
		executorProfiles, profileErr := agent.NewStaticExecutorProfiles(map[string]agent.ExecutorIdentity{
			cfg.Agent.ProfileID: {
				Adapter: cfg.Agent.Adapter, Provider: cfg.Agent.Provider, Model: cfg.Agent.Model,
				RunnerImageDigest: sandboxRunnerDigest(cfg.Agent.RunnerImage),
				ModelPolicyHash:   cfg.Agent.ModelPolicyHash,
				ParametersHash:    cfg.Agent.ParametersHash,
				PromptHash:        cfg.Agent.PromptHash,
				OutputSchemaHash:  cfg.Agent.OutputSchemaHash,
				ToolchainHash:     cfg.Agent.ToolchainHash,
			},
		})
		if profileErr != nil {
			return fmt.Errorf("create Agent executor profile: %w", profileErr)
		}
		agentControl, controlErr := agent.NewControlService(
			agentStore, planner, executorProfiles, sandboxAccess, time.Now,
		)
		if controlErr != nil {
			return fmt.Errorf("create Agent control service: %w", controlErr)
		}
		agentHandler, err = transport.NewAgentHandler(transport.AgentDependencies{
			Service: agentControl, Review: agentReview, PatchFiles: agentPatchFiles,
			Merge: agentMerge, Undo: agentUndo,
			History:          agentHistory,
			Sessions:         sandboxPlatform,
			MaxJSONBodyBytes: cfg.HTTP.MaxJSONBodyBytes,
		})
		if err != nil {
			return fmt.Errorf("create Agent transport: %w", err)
		}
		agentStreamRelay, err = agent.NewStreamRelay(
			dependencies.Postgres,
			sandboxStreamEvents,
			agent.StreamRelayConfig{
				BatchSize: cfg.Outbox.BatchSize, PollInterval: cfg.Outbox.PollInterval,
				ClaimTTL: cfg.Outbox.ClaimTTL, MaxAttempts: cfg.Outbox.MaxAttempts,
				PublishTimeout: cfg.Outbox.PublishWait,
			},
			logger,
		)
		if err != nil {
			return fmt.Errorf("create Agent stream relay: %w", err)
		}
		if cfg.Agent.WorkerEnabled {
			templateContext, contextErr := agent.NewPostgresTemplateContextReader(dependencies.Postgres)
			if contextErr != nil {
				return fmt.Errorf("create Agent Template context reader: %w", contextErr)
			}
			contextMaterializer, contextErr := agent.NewContextMaterializer(contentStore, fileBlobs, templateContext)
			if contextErr != nil {
				return fmt.Errorf("create Agent context materializer: %w", contextErr)
			}
			worktrees, worktreeErr := agent.NewWorktreeManager(cfg.Agent.WorktreeRoot, fileBlobs)
			if worktreeErr != nil {
				return fmt.Errorf("create Agent isolated worktree manager: %w", worktreeErr)
			}
			capabilities, capabilityErr := agent.NewRedisModelCapabilityAuthority(
				dependencies.Redis,
				cfg.Agent.GatewayRedisPrefix,
				cfg.Agent.GatewayBaseURL,
			)
			if capabilityErr != nil {
				return fmt.Errorf("create Agent model capability authority: %w", capabilityErr)
			}
			modelGateway, gatewayErr := agent.NewModelGateway(agent.ModelGatewayConfig{
				UpstreamResponsesURL: cfg.AI.BaseURL,
				UpstreamAPIKey:       cfg.AI.APIKey,
				Organization:         cfg.AI.Organization,
				UpstreamProject:      cfg.AI.Project,
				MaxInputBytes:        cfg.AI.MaxInputBytes,
				MaxOutputBytes:       cfg.AI.MaxOutputBytes,
				RequestTimeout:       cfg.Agent.WallTime + 30*time.Second,
			}, capabilities, nil, logger)
			if gatewayErr != nil {
				return fmt.Errorf("create Agent Model Gateway: %w", gatewayErr)
			}
			runner, runnerErr := agent.NewDockerCodexRunner(agent.DockerCodexRunnerConfig{
				RuntimeBinary: cfg.Agent.RuntimeBinary,
				DaemonHost:    cfg.Agent.DaemonHost,
				RunnerImage:   cfg.Agent.RunnerImage,
				Network:       cfg.Agent.RunnerNetwork,
				Memory:        cfg.Agent.RunnerMemory,
				CPUs:          cfg.Agent.RunnerCPUs,
				PIDs:          cfg.Agent.RunnerPIDs,
				OutputLimit:   cfg.Agent.RunnerOutputMax,
				User:          "10001:10001",
			}, capabilities, agent.OSContainerCommandExecutor{OutputLimit: cfg.Agent.RunnerOutputMax})
			if runnerErr != nil {
				return fmt.Errorf("create digest-pinned Codex Runner: %w", runnerErr)
			}
			workerLifecycle, workerErr := agent.NewWorkerService(agentStore, sandboxAccess)
			if workerErr != nil {
				return fmt.Errorf("create Agent worker lifecycle: %w", workerErr)
			}
			workerID := cfg.Agent.WorkerID
			if workerID == "" {
				workerID = cfg.ServiceName + "-agent-" + uuid.NewString()
			}
			agentExecutionWorker, workerErr = agent.NewExecutionWorker(
				agentStore,
				workerLifecycle,
				agentTreeResolver,
				worktrees,
				contextMaterializer,
				runner,
				agentEvidence,
				fileBlobs,
				agent.ExecutionWorkerConfig{
					WorkerID:      workerID,
					ClaimBatch:    cfg.Agent.ClaimBatch,
					PollInterval:  cfg.Agent.PollInterval,
					LeaseDuration: cfg.Agent.LeaseDuration,
					Heartbeat:     cfg.Agent.Heartbeat,
				},
				logger,
			)
			if workerErr != nil {
				return fmt.Errorf("create Agent execution worker: %w", workerErr)
			}
			agentModelGateway = modelGateway
		}
		logger.Info(
			"Agent control plane enabled",
			"profile_id", cfg.Agent.ProfileID,
			"runner_digest", sandboxRunnerDigest(cfg.Agent.RunnerImage),
			"worker_enabled", cfg.Agent.WorkerEnabled,
		)
	} else {
		logger.Warn("Agent control plane is disabled")
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
		constructorService,
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
		BlueprintPages:             workflowruntime.CoreBlueprintPageFanOutResolver{Artifacts: artifactService, Proposals: proposalService},
		Quality:                    deliveryServices.WorkflowQuality,
		QualityManifest:            workflowruntime.CoreQualityManifestResolver{Database: dependencies.Postgres},
		Publisher:                  deliveryServices.WorkflowPublisher,
		ProfileV3Enabled:           cfg.Workflow.ProfileV3RuntimeEnabled,
		QualifiedReleaseController: qualificationReleaseRuntimeBinding,
		DefaultModel:               cfg.AI.DefaultModel,
	})
	if err != nil {
		return fmt.Errorf("create workflow engine: %w", err)
	}
	workflowEngine.LeaseDuration = cfg.Workflow.LeaseDuration
	workflowFacade := workflowruntime.Facade{Engine: workflowEngine, Store: workflowStore, Access: accessControl, Governance: projectService}
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
	proposalAutomation, err := automation.NewService(proposalService, artifactService, reviewService)
	if err != nil {
		return fmt.Errorf("create proposal automation service: %w", err)
	}
	apiTransport := transport.NewServer(transport.Services{
		Auth: authService, Projects: projectService, Members: memberService, Access: accessControl,
		Artifacts: artifactService, Traces: traceService, Reviews: reviewService, Comments: commentService,
		Baselines: baselineService, Impacts: impactService, Proposals: proposalService,
		Automation: proposalAutomation,
		Workbench:  workbenchService, Implementation: implementationService, Activity: activityService,
		Generation: generationService, Collaboration: documentCollaborationService,
	}, cfg, logger)
	idempotencyRequestLimit := cfg.HTTP.MaxJSONBodyBytes
	if idempotencyRequestLimit < worksgithub.MaxRequestBytes {
		idempotencyRequestLimit = worksgithub.MaxRequestBytes
	}
	if idempotencyRequestLimit < designimport.MaxRequestBytes {
		idempotencyRequestLimit = designimport.MaxRequestBytes
	}
	if idempotencyRequestLimit < repository.MaxFileBytes {
		idempotencyRequestLimit = repository.MaxFileBytes
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
	runtimeFatalErrors := make(chan error, 1)
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
	if agentStreamRelay != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			agentStreamRelay.Run(runtimeCtx)
		}()
	}
	if sandboxDeadlineWorker != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if workerErr := sandboxDeadlineWorker.Run(runtimeCtx); workerErr != nil &&
				!errors.Is(workerErr, context.Canceled) {
				logger.Error("SandboxSession lifecycle deadline worker stopped", "error", workerErr)
			}
		}()
	}
	if agentExecutionWorker != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if workerErr := agentExecutionWorker.Run(runtimeCtx); workerErr != nil &&
				!errors.Is(workerErr, context.Canceled) {
				logger.Error("Agent execution worker stopped", "error", workerErr)
			}
		}()
	}
	if verificationWorker != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if workerErr := verificationWorker.Run(runtimeCtx); workerErr != nil &&
				!errors.Is(workerErr, context.Canceled) {
				logger.Error("Candidate verification worker stopped", "error", workerErr)
			}
		}()
	}
	if canonicalVerificationWorker != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if workerErr := canonicalVerificationWorker.Run(runtimeCtx); workerErr != nil &&
				!errors.Is(workerErr, context.Canceled) {
				logger.Error("Canonical verification worker stopped", "error", workerErr)
			}
		}()
	}
	if releaseDeliveryWorker != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if workerErr := releaseDeliveryWorker.Run(runtimeCtx); workerErr != nil &&
				!errors.Is(workerErr, context.Canceled) {
				logger.Error("release delivery worker stopped", "error", workerErr)
			}
		}()
	}
	if qualificationReleaseWorker != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if workerErr := qualificationReleaseWorker.Run(runtimeCtx); workerErr != nil &&
				!errors.Is(workerErr, context.Canceled) {
				logger.Error("qualified release worker stopped", "error", workerErr)
			}
		}()
	}
	if workflowQualificationActivationRuntime != nil {
		runtimeWorkers.Add(1)
		go func() {
			defer runtimeWorkers.Done()
			if workerErr := workflowQualificationActivationRuntime.Run(runtimeCtx); workerErr != nil &&
				!errors.Is(workerErr, context.Canceled) {
				select {
				case runtimeFatalErrors <- fmt.Errorf("workflow qualification activation worker stopped: %w", workerErr):
				case <-runtimeCtx.Done():
				}
			}
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
	if releaseDeliveryProvider != nil {
		readinessChecks["release_delivery_controller"] = func(ctx context.Context) error {
			if err := reconciledReleaseStore.Readiness(ctx); err != nil {
				return err
			}
			return releaseDeliveryProvider.Readiness(ctx)
		}
	}
	if qualificationReleaseWorker != nil {
		readinessChecks["qualification_release_publisher"] = func(ctx context.Context) error {
			expectedController := qualificationrelease.ControllerIdentity{
				SchemaVersion: qualificationrelease.ControllerIdentitySchemaVersion,
				ID:            cfg.Delivery.ReleaseControllerID, Version: cfg.Delivery.ReleaseControllerVersion,
				Protocol:       cfg.Delivery.ReleaseControllerProtocol,
				TrustKeyDigest: cfg.Delivery.ReleaseControllerTrustKeyDigest,
			}
			if err := qualificationReleaseStore.Readiness(ctx, expectedController, dependencies.PostgresSQL); err != nil {
				return err
			}
			if err := qualificationReleaseSource.Readiness(ctx); err != nil {
				return err
			}
			return qualificationReleaseObserver.Readiness(ctx)
		}
	}
	if workflowQualificationActivationRuntime != nil {
		readinessChecks["workflow_qualification_activation"] = workflowQualificationActivationRuntime.Readiness
	}
	readinessChecks["nats_event_stream"] = func(checkCtx context.Context) error {
		_, checkErr := dependencies.JetStream.StreamInfo(events.DefaultStreamName, nats.Context(checkCtx))
		return checkErr
	}
	readinessChecks["realtime_fanout"] = fanoutSupervisor.Readiness
	readinessChecks["workflow_execution_profiles"] = workflowEngine.Readiness
	if interactiveRuntime != nil {
		readinessChecks["interactive_sandbox_runtime"] = interactiveRuntime.Readiness
	}
	if languageServerRuntime != nil {
		readinessChecks["language_server_runtime"] = func(checkCtx context.Context) error {
			return languageServerRuntime.Readiness(checkCtx)
		}
	}
	if sandboxDeadlineWorker != nil {
		readinessChecks["sandbox_lifecycle_deadline_worker"] = sandboxDeadlineWorker.Readiness
	}
	if agentStreamRelay != nil {
		readinessChecks["agent_stream_relay"] = agentStreamRelay.Readiness
	}
	if agentExecutionWorker != nil {
		readinessChecks["agent_execution_worker"] = agentExecutionWorker.Readiness
	}
	if verificationWorker != nil {
		readinessChecks["candidate_verification_worker"] = verificationWorker.Readiness
	}
	if canonicalVerificationWorker != nil {
		readinessChecks["canonical_verification_worker"] = canonicalVerificationWorker.Readiness
	}
	readiness := health.NewReadiness(cfg.Dependencies.ReadinessTimeout, readinessChecks)
	router, err := httpapi.NewRouter(cfg, logger, httpapi.RouterOptions{
		Readiness: readiness, WebSocket: websocketHandler, SandboxWebSocket: sandboxStreamHandler,
		ModelGateway: agentModelGateway,
		Transport:    apiTransport, Authentication: authService, Idempotency: idempotency,
		Workflow: workflowHandler, Conversation: conversationHandler, GitHub: githubHandler, Data: dataHandler,
		PublicData: publicDataHandler, Delivery: deliveryHandler, DesignImports: designImportHandler,
		Constructor: constructorHandler, TemplateRegistry: templateRegistryHandler,
		Repository: repositoryHandler, Sandbox: sandboxHandler,
		LSPTickets: lspTicketHandler, LSPWebSocket: lspWebSocket,
		Verification: verificationHandler, Agent: agentHandler,
		Release: releaseHandler,
	})
	if err != nil {
		stopRuntime()
		return fmt.Errorf("create HTTP router: %w", err)
	}

	var serverHandler http.Handler = router
	if sandboxPorts != nil {
		previewHandler, previewErr := transport.NewSandboxPreviewHandler(
			router, sandboxPorts, cfg.Sandbox.PreviewPublicOrigin, cfg.CORS.AllowedOrigins,
			[]string{cfg.Security.Session.CookieName, cfg.Security.CSRF.CookieName}, logger,
		)
		if previewErr != nil {
			stopRuntime()
			return fmt.Errorf("create sandbox preview transport: %w", previewErr)
		}
		serverHandler = previewHandler
	}
	server := &http.Server{
		Addr:              cfg.HTTP.Address(),
		Handler:           serverHandler,
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

	var runtimeFailure error
	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case runtimeFailure = <-runtimeFatalErrors:
		logger.Error("critical runtime worker failed; shutting down", "error", runtimeFailure)
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
	if runtimeFailure != nil {
		return runtimeFailure
	}
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

func repositorySearchAdmissionOptions(
	configured config.RepositorySearchAdmissionConfig,
) repository.RedisExactTreeSearchAdmissionOptions {
	bucket := func(value config.RepositorySearchRateBucketConfig) repository.ExactTreeSearchAdmissionBucketLimits {
		return repository.ExactTreeSearchAdmissionBucketLimits{
			RefillTokens:   value.RefillTokens,
			RefillInterval: value.RefillInterval,
			Burst:          value.Burst,
		}
	}
	return repository.RedisExactTreeSearchAdmissionOptions{
		Prefix:  configured.RedisPrefix,
		Timeout: configured.Timeout,
		Query: repository.ExactTreeSearchAdmissionOperationLimits{
			Project: bucket(configured.QueryProject),
			Actor:   bucket(configured.QueryActor),
		},
		FirstBuilder: repository.ExactTreeSearchAdmissionOperationLimits{
			Project: bucket(configured.BuildProject),
			Actor:   bucket(configured.BuildActor),
		},
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
