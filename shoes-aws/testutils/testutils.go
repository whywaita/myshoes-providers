package testutils

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ory/dockertest/v3"
)

var (
	testEndpoint = ""
)

// IntegrationTestRunner is all integration test
func IntegrationTestRunner(m *testing.M) int {
	// uses a sensible default on windows (tcp/http) and linux/osx (socket)
	pool, err := dockertest.NewPool("")
	if err != nil {
		log.Fatalf("Could not connect to docker: %s", err)
	}

	// pulls an image, creates a container based on it and runs it
	resource, err := pool.Run("localstack/localstack", "latest", []string{"SERVICES=ec2"})
	if err != nil {
		log.Fatalf("Could not start resource: %s", err)
	}

	// exponential backoff-retry, because the application in the container might not be ready to accept connections yet
	if err := pool.Retry(func() error {
		healthcheckURL := fmt.Sprintf("http://localhost:%s/health?reload", resource.GetPort("4566/tcp"))
		resp, pErr := http.Get(healthcheckURL)
		if pErr != nil {
			return fmt.Errorf("failed to GET healthcheck request: %w", pErr)
		}
		defer resp.Body.Close()
		b, pErr := io.ReadAll(resp.Body)
		if pErr != nil {
			return fmt.Errorf("failed to read all response body: %w", pErr)
		}

		type healthResp struct {
			Services map[string]string `json:"services"`
		}

		var health healthResp
		if pErr := json.Unmarshal(b, &health); err != nil {
			return fmt.Errorf("failed to unmarshal json: %w", pErr)
		}

		state, ok := health.Services["ec2"]
		if !ok {
			return fmt.Errorf("localstack has not ec2 service")
		}
		if !strings.EqualFold(state, "running") {
			return fmt.Errorf("ec2 service is not running: (%s)", state)
		}

		testEndpoint = fmt.Sprintf("http://localhost:%s", resource.GetPort("4566/tcp"))

		return nil
	}); err != nil {
		log.Fatalf("Could not connect to docker: %s", err)
	}

	testingEnv := map[string]string{
		"AWS_RESOURCE_TYPE_MAPPING": `{"nano": "c5a.large", "micro": "c5a.xlarge"}`,
		"AWS_REGION":                "shoes-aws-testing-region",
	}

	// rewrite to T.Setenv after Go 1.17
	prevEnv := map[string]string{}
	for k, v := range testingEnv {
		prev := os.Getenv(k)
		if err := os.Setenv(k, v); err != nil {
			log.Fatalf("Could not set environment value (key: %q): %+v", k, err)
		}
		prevEnv[k] = prev
	}

	code := m.Run()

	// TODO: reset

	for k, v := range prevEnv {
		if err := os.Setenv(k, v); err != nil {
			log.Fatalf("Could not set environment value (key: %q): %+v", k, err)
		}
	}

	// You can't defer this because os.Exit doesn't care for defer
	if err := pool.Purge(resource); err != nil {
		log.Fatalf("Could not purge resource: %s", err)
	}

	return code
}

// GetTestEndpoint get endpoint for test
func GetTestEndpoint() string {
	if testEndpoint == "" {
		panic("testEndpoint is blank, not initialized yet")
	}

	return testEndpoint
}
