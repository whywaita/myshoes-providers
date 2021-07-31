package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/hashicorp/go-plugin"
	pb "github.com/whywaita/myshoes/api/proto"
	"github.com/whywaita/myshoes/pkg/datastore"
	"google.golang.org/grpc"
)

// Environment key values
const (
	EnvAWSImageID             = "AWS_IMAGE_ID"
	EnvAWSResourceTypeMapping = "AWS_RESOURCE_TYPE_MAPPING"

	DefaultImageID = "ami-02868af3c3df4b3aa" // us-west-2 focal 20.04 LTS amd64 hvm:ebs-ssd
)

func main() {
	handshake := plugin.HandshakeConfig{
		ProtocolVersion:  1,
		MagicCookieKey:   "SHOES_PLUGIN_MAGIC_COOKIE",
		MagicCookieValue: "are_you_a_shoes?",
	}
	pluginMap := map[string]plugin.Plugin{
		"shoes_grpc": &AWSPlugin{},
	}

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         pluginMap,
		GRPCServer:      plugin.DefaultGRPCServer,
	})
}

// AWSPlugin is plugin implement for AWS
type AWSPlugin struct {
	plugin.Plugin
}

// GRPCServer is server of gRPC
func (p *AWSPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	ctx := context.Background()

	server, err := newServer(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to initialize server: %w", err)
	}
	pb.RegisterShoesServer(s, *server)

	return nil
}

func newServer(ctx context.Context, endpoint string) (*AWS, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithEndpointResolver(mockEndpointResolver(endpoint)))
	if err != nil {
		return nil, fmt.Errorf("failed to load SDK config: %w", err)
	}
	service := ec2.NewFromConfig(cfg)

	imageID, mapping, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	server := &AWS{
		client:          service,
		imageID:         imageID,
		resourceMapping: mapping,
	}

	return server, nil
}

// GRPCClient is client of gRPC
func (p *AWSPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return nil, nil
}

// AWS is interface of Amazon Web Service
type AWS struct {
	pb.UnimplementedShoesServer

	client          *ec2.Client
	imageID         string
	resourceMapping map[pb.ResourceType]string
}

func (a AWS) generateInput(script string, rt pb.ResourceType) *ec2.RunInstancesInput {
	instanceCount := int32(1)

	return &ec2.RunInstancesInput{
		MaxCount:     &instanceCount,
		MinCount:     &instanceCount,
		ImageId:      aws.String(a.imageID),
		InstanceType: types.InstanceType(a.resourceMapping[rt]),
		UserData:     aws.String(script),
	}
}

// AddInstance create an instance from AWS
func (a AWS) AddInstance(ctx context.Context, req *pb.AddInstanceRequest) (*pb.AddInstanceResponse, error) {
	instanceID, ip, err := a.createRunnerInstance(ctx, req.RunnerName, req.SetupScript, req.ResourceType)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create a runner instance: %+v", err)
	}

	return &pb.AddInstanceResponse{
		CloudId:   instanceID,
		ShoesType: "aws",
		IpAddress: ip,
	}, nil
}

func (a AWS) createRunnerInstance(ctx context.Context, runnerName, script string, resourceType pb.ResourceType) (string, string, error) {
	input := a.generateInput(script, resourceType)

	result, err := a.client.RunInstances(ctx, input)
	if err != nil {
		return "", "", fmt.Errorf("failed to create instance: %w", err)
	}
	instanceID := *result.Instances[0].InstanceId
	ip := *result.Instances[0].PublicIpAddress

	if _, err := a.client.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags: []types.Tag{
			{
				Key:   aws.String("Name"),
				Value: aws.String(runnerName),
			},
		},
	}); err != nil {
		return "", "", fmt.Errorf("failed to attach tag: %w", err)
	}

	if _, err := a.client.StartInstances(ctx, &ec2.StartInstancesInput{
		InstanceIds: []string{instanceID},
	}); err != nil {
		return "", "", fmt.Errorf("failed to start instance (id: %s): %w", instanceID, err)
	}

	return instanceID, ip, nil
}

// DeleteInstance delete an instance from AWS
func (a AWS) DeleteInstance(ctx context.Context, req *pb.DeleteInstanceRequest) (*pb.DeleteInstanceResponse, error) {
	if err := a.deleteRunnerInstance(ctx, req.CloudId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete runner instance: %+v", err)
	}

	return &pb.DeleteInstanceResponse{}, nil
}

func (a AWS) deleteRunnerInstance(ctx context.Context, instanceID string) error {
	if _, err := a.client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	}); err != nil {
		return fmt.Errorf("failed to terminate instance (id: %s): %w", instanceID, err)
	}

	waiter := ec2.NewInstanceTerminatedWaiter(a.client)
	if err := waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, 5*time.Minute); err != nil {
		return fmt.Errorf("failed to wait terminating instance (id: %s): %w", instanceID, err)
	}

	return nil
}

func loadConfig() (string, map[pb.ResourceType]string, error) {
	var imageID string
	if os.Getenv(EnvAWSImageID) != "" {
		imageID = os.Getenv(EnvAWSImageID)
	} else {
		imageID = DefaultImageID
	}

	if os.Getenv(EnvAWSResourceTypeMapping) == "" {
		return "", nil, fmt.Errorf("must be set %s", EnvAWSResourceTypeMapping)
	}

	m, err := readResourceTypeMapping(os.Getenv(EnvAWSResourceTypeMapping))
	if err != nil {
		return "", nil, fmt.Errorf("failed to read %s: %w", EnvAWSResourceTypeMapping, err)
	}

	return imageID, m, nil
}

func readResourceTypeMapping(env string) (map[pb.ResourceType]string, error) {
	var mapping map[string]string
	if err := json.Unmarshal([]byte(env), &mapping); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	r := map[pb.ResourceType]string{}
	for key, value := range mapping {
		rt := datastore.UnmarshalResourceType(key)
		if rt == datastore.ResourceTypeUnknown {
			return nil, fmt.Errorf("%s is invalid resource type", key)
		}

		r[rt.ToPb()] = value
	}

	return r, nil
}

// mockEndpointResolver set endpoint to localhost localstack for testing.
func mockEndpointResolver(endpoint string) aws.EndpointResolverFunc {
	return aws.EndpointResolverFunc(func(service, region string) (aws.Endpoint, error) {
		if endpoint == "" {
			return aws.Endpoint{}, &aws.EndpointNotFoundError{}
		}

		if service == ec2.ServiceID && region == "shoes-aws-testing-region" {
			return aws.Endpoint{
				PartitionID:   "aws",
				URL:           endpoint,
				SigningRegion: "shoes-aws-testing-region",
			}, nil
		}

		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})
}
