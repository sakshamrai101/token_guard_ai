package integration

import (
	"io"
	"log/slog"
	"os"
	"testing"

	redislogging "github.com/redis/go-redis/v9/logging"
)

func TestMain(m *testing.M) {
	// High-volume integration tests (1000 aborted streams, fail-open with dead Redis)
	// should not flood stderr during `go test -v`.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	redislogging.Disable()
	os.Exit(m.Run())
}
