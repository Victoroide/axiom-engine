//go:build e2e

package e2e_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type planner struct{}

func (planner) GenerateTopic(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	return []domain.PlanItem{{Topic: r.Topic, Explanation: "Practice " + r.Topic, PrerequisiteChain: r.PrerequisiteOrder, Resources: []domain.Resource{{Title: "Practice", URL: "https://example.com/practice", ResourceType: "diagram"}}}}, nil
}

func TestSwaggerTryItOut(t *testing.T) {
	cfg, err := config.Load(func(k string) string {
		if k == "AXIOM_INTERNAL_TOKEN" {
			return "e2e-internal-token-0123456789abcdef"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Planner: planner{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	defer func() {
		if err := app.ShutdownWithTimeout(3 * time.Second); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	l := launcher.New().Headless(true).NoSandbox(true).Set("disable-dev-shm-usage")
	if bin := os.Getenv("ROD_BROWSER_BIN"); bin != "" {
		l.Bin(bin)
	} else if bin, ok := launcher.LookPath(); ok {
		l.Bin(bin)
	}
	controlURL, err := l.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	browser := rod.New().ControlURL(controlURL).MustConnect()
	defer browser.MustClose()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page := browser.Context(ctx).MustPage("http://" + ln.Addr().String() + "/swagger/index.html").MustWaitLoad()
	defer func() {
		if failure := recover(); failure != nil {
			text, readErr := page.Context(context.Background()).Timeout(3 * time.Second).Element("body")
			if readErr == nil {
				body, readErr := text.Text()
				if readErr == nil {
					t.Log(body)
				}
			}
			t.Errorf("browser interaction failed: %v", failure)
		}
	}()

	title := page.MustElement(".swagger-ui .info .title").MustText()
	if !strings.Contains(title, "AxiomEngine API") {
		t.Fatal(title)
	}
	paths := []string{"/health", "/api/v1/adaptive-plan", "/api/v1/tutor/student/{student_id}/cognitive-graph"}
	for _, path := range paths {
		page.MustElement(`.opblock .opblock-summary-path[data-path="` + path + `"]`).MustWaitVisible()
	}
	if got := len(page.MustElements(".opblock")); got != len(paths) {
		t.Fatalf("got %d operations", got)
	}
	operation := func(path string) *rod.Element {
		return page.MustElement(`.opblock:has(.opblock-summary-path[data-path="` + path + `"])`)
	}
	expand := func(op *rod.Element) {
		op.MustElement(".opblock-summary-control").MustClick()
		op.MustWait(`() => this.querySelector(".try-out__btn") !== null`)
		op.MustElement(".try-out__btn").MustClick()
	}
	execute := func(op *rod.Element, status string, contains ...string) {
		op.MustWait(`() => this.querySelector("button.execute") !== null`)
		op.MustElement("button.execute").MustClick()
		op.MustWait(`() => this.querySelector(".live-responses-table tbody .response-col_status") !== null`)
		op.MustWait(`(status) => this.querySelector(".live-responses-table tbody .response-col_status")?.textContent.trim().split(/\s/)[0] === status`, status)
		text := op.MustElement(".live-responses-table tbody .response-col_description").MustText()
		for _, value := range contains {
			if !strings.Contains(text, value) {
				t.Fatalf("response does not contain %q: %s", value, text)
			}
		}
	}
	t.Log("health Try it out")
	health := operation("/health")
	expand(health)
	execute(health, "200", `"status": "ok"`)
	t.Log("plan unauthorized Try it out")
	plan := operation("/api/v1/adaptive-plan")
	expand(plan)
	plan.MustElement("textarea.body-param__text").MustSelectAllText().MustInput(`{"student_id":"student-42","course_id":"cs101","failed_topics":["Recursion","Polymorphism"],"vark_profile":"visual"}`)
	execute(plan, "401", "unauthorized")
	t.Log("authorize")
	page.MustElementR("button.authorize", "Authorize").MustClick()
	dialog := page.MustElement(".dialog-ux").MustWaitVisible()
	dialog.MustElement("input").MustInput("e2e-internal-token-0123456789abcdef")
	dialog.MustElementR("button.authorize", "Authorize").MustClick()
	dialog.MustElementR("button", "^Close$").MustClick()
	t.Log("plan authorized Try it out")
	execute(plan, "200", "Recursion", "Polymorphism")
	t.Log("cognitive Try it out")
	cognitive := operation("/api/v1/tutor/student/{student_id}/cognitive-graph")
	expand(cognitive)
	cognitive.MustElement(`tr[data-param-name="student_id"] input`).MustSelectAllText().MustInput("student-42")
	cognitive.MustElement(`tr[data-param-name="topics"] input`).MustSelectAllText().MustInput("Recursion,Polymorphism")
	cognitive.MustElement(`tr[data-param-name="target_topic"] input`).MustSelectAllText().MustInput("Recursion")
	execute(cognitive, "200", `"nodes"`, `"Recursion"`, `"edges"`)
}
