package main

import (
	"context"
	"os"
	"testing"

	"github.com/whywaita/myshoes-providers/shoes-aws/testutils"
	pb "github.com/whywaita/myshoes/api/proto"
)

func TestMain(m *testing.M) {
	os.Exit(testutils.IntegrationTestRunner(m))
}

func Test_createRunnerInstance(t *testing.T) {
	ctx := context.Background()
	testEndpoint := testutils.GetTestEndpoint()

	a, err := newServer(ctx, testEndpoint)
	if err != nil {
		t.Fatalf("failed to newServer: %+v", err)
	}

	if _, _, err := a.createRunnerInstance(ctx, "test-runner", "echo 0", pb.ResourceType_Nano); err != nil {
		t.Fatalf("failed to createRunnerInstance: %+v", err)
	}
}

func Test_deleteRunnerInstance(t *testing.T) {
	ctx := context.Background()
	testEndpoint := testutils.GetTestEndpoint()

	a, err := newServer(ctx, testEndpoint)
	if err != nil {
		t.Fatalf("failed to newServer: %+v", err)
	}

	instanceID, _, err := a.createRunnerInstance(ctx, "test-runner", "echo 0", pb.ResourceType_Nano)
	if err != nil {
		t.Fatalf("failed to createRunnerInstance: %+v", err)
	}

	if err := a.deleteRunnerInstance(ctx, instanceID); err != nil {
		t.Fatalf("failed to deleteRunnerInstance: %+v", err)
	}
}
