package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func TestNativeBedrockRegion(t *testing.T) {
	metadata := isolateRegionFixtures(t)
	for _, tc := range []struct {
		name, strict, region, alternate, profile, want, wantNext string
		wantMetadata                                             int32
	}{
		{name: "strict pins the startup region", strict: "true", region: "eusc-de-east-1", alternate: "us-west-2", profile: "eu-west-2", want: "eusc-de-east-1", wantNext: "eusc-de-east-1"},
		{name: "default region precedence", region: "us-west-1", alternate: "us-west-2", profile: "eu-west-2", want: "us-west-1", wantNext: "eu-central-1"},
		{name: "default alternate fallback", alternate: "us-west-2", profile: "eu-west-2", want: "us-west-2", wantNext: "eu-central-1"},
		{name: "explicitly off profile fallback", strict: "false", profile: "eu-west-2", want: "eu-west-2", wantNext: "eu-central-1"},
		{name: "default IMDS fallback", want: "us-east-2", wantNext: "eu-central-1", wantMetadata: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newProviderFixture(t, "aws-bedrock", "ok")
			t.Setenv("AWS_EC2_METADATA_DISABLED", "false")
			for key, value := range map[string]string{"AWS_REGION": tc.region, "AWS_DEFAULT_REGION": tc.alternate} {
				t.Setenv(key, value)
				// The SDK can treat an empty variable as a configured region.
				if value == "" {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			profileFile := filepath.Join(t.TempDir(), "config")
			writeRegionProfile := func(region string) {
				t.Helper()
				if err := os.WriteFile(profileFile, []byte("[profile local]\nregion = "+region+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.profile != "" {
				writeRegionProfile(tc.profile)
				t.Setenv("AWS_CONFIG_FILE", profileFile)
				t.Setenv("AWS_PROFILE", "local")
			}
			metadata.region.Store("us-east-2")
			before := metadata.calls.Load()
			settings := loadRegionFixture(t, fixture.settings, tc.strict)
			if tc.strict == "true" {
				// A later environment value must not replace the one startup validated.
				t.Setenv("AWS_REGION", "ap-south-1")
			}
			planner := New(settings)
			if metadata.calls.Load() != before || fixture.count() != 0 {
				t.Fatal("startup contacted an upstream endpoint")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := planner.GenerateTopic(ctx, topicRequest()); err != nil {
				t.Fatal(err)
			}
			afterFirst := metadata.calls.Load()
			if got := afterFirst - before; got != tc.wantMetadata {
				t.Errorf("first IMDS requests = %d, want %d", got, tc.wantMetadata)
			}
			switch {
			case tc.region != "":
				t.Setenv("AWS_REGION", "eu-central-1")
			case tc.alternate != "":
				t.Setenv("AWS_DEFAULT_REGION", "eu-central-1")
			case tc.profile != "":
				writeRegionProfile("eu-central-1")
			default:
				metadata.region.Store("eu-central-1")
			}
			if _, err := planner.GenerateTopic(ctx, topicRequest()); err != nil {
				t.Fatal(err)
			}
			requests := fixture.capturedRequests()
			if len(requests) != 2 {
				t.Fatalf("Bedrock requests = %d, want 2", len(requests))
			}
			// Strict mode keeps the validated startup region. The default chain stays native,
			// so its later calls follow the changed source.
			for i, want := range []string{tc.want, tc.wantNext} {
				if !signedForRegion(requests[i], want) {
					t.Errorf("Bedrock request %d signing scope did not use region %q: %s", i+1, want, requests[i].headers)
				}
			}
			if got := metadata.calls.Load() - afterFirst; got != tc.wantMetadata {
				t.Errorf("second IMDS requests = %d, want %d", got, tc.wantMetadata)
			}
		})
	}
}

func signedForRegion(request providerRequest, region string) bool {
	return strings.Contains(request.headers, "Authorization: AWS4-HMAC-SHA256 Credential=dummy/") &&
		strings.Contains(request.headers, "/"+region+"/bedrock/aws4_request,")
}

// A selected profile's region stays in effect when its credential settings cannot load.
func TestNativeBedrockDeclaredProfileRegion(t *testing.T) {
	metadata := isolateRegionFixtures(t)
	const selected = "[profile local]\nregion = eu-west-2\n"
	for _, tc := range []struct{ name, profile string }{
		{"simple profile", selected},
		{"role profile with a missing source", selected + "role_arn = arn:aws:iam::123456789012:role/planner\nsource_profile = absent\n"},
	} {
		for _, imds := range []string{"false", "true"} {
			t.Run(tc.name+"/IMDS disabled="+imds, func(t *testing.T) {
				fixture := newProviderFixture(t, "aws-bedrock", "ok")
				for _, key := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
					t.Setenv(key, "")
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
				profileFile := filepath.Join(t.TempDir(), "config")
				if err := os.WriteFile(profileFile, []byte(tc.profile), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("AWS_CONFIG_FILE", profileFile)
				t.Setenv("AWS_PROFILE", "local")
				t.Setenv("AWS_EC2_METADATA_DISABLED", imds)
				before := metadata.calls.Load()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if _, err := New(loadRegionFixture(t, fixture.settings, "false")).GenerateTopic(ctx, topicRequest()); err != nil {
					t.Fatal(err)
				}
				requests := fixture.capturedRequests()
				if len(requests) != 1 || !signedForRegion(requests[0], "eu-west-2") {
					t.Fatalf("Bedrock requests did not sign once for eu-west-2: %+v", requests)
				}
				if got := metadata.calls.Load() - before; got != 0 {
					t.Errorf("IMDS requests = %d, want 0", got)
				}
			})
		}
	}
}

func TestNativeNonBedrockIgnoresRegion(t *testing.T) {
	metadata := isolateRegionFixtures(t)
	for _, provider := range nativeProviders {
		if provider == "aws-bedrock" {
			continue
		}
		for _, tc := range []struct{ name, region string }{{"no region sources", ""}, {"malformed explicit region", "invalid region"}} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				before := metadata.calls.Load()
				fixture := newProviderFixture(t, provider, "ok")
				if tc.region != "" {
					t.Setenv("AWS_REGION", tc.region)
				}
				planner := New(loadRegionFixture(t, fixture.settings, "true"))
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if _, err := planner.GenerateTopic(ctx, topicRequest()); err != nil {
					t.Fatal(err)
				}
				if got := fixture.count(); got != 1 {
					t.Errorf("provider requests = %d, want 1", got)
				}
				if got := metadata.calls.Load() - before; got != 0 {
					t.Errorf("IMDS requests = %d, want 0", got)
				}
			})
		}
	}
}

// A rejected IMDSv2 token stops the native lookup before it reaches Bedrock,
// and a later call recovers without a restart.
func TestNativeBedrockIMDSTokenRejection(t *testing.T) {
	metadata := isolateRegionFixtures(t)
	fixture := newProviderFixture(t, "aws-bedrock", "ok")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "false")
	for _, key := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	planner := New(loadRegionFixture(t, fixture.settings, "false"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	metadata.rejectToken.Store(true)
	if _, err := planner.GenerateTopic(ctx, topicRequest()); err == nil {
		t.Fatal("rejected IMDSv2 token returned a plan")
	}
	if metadata.calls.Load() == 0 || metadata.regionGets.Load() != 0 || fixture.count() != 0 {
		t.Fatalf("rejected IMDSv2 token reached region or Bedrock: requests=%d, region GETs=%d, Bedrock=%d", metadata.calls.Load(), metadata.regionGets.Load(), fixture.count())
	}
	metadata.rejectToken.Store(false)
	if _, err := planner.GenerateTopic(ctx, topicRequest()); err != nil {
		t.Fatalf("call after IMDS recovery: %v", err)
	}
	if requests := fixture.capturedRequests(); len(requests) != 1 || !signedForRegion(requests[0], "us-east-2") {
		t.Fatalf("Bedrock requests after recovery: %+v", requests)
	}
}

type regionMetadataFixture struct {
	calls       atomic.Int32
	regionGets  atomic.Int32
	region      atomic.Value
	rejectToken atomic.Bool
}

func isolateRegionFixtures(t *testing.T) *regionMetadataFixture {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "AWS_") {
			t.Setenv(key, value)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	settingsFile := filepath.Join(t.TempDir(), "aws-settings")
	if err := os.WriteFile(settingsFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", settingsFile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", settingsFile)
	fixture := &regionMetadataFixture{}
	fixture.region.Store("us-east-2")
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		switch r.Method + " " + r.URL.Path {
		case "PUT /latest/api/token":
			if fixture.rejectToken.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", "60")
			_, _ = io.WriteString(w, "local-imds-token")
		case "GET /latest/meta-data/placement/region":
			fixture.regionGets.Add(1)
			if r.Header.Get("X-Aws-Ec2-Metadata-Token") != "local-imds-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, fixture.region.Load().(string))
		default:
			t.Errorf("unexpected IMDS request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(metadata.Close)
	t.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT", metadata.URL)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "false")
	return fixture
}

func loadRegionFixture(t *testing.T, settings config.LLM, strict string) config.LLM {
	t.Helper()
	env := map[string]string{
		"AXIOM_AUTH_DISABLED": "true", "BEDROCK_REQUIRE_EXPLICIT_REGION": strict,
		"AWS_REGION": os.Getenv("AWS_REGION"), "LLM_PROVIDER": settings.Provider, "LLM_MODEL": settings.Model,
		"LLM_BASE_URL": settings.BaseURL, "LLM_API_KEY": settings.APIKey, "OPENAI_API_KEY": settings.APIKey,
		"ANTHROPIC_API_KEY": settings.APIKey, "GOOGLE_API_KEY": settings.APIKey,
	}
	// Only providers that use LLM_BASE_URL accept it through application configuration.
	if settings.Provider == "anthropic" || settings.Provider == "google-ai" {
		delete(env, "LLM_BASE_URL")
	}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLM.BaseURL = settings.BaseURL
	return cfg.LLM
}
