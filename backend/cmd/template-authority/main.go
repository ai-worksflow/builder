package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/templateoperator"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	postgresDSNEnvironment = "WORKSFLOW_TEMPLATE_AUTHORITY_POSTGRES_DSN"
	maximumInputBytes      = 32 << 20
)

type options struct {
	mode       string
	configPath string
	inputPath  string
	timeout    time.Duration
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", "template-artifact-authority")
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Error("operator command failed", "error", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("template-authority", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configuration := options{}
	flags.StringVar(&configuration.mode, "mode", "", "commitments, readiness, or admit")
	flags.StringVar(&configuration.configPath, "config", "", "absolute path to the authority config")
	flags.StringVar(&configuration.inputPath, "input", "-", "admission JSON file, or - for stdin")
	flags.DurationVar(&configuration.timeout, "timeout", 5*time.Minute, "whole operation timeout")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not accepted")
	}
	if configuration.mode != "commitments" && configuration.mode != "readiness" && configuration.mode != "admit" {
		return errors.New("-mode must be commitments, readiness, or admit")
	}
	if configuration.configPath == "" {
		return errors.New("-config is required")
	}
	if configuration.timeout < time.Second || configuration.timeout > 30*time.Minute {
		return errors.New("-timeout must be between one second and thirty minutes")
	}

	config, err := templateoperator.LoadConfig(configuration.configPath)
	if err != nil {
		return err
	}
	if configuration.mode == "commitments" {
		commitments, err := templateoperator.DeriveCommitments(config)
		if err != nil {
			return err
		}
		return writeCanonicalJSON(output, commitments)
	}

	dsn, present := os.LookupEnv(postgresDSNEnvironment)
	if !present || dsn == "" {
		return fmt.Errorf("%s is required", postgresDSNEnvironment)
	}
	database, closeDatabase, err := openDatabase(dsn)
	if err != nil {
		return err
	}
	defer closeDatabase()
	operator, err := templateoperator.New(database, config, os.LookupEnv)
	if err != nil {
		return err
	}

	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, configuration.timeout)
	defer cancel()
	if configuration.mode == "readiness" {
		if err := operator.Readiness(ctx); err != nil {
			return err
		}
		return writeCanonicalJSON(output, struct {
			SchemaVersion string                       `json:"schemaVersion"`
			Ready         bool                         `json:"ready"`
			Commitments   templateoperator.Commitments `json:"commitments"`
		}{SchemaVersion: "template-artifact-authority-readiness/v1", Ready: true, Commitments: operator.Commitments()})
	}

	encoded, err := readAdmissionInput(configuration.inputPath, os.Stdin)
	if err != nil {
		return err
	}
	request, err := templateoperator.DecodeAdmissionRequest(encoded)
	if err != nil {
		return err
	}
	registration, err := operator.Admit(ctx, request)
	if err != nil {
		return err
	}
	return writeCanonicalJSON(output, registration)
}

func openDatabase(dsn string) (*gorm.DB, func(), error) {
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open Template Artifact Authority PostgreSQL: %w", err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("open Template Artifact Authority connection pool: %w", err)
	}
	sqlDatabase.SetMaxOpenConns(4)
	sqlDatabase.SetMaxIdleConns(2)
	sqlDatabase.SetConnMaxLifetime(30 * time.Minute)
	sqlDatabase.SetConnMaxIdleTime(5 * time.Minute)
	return database, func() { _ = sqlDatabase.Close() }, nil
}

func readAdmissionInput(path string, standardInput io.Reader) ([]byte, error) {
	var reader io.Reader = standardInput
	var file *os.File
	if path != "-" {
		opened, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open admission input: %w", err)
		}
		file = opened
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("admission input must be a regular file")
		}
		reader = file
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, maximumInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read admission input: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > maximumInputBytes {
		return nil, fmt.Errorf("admission input must be between 1 and %d bytes", maximumInputBytes)
	}
	return encoded, nil
}

func writeCanonicalJSON(output io.Writer, value any) error {
	encoded, err := domain.CanonicalJSON(value)
	if err != nil {
		return fmt.Errorf("encode canonical operator result: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := output.Write(encoded); err != nil {
		return fmt.Errorf("write operator result: %w", err)
	}
	return nil
}
