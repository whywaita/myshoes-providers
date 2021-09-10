package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-plugin"
	lxd "github.com/lxc/lxd/client"
	"github.com/lxc/lxd/shared/api"

	pb "github.com/whywaita/myshoes/api/proto"
	"github.com/whywaita/myshoes/pkg/datastore"
	"github.com/whywaita/myshoes/pkg/runner"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Environment key values
const (
	// worker definition

	// for single node
	EnvLXDHost       = "LXD_HOST"
	EnvLXDClientCert = "LXD_CLIENT_CERT"
	EnvLXDClientKey  = "LXD_CLIENT_KEY"

	// for multi nodes
	EnvLXDHosts = "LXD_HOSTS"

	// optional variables
	EnvLXDImageAlias          = "LXD_IMAGE_ALIAS"
	EnvLXDResourceTypeMapping = "LXD_RESOURCE_TYPE_MAPPING"
)

func main() {
	rand.Seed(time.Now().UnixNano())

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
	hostsConfig, mapping, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	client, err := New(hostsConfig, mapping)
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
	hosts []LXDHost

	resourceMapping map[pb.ResourceType]Mapping
}

// scheduleHost extract host in workers
func (c *LXDClient) scheduleHost() LXDHost {
	if len(c.hosts) == 1 {
		return c.hosts[0]
	}

	index := rand.Intn(len(c.hosts)) // scheduling algorithm
	return c.hosts[index]
}

// LXDHost is define of host
type LXDHost struct {
	client lxd.InstanceServer

	config hostConfig
}

type hostConfig struct {
	cert tls.Certificate

	lxdHost       string
	lxdClientCert string
	lxdClientKey  string
}

// Mapping is resource mapping
type Mapping struct {
	ResourceTypeName string `json:"resource_type_name"`
	CPUCore          int    `json:"cpu"`
	Memory           string `json:"memory"`
}

// parseAlias parse user input
func parseAlias(input string) (api.InstanceSource, error) {
	if strings.EqualFold(input, "") {
		// default value is ubuntu:bionic
		return api.InstanceSource{
			Type: "image",
			Properties: map[string]string{
				"os":      "ubuntu",
				"release": "bionic",
			},
		}, nil
	}

	if strings.HasPrefix(input, "http") {
		// https://<FQDN or IP>:8443/<alias>
		u, err := url.Parse(input)
		if err != nil {
			return api.InstanceSource{}, fmt.Errorf("failed to parse alias: %w", err)
		}

		urlImageServer := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
		alias := strings.TrimPrefix(u.Path, "/")

		fmt.Println(urlImageServer)
		fmt.Println(alias)

		return api.InstanceSource{
			Type:   "image",
			Mode:   "pull",
			Server: urlImageServer,
			Alias:  alias,
		}, nil
	}

	return api.InstanceSource{
		Type:  "image",
		Alias: input,
	}, nil
}

// isExistInstance search created instance in same name
func (l LXDClient) isExistInstance(instanceName string) (lxd.InstanceServer, bool) {
	for _, host := range l.hosts {
		_, _, err := host.client.GetInstance(instanceName)
		if err == nil {
			// found LXD worker
			return host.client, true
		}
	}

	return nil, false
}

// AddInstance add a lxd instance.
func (l LXDClient) AddInstance(ctx context.Context, req *pb.AddInstanceRequest) (*pb.AddInstanceResponse, error) {
	if _, err := runner.ToUUID(req.RunnerName); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse request name: %+v", err)
	}
	instanceName := req.RunnerName

	rawLXCConfig := `lxc.apparmor.profile = unconfined
lxc.cgroup.devices.allow = a
lxc.cap.drop=`

	instanceConfig := map[string]string{
		"security.nesting":    "true",
		"security.privileged": "true",
		"raw.lxc":             rawLXCConfig,
		"user.user-data":      req.SetupScript,
	}

	if mapping, ok := l.resourceMapping[req.ResourceType]; ok {
		instanceConfig["limits.cpu"] = strconv.Itoa(mapping.CPUCore)
		instanceConfig["limits.memory"] = mapping.Memory
	}

	client, found := l.isExistInstance(instanceName)
	if !found {
		client = l.scheduleHost().client

		source, err := parseAlias(os.Getenv(EnvLXDImageAlias))
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "failed to parse %s: %+v", EnvLXDImageAlias, err)
		}

		reqInstance := api.InstancesPost{
			InstancePut: api.InstancePut{
				Config: instanceConfig,
			},
			Name:   instanceName,
			Source: source,
		}

		op, err := client.CreateInstance(reqInstance)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create instance: %+v", err)
		}
		if err := op.Wait(); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to wait creating instance: %+v", err)
		}
	}

	reqState := api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}
	op, err := client.UpdateInstanceState(instanceName, reqState, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to start instance: %+v", err)
	}
	if err := op.Wait(); err != nil && !strings.EqualFold(err.Error(), "The instance is already running") {
		return nil, status.Errorf(codes.Internal, "failed to wait starting instance: %+v", err)
	}

	i, _, err := client.GetInstance(instanceName)
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

	client, found := l.isExistInstance(instanceName)
	if !found {
		return nil, status.Errorf(codes.InvalidArgument, "failed to found worker that has %s", instanceName)
	}

	reqState := api.InstanceStatePut{
		Action:  "stop",
		Timeout: -1,
	}
	op, err := client.UpdateInstanceState(instanceName, reqState, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to stop instance: %+v", err)
	}
	if err := op.Wait(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to wait stopping instance: %+v", err)
	}

	op, err = client.DeleteInstance(instanceName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete instance: %+v", err)
	}
	if err := op.Wait(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to wait deleting instance: %+v", err)
	}

	return &pb.DeleteInstanceResponse{}, nil
}

