package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/whywaita/myshoes/pkg/datastore"

	"github.com/hashicorp/go-plugin"
	lxd "github.com/lxc/lxd/client"
	"github.com/lxc/lxd/shared/api"

	pb "github.com/whywaita/myshoes/api/proto"
	"github.com/whywaita/myshoes/pkg/runner"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Environment key values
const (
	// required variables
	EnvLXDHost       = "LXD_HOST"
	EnvLXDClientCert = "LXD_CLIENT_CERT"
	EnvLXDClientKey  = "LXD_CLIENT_KEY"

	// optional variables
	EnvLXDImageAlias          = "LXD_IMAGE_ALIAS"
	EnvLXDResourceTypeMapping = "LXC_RESOURCE_TYPE_MAPPING"
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
		"shoes_grpc": &LXDPlugin{},
	}

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         pluginMap,
		GRPCServer:      plugin.DefaultGRPCServer,
	})

	return nil
}

// LXDPlugin is plugin for lxd
type LXDPlugin struct {
	plugin.Plugin
}

// GRPCServer is implement gRPC Server.
func (l *LXDPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	c, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	client, err := New(c)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	pb.RegisterShoesServer(s, client)
	return nil
}

// GRPCClient is implement gRPC client.
// This function is not have client, so return nil
func (l *LXDPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return nil, nil
}

// LXDClient is a client for lxd.
type LXDClient struct {
	client lxd.InstanceServer

	config config
}

// AddInstance add a lxd instance.
func (l LXDClient) AddInstance(ctx context.Context, req *pb.AddInstanceRequest) (*pb.AddInstanceResponse, error) {
	if _, err := runner.ToUUID(req.RunnerName); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse request name: %+v", err)
	}

	rawLXCConfig := `lxc.apparmor.profile = unconfined
lxc.cgroup.devices.allow = a
lxc.cap.drop=`

	instanceConfig := map[string]string{
		"security.nesting":    "true",
		"security.privileged": "true",
		"raw.lxc":             rawLXCConfig,
		"user.user-data":      req.SetupScript,
	}

	if mapping, ok := l.config.resourceMapping[req.ResourceType]; ok {
		instanceConfig["limits.cpu"] = strconv.Itoa(mapping.CPUCore)
		instanceConfig["limits.memory"] = mapping.Memory
	}

	var is api.InstanceSource
	if strings.EqualFold(os.Getenv(EnvLXDImageAlias), "") {
		is = api.InstanceSource{
			Properties: map[string]string{
				"os":      "ubuntu",
				"release": "bionic",
			},
		}
	} else {
		is = api.InstanceSource{
			Type:  "image",
			Alias: os.Getenv(EnvLXDImageAlias),
		}
	}

	reqInstance := api.InstancesPost{
		InstancePut: api.InstancePut{
			Config: instanceConfig,
		},
		Name:   req.RunnerName,
		Source: is,
	}
	op, err := l.client.CreateInstance(reqInstance)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create instance: %+v", err)
	}
	if err := op.Wait(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to wait creating instance: %+v", err)
	}

	reqState := api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}
	op, err = l.client.UpdateInstanceState(req.RunnerName, reqState, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to start instance: %+v", err)
	}
	if err := op.Wait(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to wait starting instance: %+v", err)
	}

	i, _, err := l.client.GetInstance(req.RunnerName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to retrieve instance information: %+v", err)
	}

	return &pb.AddInstanceResponse{
		CloudId:   i.Name,
		ShoesType: "lxd",
		IpAddress: "",
	}, nil
}

// DeleteInstance delete a lxd instance.
func (l LXDClient) DeleteInstance(ctx context.Context, req *pb.DeleteInstanceRequest) (*pb.DeleteInstanceResponse, error) {
	if _, err := runner.ToUUID(req.CloudId); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse request id: %+v", err)
	}
	instanceName := req.CloudId

	reqState := api.InstanceStatePut{
		Action:  "stop",
		Timeout: -1,
	}
	op, err := l.client.UpdateInstanceState(instanceName, reqState, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to stop instance: %+v", err)
	}
	if err := op.Wait(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to wait stopping instance: %+v", err)
	}

	op, err = l.client.DeleteInstance(instanceName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete instance: %+v", err)
	}
	if err := op.Wait(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to wait deleting instance: %+v", err)
	}

	return &pb.DeleteInstanceResponse{}, nil
}

// New is create LXDClient
func New(c config) (*LXDClient, error) {
	args := &lxd.ConnectionArgs{
		UserAgent:          "shoes-lxd",
		TLSClientCert:      c.lxdClientCert,
		TLSClientKey:       c.lxdClientKey,
		InsecureSkipVerify: true,
	}

	conn, err := lxd.ConnectLXD(c.lxdHost, args)
	if err != nil {
		return nil, err
	}

	return &LXDClient{
		client: conn,
		config: c,
	}, nil
}

type config struct {
	cert tls.Certificate

	lxdHost       string
	lxdClientCert string
	lxdClientKey  string

	resourceMapping map[pb.ResourceType]Mapping
}

type Mapping struct {
	ResourceTypeName string `json:"resource_type_name"`
	CPUCore          int    `json:"cpu"`
	Memory           string `json:"memory"`
}

func loadConfig() (config, error) {
	var c config

	var unsetValues []string
	for _, e := range []string{EnvLXDHost, EnvLXDClientCert, EnvLXDClientKey} {
		if os.Getenv(e) == "" {
			unsetValues = append(unsetValues, e)
		}
	}
	if len(unsetValues) != 0 {
		return config{}, fmt.Errorf("must be set %s", strings.Join(unsetValues, ", "))
	}

	c.lxdHost = os.Getenv(EnvLXDHost)

	lxdClientCert, err := ioutil.ReadFile(os.Getenv(EnvLXDClientCert))
	if err != nil {
		return config{}, fmt.Errorf("failed to read %s: %w", EnvLXDClientCert, err)
	}
	lxdClientKey, err := ioutil.ReadFile(os.Getenv(EnvLXDClientKey))
	if err != nil {
		return config{}, fmt.Errorf("failed to read %s: %w", EnvLXDClientKey, err)
	}

	c.lxdClientCert = string(lxdClientCert)
	c.lxdClientKey = string(lxdClientKey)

	envMappingJSON := os.Getenv(EnvLXDResourceTypeMapping)
	if envMappingJSON != "" {
		m, err := readResourceTypeMapping(envMappingJSON)
		if err != nil {
			return config{}, fmt.Errorf("failed to read %s: %w", EnvLXDResourceTypeMapping, err)
		}
		c.resourceMapping = m
	}

	return c, nil
}

func readResourceTypeMapping(env string) (map[pb.ResourceType]Mapping, error) {
	var mapping []Mapping
	if err := json.Unmarshal([]byte(env), &mapping); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	r := map[pb.ResourceType]Mapping{}
	for _, m := range mapping {
		rt := datastore.UnmarshalResourceType(m.ResourceTypeName)
		if rt == datastore.ResourceTypeUnknown {
			return nil, fmt.Errorf("%s is invalid resource type", m.ResourceTypeName)
		}

		r[rt.ToPb()] = m
	}

	return r, nil
}
