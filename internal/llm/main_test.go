package llm

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// The BAML runtime loads its native library during package initialization,
	// before TestMain runs. Without a verified local library it may download one,
	// and the download client keeps an idle HTTP/2 connection open for 90 seconds,
	// which the leak check would report as a failure of this package.
	if os.Getenv("BAML_LIBRARY_PATH") == "" || os.Getenv("BAML_LIBRARY_DISABLE_DOWNLOAD") != "true" {
		fmt.Fprintln(os.Stderr, "internal/llm tests need the verified native library: run make test, or set BAML_LIBRARY_PATH to the library from make native-baml and BAML_LIBRARY_DISABLE_DOWNLOAD=true")
		os.Exit(1)
	}
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/gofiber/fiber/v3/internal/memory.(*Storage).gc"),
		goleak.IgnoreTopFunction("github.com/gofiber/utils/v2.StartTimeStampUpdater.func1"),
		goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.updateServerDate.func1"),
		goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.(*workerPool).Start.func2"),
	)
}