// New create LXDClient
func New(hc []hostConfig, m map[pb.ResourceType]Mapping) (*LXDClient, error) {
	var hosts []LXDHost

	for _, h := range hc {
		args := &lxd.ConnectionArgs{
			UserAgent:          "shoes-lxd",
			TLSClientCert:      h.lxdClientCert,
			TLSClientKey:       h.lxdClientKey,
			InsecureSkipVerify: true,
		}

		conn, err := lxd.ConnectLXD(h.lxdHost, args)
		if err != nil {
			return nil, err
		}

		hosts = append(hosts, LXDHost{
			client: conn,
			config: h,
		})
	}

	return &LXDClient{
		hosts:           hosts,
		resourceMapping: m,
	}, nil
}

func loadConfig() ([]hostConfig, map[pb.ResourceType]Mapping, error) {
	hosts, err := loadHostsConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load LXD host config: %w", err)
	}

	if len(hosts) <= 0 {
		return nil, nil, fmt.Errorf("must set LXD host config")
	}

	envMappingJSON := os.Getenv(EnvLXDResourceTypeMapping)
	var m map[pb.ResourceType]Mapping
	if envMappingJSON != "" {
		m, err = readResourceTypeMapping(envMappingJSON)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read %s: %w", EnvLXDResourceTypeMapping, err)
		}
	}

	return hosts, m, nil
}

func loadHostsConfig() ([]hostConfig, error) {
	if strings.EqualFold(os.Getenv(EnvLXDHosts), "") {
		return loadSingleHostConfig()
	}

	return loadMultiHostsConfig()
}

func newHostConfig(ip, pathCert, pathKey string) (*hostConfig, error) {
	var host hostConfig

	host.lxdHost = ip

	lxdClientCert, err := ioutil.ReadFile(pathCert)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", pathCert, err)
	}
	lxdClientKey, err := ioutil.ReadFile(pathKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", pathKey, err)
	}

	host.lxdClientCert = string(lxdClientCert)
	host.lxdClientKey = string(lxdClientKey)

	return &host, nil
}

func loadSingleHostConfig() ([]hostConfig, error) {
	var unsetValues []string
	for _, e := range []string{EnvLXDHost, EnvLXDClientCert, EnvLXDClientKey} {
		if os.Getenv(e) == "" {
			unsetValues = append(unsetValues, e)
		}
	}
	if len(unsetValues) != 0 {
		return nil, fmt.Errorf("must be set %s", strings.Join(unsetValues, ", "))
	}

	ip := os.Getenv(EnvLXDHost)
	pathCert := os.Getenv(EnvLXDClientCert)
	pathKey := os.Getenv(EnvLXDClientKey)

	host, err := newHostConfig(ip, pathCert, pathKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create hostConfig: %w", err)
	}

	return []hostConfig{*host}, nil
}

type multiNode struct {
	IPAddress  string `json:"host"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
}

func loadMultiHostsConfig() ([]hostConfig, error) {
	multiNodeJSON := os.Getenv(EnvLXDHosts)
	var mn []multiNode

	if err := json.Unmarshal([]byte(multiNodeJSON), &mn); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", EnvLXDHosts, err)
	}

	var hostConfigs []hostConfig
	for _, node := range mn {
		host, err := newHostConfig(node.IPAddress, node.ClientCert, node.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create hostConfig: %w", err)
		}

		hostConfigs = append(hostConfigs, *host)
	}

	return hostConfigs, nil
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
