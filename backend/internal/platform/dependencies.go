package platform

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/health"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type Dependencies struct {
	Postgres    *gorm.DB
	PostgresSQL *sql.DB
	Redis       *redis.Client
	Mongo       *mongo.Client
	MongoDB     *mongo.Database
	NATS        *nats.Conn
	JetStream   nats.JetStreamContext

	logger *slog.Logger
}

func Connect(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Dependencies, error) {
	connectCtx, cancel := context.WithTimeout(ctx, cfg.Dependencies.ConnectTimeout)
	defer cancel()

	dependencies := &Dependencies{logger: logger}
	if err := dependencies.connectPostgres(connectCtx, cfg.Postgres); err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := dependencies.connectRedis(connectCtx, cfg.Redis); err != nil {
		_ = dependencies.Close(context.Background())
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	if err := dependencies.connectMongo(connectCtx, cfg.Mongo, cfg.ServiceName); err != nil {
		_ = dependencies.Close(context.Background())
		return nil, fmt.Errorf("connect mongo: %w", err)
	}
	if err := dependencies.connectNATS(connectCtx, cfg.NATS, cfg.Dependencies.ConnectTimeout); err != nil {
		_ = dependencies.Close(context.Background())
		return nil, fmt.Errorf("connect NATS JetStream: %w", err)
	}

	logger.Info("infrastructure dependencies connected",
		"postgres", true,
		"redis", true,
		"mongo", true,
		"nats_jetstream", true,
	)
	return dependencies, nil
}

func (d *Dependencies) connectPostgres(ctx context.Context, cfg config.PostgresConfig) error {
	dsn, err := postgresDSNWithSchema(cfg.DSN, cfg.Schema)
	if err != nil {
		return errors.New("prepare PostgreSQL connection configuration")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return errors.New("open PostgreSQL driver configuration")
	}
	sqlDB, err := database.DB()
	if err != nil {
		return errors.New("obtain PostgreSQL connection pool")
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return errors.New("PostgreSQL readiness probe failed")
	}
	d.Postgres = database
	d.PostgresSQL = sqlDB
	return nil
}

func postgresDSNWithSchema(rawDSN, schema string) (string, error) {
	parsed, err := url.Parse(rawDSN)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (d *Dependencies) connectRedis(ctx context.Context, cfg config.RedisConfig) error {
	redisOptions := &redis.Options{
		Addr:                  cfg.Address,
		Password:              cfg.Password,
		DB:                    cfg.DB,
		ContextTimeoutEnabled: true,
	}
	if cfg.UseTLS {
		redisOptions.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(redisOptions)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return err
	}
	d.Redis = client
	return nil
}

func (d *Dependencies) connectMongo(ctx context.Context, cfg config.MongoConfig, serviceName string) error {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.URI).SetAppName(serviceName))
	if err != nil {
		return err
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return err
	}
	d.Mongo = client
	d.MongoDB = client.Database(cfg.Database)
	return nil
}

func (d *Dependencies) connectNATS(ctx context.Context, cfg config.NATSConfig, timeout time.Duration) error {
	connection, err := nats.Connect(
		cfg.URL,
		nats.Name(cfg.Name),
		nats.Timeout(timeout),
		nats.DrainTimeout(timeout),
		nats.RetryOnFailedConnect(false),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				d.logger.Warn("NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(connection *nats.Conn) {
			d.logger.Info("NATS reconnected", "server", connection.ConnectedUrlRedacted())
		}),
		nats.ErrorHandler(func(_ *nats.Conn, subscription *nats.Subscription, err error) {
			attrs := []any{"error", err}
			if subscription != nil {
				attrs = append(attrs, "subject", subscription.Subject)
			}
			d.logger.Error("NATS asynchronous error", attrs...)
		}),
	)
	if err != nil {
		return err
	}
	jetStream, err := connection.JetStream(nats.MaxWait(timeout))
	if err != nil {
		connection.Close()
		return err
	}
	if _, err := jetStream.AccountInfo(nats.Context(ctx)); err != nil {
		connection.Close()
		return err
	}
	d.NATS = connection
	d.JetStream = jetStream
	return nil
}

func (d *Dependencies) Checks() map[string]health.Check {
	return map[string]health.Check{
		"mongo": func(ctx context.Context) error {
			if d.Mongo == nil {
				return errors.New("mongo client is unavailable")
			}
			return d.Mongo.Ping(ctx, readpref.Primary())
		},
		"nats_jetstream": func(ctx context.Context) error {
			if d.NATS == nil || d.JetStream == nil || d.NATS.Status() != nats.CONNECTED {
				return errors.New("NATS JetStream is unavailable")
			}
			_, err := d.JetStream.AccountInfo(nats.Context(ctx))
			return err
		},
		"postgres": func(ctx context.Context) error {
			if d.PostgresSQL == nil {
				return errors.New("postgres client is unavailable")
			}
			return d.PostgresSQL.PingContext(ctx)
		},
		"redis": func(ctx context.Context) error {
			if d.Redis == nil {
				return errors.New("redis client is unavailable")
			}
			return d.Redis.Ping(ctx).Err()
		},
	}
}

func (d *Dependencies) Close(ctx context.Context) error {
	var errs []error
	if d.NATS != nil {
		if err := d.NATS.Drain(); err != nil {
			errs = append(errs, fmt.Errorf("drain NATS: %w", err))
			d.NATS.Close()
		}
	}
	if d.Mongo != nil {
		if err := d.Mongo.Disconnect(ctx); err != nil {
			errs = append(errs, fmt.Errorf("disconnect mongo: %w", err))
		}
	}
	if d.Redis != nil {
		if err := d.Redis.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close redis: %w", err))
		}
	}
	if d.PostgresSQL != nil {
		if err := d.PostgresSQL.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close postgres: %w", err))
		}
	}
	return errors.Join(errs...)
}
