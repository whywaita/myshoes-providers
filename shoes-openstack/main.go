package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/hashicorp/go-plugin"

	pb "github.com/whywaita/myshoes/api/proto"
	"github.com/whywaita/myshoes/pkg/runner"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Environment key values
const (
	EnvFlavorID  = "OS_FLAVOR_ID"
	EnvImageID   = "OS_IMAGE_ID"
	EnvNetworkID = "OS_NETWORK_ID"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	handshake := plugin.HandshakeConfig{
		ProtocolVersion:  1,
		MagicCookieKey:   "SHOES_PLUGIN_MAGIC_COOKIE",
		MagicCookieValue: "are_you_a_shoes?",
	}
	pluginMap := map[string]plugin.Plugin{
		"shoes_grpc": &OpenStackPlugin{},
	}

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         pluginMap,
		GRPCServer:      plugin.DefaultGRPCServer,
	})

	return nil
}

// OpenStackPlugin is plugin for OpenStack.
type OpenStackPlugin struct {
	plugin.Plugin
}

// GRPCServer is implement gRPC Server.
func (o *OpenStackPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	c, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	client, err := New(c)
	if err != nil {
		return fmt.Errorf("failed to create OpenStackClient: %w", err)
	}

	pb.RegisterShoesServer(s, client)

	return nil
}

// GRPCClient is implement gRPC client.
// This function is not have client, so return nil
func (o *OpenStackPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return nil, nil
}

// OpenStackClient is a client for OpenStack.
type OpenStackClient struct {
	computeClient *gophercloud.ServiceClient

	flavorID  string
	imageID   string
	networkID string
}

// New is create OpenStackClient.
func New(c config) (*OpenStackClient, error) {
	p := &OpenStackClient{
		flavorID:  c.flavorID,
		imageID:   c.imageID,
		networkID: c.networkID,
	}

	computeClient, err := openstackAuthenticate()
	if err != nil {
		return nil, fmt.Errorf("failed to auth openstack: %w", err)
	}
	p.computeClient = computeClient

	return p, nil
}

// AddInstance add a OpenStack instance.
func (p OpenStackClient) AddInstance(ctx context.Context, req *pb.AddInstanceRequest) (*pb.AddInstanceResponse, error) {
	if _, err := runner.ToUUID(req.RunnerName); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse request name: %+v", err)
	}

	createOpts := servers.CreateOpts{
		Name:      req.RunnerName,
		FlavorRef: p.flavorID,
		ImageRef:  p.imageID,
		Networks:  []servers.Network{{UUID: p.networkID}},
		UserData:  []byte(req.SetupScript),
	}

	server, err := servers.Create(p.computeClient, createOpts).Extract()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create server: %+v", err)
	}

	if err := servers.WaitForStatus(p.computeClient, server.ID, "ACTIVE", 3*60); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to wait running status: %+v", err)
	}

	return &pb.AddInstanceResponse{
		CloudId:   server.ID,
		ShoesType: "openstack",
		IpAddress: server.AccessIPv4,
	}, nil
}

// DeleteInstance delete a OpenStack instance.
func (p OpenStackClient) DeleteInstance(ctx context.Context, req *pb.DeleteInstanceRequest) (*pb.DeleteInstanceResponse, error) {
	err := servers.Delete(p.computeClient, req.CloudId).ExtractErr()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete server: %+v", err)
	}

	return &pb.DeleteInstanceResponse{}, nil
}

type config struct {
	listenAddress string

	flavorID  string
	imageID   string
	networkID string
}

func loadConfig() (config, error) {
	var c config

	var unsetValues []string
	for _, e := range []string{EnvFlavorID, EnvImageID, EnvNetworkID} {
		if os.Getenv(e) == "" {
			unsetValues = append(unsetValues, e)
		}
	}
	if len(unsetValues) != 0 {
		return config{}, fmt.Errorf("must be set %s", strings.Join(unsetValues, ", "))
	}

	c.flavorID = os.Getenv(EnvFlavorID)
	c.imageID = os.Getenv(EnvImageID)
	c.networkID = os.Getenv(EnvNetworkID)

	return c, nil
}

// openstackAuthenticate is auth
func openstackAuthenticate() (*gophercloud.ServiceClient, error) {
	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return nil, err
	}
	opts.DomainName = os.Getenv("OS_USER_DOMAIN_NAME")

	provider, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return nil, err
	}

	computeClient, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return nil, err
	}

	return computeClient, nil
}
