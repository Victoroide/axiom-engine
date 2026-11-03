package server

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/gofiber/fiber/v3/internal/memory.(*Storage).gc"),
		goleak.IgnoreTopFunction("github.com/gofiber/utils/v2.StartTimeStampUpdater.func1"),
		goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.updateServerDate.func1"),
		goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.(*workerPool).Start.func2"),
	)
}
