package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	authServer "authd/internal/api/authServer"
	"authd/internal/config"
	"authd/internal/service"
	"authd/pkg/database/sqlite"
	"authd/pkg/version"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
)

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "2006-01-02T15:04:05.000Z"}
	log.Logger = log.Output(output).With().Logger()
}

func main() {
	flagConfigFile := flag.String("config", "", "Path to the configuration file (default: <exe-dir>/config/authd.toml)")
	flagSettingsFile := flag.String("settings", "", "Path to the mutable settings file (default: <exe-dir>/config/authd-settings.toml)")
	flagVersion := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()

	if *flagVersion {
		fmt.Println(version.Detailed())
		os.Exit(0)
	}

	log.Info().Msgf("Auth service version %s starting...", version.Info())

	cfg, err := config.Load(*flagConfigFile)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load auth config")
	}

	settings, err := config.LoadSettings(*flagSettingsFile, cfg.DefaultSetting)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load auth settings")
	}

	store, err := sqlite.Open(cfg.Storage.SQLitePath, cfg.Storage.BusyTimeout.Std())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open sqlite store")
	}
	defer store.Close()

	authService, err := service.New(cfg.Auth, settings, store)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize auth service")
	}

	if err := authService.EnsureBootstrapAdmin(); err != nil {
		log.Fatal().Err(err).Msg("failed to ensure bootstrap admin")
	}

	listener, err := net.Listen("tcp", cfg.Server.ListenAddress)
	if err != nil {
		log.Fatal().Err(err).Str("listen", cfg.Server.ListenAddress).Msg("failed to bind gRPC listener")
	}

	server := authServer.NewServer(authServer.NewHandler(authService))
	serveErrCh := make(chan error, 1)
	go func() {
		// Serve 在 GracefulStop 後會回傳 ErrServerStopped，視為正常結束。
		if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErrCh <- err
		}
		close(serveErrCh)
	}()

	log.Info().Str("listen", cfg.Server.ListenAddress).Str("sqlite_path", cfg.Storage.SQLitePath).Msg("auth service ready")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info().Stringer("signal", sig).Msg("received shutdown signal")
	case err := <-serveErrCh:
		if err != nil {
			log.Error().Err(err).Msg("gRPC server failed")
		}
	}

	server.GracefulStop()
	log.Info().Msg("auth service stopped")
}
