package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	// Side-effect import: registers the project models in the REPL namespace.
	_ "github.com/daqing/a1s/app/models"

	"github.com/daqing/airway/cmd"
	"github.com/daqing/airway/lib/plugin"
	"github.com/daqing/airway/lib/redis_client"
	"github.com/daqing/airway/lib/repo"
	"github.com/daqing/airway/lib/storage"
	"github.com/daqing/airway/lib/utils"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

// The project binary dispatches on its first argument. The A1s process
// subcommands (`api`, `scheduler`, `monitor`, `worker`) start system
// processes; any other argument goes to the Airway CLI compiled into this
// binary, so project-local code (REPL models, plugins, migrations) is
// visible to commands like `go run . repl`.
func main() {
	args := os.Args[1:]

	// `--version` / `-v` print the VERSION file contents directly, without
	// loading .env or any other project setup.
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v") {
		printVersion()
		return
	}

	if len(args) == 0 {
		printUsage(os.Stderr)
		os.Exit(2)
	}

	switch args[0] {
	case "server", "api":
		runServer()
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		fmt.Fprintln(os.Stdout)
		cmd.Version = versionString()
		cmd.Run([]string{"help"})
	case "scheduler", "monitor", "worker":
		notImplemented(args[0])
	default:
		cmd.Version = versionString()
		loadCLIEnv()
		cmd.Run(withREPLDSN(args))
	}
}

// withREPLDSN passes the configured DSN to the Airway REPL, which — unlike
// the other CLI commands — does not read the DSN env vars itself.
func withREPLDSN(args []string) []string {
	if len(args) == 0 || args[0] != "repl" {
		return args
	}

	for _, arg := range args[1:] {
		if arg == "--dsn" || strings.HasPrefix(arg, "--dsn=") {
			return args
		}
	}

	if dsn := utils.GetEnvMulti("A1S_DSN", "AIRWAY_DSN", "DSN"); dsn != "" {
		return append(args, "--dsn", dsn)
	}

	return args
}

// notImplemented exits with a code distinct from the server boot failure
// codes (1, 3–6), so scripts can tell "process not built yet" from a
// failed boot.
func notImplemented(name string) {
	fmt.Fprintf(os.Stderr, "a1s %s is not implemented yet\n", name)
	os.Exit(70)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: a1s <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "process commands:")
	fmt.Fprintln(w, "  api        start the HTTP control plane API (alias: server)")
	fmt.Fprintln(w, "  scheduler  start the scheduling loop (not implemented yet)")
	fmt.Fprintln(w, "  monitor    start the health monitor (not implemented yet)")
	fmt.Fprintln(w, "  worker     start the worker agent (not implemented yet)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "any other command is dispatched to the Airway CLI (repl, db:migrate, generate, ...); the command list follows")
}

func runServer() {
	// .env supplies fallbacks for anything the process environment does not
	// set (godotenv.Load never overrides existing values), so
	// `PORT=1988 airway server` wins over a PORT in .env. Load it before any
	// env checks so AIRWAY_ENV itself can come from .env.
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Loading env file: .env failed: %v", err)
	}

	appConfig := utils.AppConfig()

	if appConfig.Env == "" {
		log.Println("AIRWAY_ENV is not set")
		os.Exit(1)
	}

	if !appConfig.IsLocal {
		gin.SetMode(gin.ReleaseMode)
	}

	// Process vars use the A1S_ prefix (see .env.example); the AIRWAY_/
	// legacy names stay accepted so the Airway framework and existing
	// deployments keep working.
	dsn := utils.GetEnvMulti("A1S_DSN", "AIRWAY_DSN", "DSN")

	if len(dsn) > 0 {
		if _, setupErr := repo.SetupDB(dsn); setupErr != nil {
			log.Printf("database setup failed: %v", setupErr)
			os.Exit(3)
		}
	}

	redisURL := utils.GetEnvMulti("A1S_REDIS", "AIRWAY_REDIS", "REDIS")
	if len(redisURL) > 0 {
		redis_client.Setup(redisURL)
	}

	if _, err := storage.Setup(storage.FromEnv()); err != nil {
		log.Printf("storage setup failed: %v", err)
		os.Exit(4)
	}

	if err := plugin.BootAll(); err != nil {
		log.Printf("plugin boot failed: %v", err)
		os.Exit(5)
	}

	runApp()
}

// loadCLIEnv loads .env for CLI commands and bridges the canonical A1S_
// vars onto the legacy names the Airway CLI reads (DSN, REDIS).
func loadCLIEnv() {
	err := godotenv.Load(".env")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Loading env file: .env failed: %v", err)
	}

	bridgeEnv("A1S_DSN", "DSN")
	bridgeEnv("A1S_REDIS", "REDIS")
}

// bridgeEnv exposes the canonical A1S_ var under the legacy name the
// Airway CLI expects, unless the legacy name is already set.
func bridgeEnv(canon, legacy string) {
	if os.Getenv(legacy) == "" {
		if v := os.Getenv(canon); v != "" {
			os.Setenv(legacy, v)
		}
	}
}

func runApp() {
	app := NewApp("Airway", utils.GetEnvOr("AIRWAY_PORT", "PORT"))
	app.Run()
}
