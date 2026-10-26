package docker

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Fake is an in-memory Client for app-layer tests. Records calls and
// transitions container state per CreateAndStart/Start/Stop/Remove.
//
// Error-injection fields (DaemonErr, PullErr, CreateAndStartErr) are
// intended to be set during test setup, not mutated concurrently with the
// SUT.
type Fake struct {
	mu sync.Mutex

	DaemonErr         error
	PullErr           error
	CreateAndStartErr error

	Volumes       map[string]bool
	PulledImg     map[string]bool
	Containers    map[string]ContainerState
	ExecResponses map[string]ExecResponse
	Spec          ContainerSpec
	Removed       []string

	Networks           map[string]bool
	NetworkConnections map[string]bool // key: "<network>:<container>"

	EnsureNetworkErr  error
	ConnectNetworkErr error

	ImageIDs            map[string]string // ref -> image id
	ContainerImageIDs   map[string]string // container -> image id
	ImageIDErr          error
	ContainerImageIDErr error

	// Engine is what EngineInfo returns (Docker Desktop by default, so
	// tests exercise the macOS path unless they opt into another engine);
	// EngineInfoErr overrides it.
	Engine        EngineInfo
	EngineInfoErr error
	// Gateway is what BridgeGateway returns; GatewayErr overrides it.
	Gateway    string
	GatewayErr error

	// Specs records every spec CreateAndStart ran, by container name.
	// Spec (above) is only the most recent one.
	Specs map[string]ContainerSpec
	// Infos is what InspectContainer reports per container. CreateAndStart
	// fills it from the spec and ConnectNetwork appends to Networks; tests
	// seed it directly to model a container created by an older release.
	Infos      map[string]ContainerInfo
	InspectErr error

	// CreateErrs fails CreateAndStart for one container name only (e.g. the
	// relay), unlike CreateAndStartErr which fails every create.
	CreateErrs map[string]error

	// ExecHang lists Exec keys (same form as ExecResponses) that block
	// until ctx is done and return ctx.Err(), modelling a command that
	// hangs (e.g. a DNS lookup stuck in a forwarding loop).
	ExecHang map[string]bool
}

// DesktopEngine is the `docker info` subset Docker Desktop on macOS reports.
var DesktopEngine = EngineInfo{
	OperatingSystem: "Docker Desktop",
	OSType:          "linux",
	Name:            "docker-desktop",
	Labels:          []string{"com.docker.desktop.address=unix:///Users/x/Library/Containers/com.docker.docker/Data/docker-cli.sock"},
}

// NewFake returns a Fake with empty maps and a reachable daemon.
func NewFake() *Fake {
	return &Fake{
		Volumes:            map[string]bool{},
		PulledImg:          map[string]bool{},
		Containers:         map[string]ContainerState{},
		ExecResponses:      map[string]ExecResponse{},
		Networks:           map[string]bool{},
		NetworkConnections: map[string]bool{},
		ImageIDs:           map[string]string{},
		ContainerImageIDs:  map[string]string{},
		Engine:             DesktopEngine,
		Gateway:            "172.17.0.1",
		Specs:              map[string]ContainerSpec{},
		Infos:              map[string]ContainerInfo{},
		ExecHang:           map[string]bool{},
	}
}

// ExecResponse is one scripted response to Fake.Exec. The key in
// ExecResponses is "<container>:<joined-argv>" (argv joined by single space).
type ExecResponse struct {
	Stdout []byte
	Err    error
}

func (f *Fake) DaemonReachable() error { return f.DaemonErr }

func (f *Fake) EnsureVolume(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Volumes[name] = true
	return nil
}

func (f *Fake) PullImage(image string) error {
	if f.PullErr != nil {
		return f.PullErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PulledImg[image] = true
	return nil
}

func (f *Fake) ContainerState(name string) (ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DaemonErr != nil {
		return StateAbsent, f.DaemonErr
	}
	s, ok := f.Containers[name]
	if !ok {
		return StateAbsent, nil
	}
	return s, nil
}

func (f *Fake) CreateAndStart(spec ContainerSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Spec = spec
	f.Specs[spec.Name] = spec
	if err := f.CreateErrs[spec.Name]; err != nil {
		return err
	}
	if f.CreateAndStartErr != nil {
		// Mirror docker reality: when `docker run -d` fails part-way
		// (e.g. networking) the container record is still left behind in
		// "Created" state. Tests of the rollback path rely on this.
		f.Containers[spec.Name] = StateStopped
		return f.CreateAndStartErr
	}
	f.Containers[spec.Name] = StateRunning
	mode := spec.NetworkMode
	if mode == "" {
		mode = "bridge"
	}
	info := ContainerInfo{
		NetworkMode: mode,
		ExtraHosts:  append([]string(nil), spec.ExtraHosts...),
		Mounts:      append([]VolumeMount(nil), spec.Volumes...),
		Networks:    []string{mode},
		Command:     append([]string(nil), spec.Command...),
		Ports:       append([]PortMapping(nil), spec.PortMappings...),
	}
	SortPortMappings(info.Ports)
	f.Infos[spec.Name] = info
	return nil
}

func (f *Fake) Start(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Containers[name] = StateRunning
	return nil
}

func (f *Fake) Stop(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Containers[name]; ok {
		f.Containers[name] = StateStopped
	}
	return nil
}

func (f *Fake) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Removed = append(f.Removed, name)
	delete(f.Containers, name)
	delete(f.Infos, name)
	for k := range f.NetworkConnections {
		if strings.HasSuffix(k, ":"+name) {
			delete(f.NetworkConnections, k)
		}
	}
	return nil
}

func (f *Fake) Exec(ctx context.Context, name string, argv []string) ([]byte, error) {
	key := name + ":" + strings.Join(argv, " ")
	if f.ExecHang[key] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.ExecResponses[key]
	if !ok {
		return nil, fmt.Errorf("fake docker: no scripted Exec response for %q", key)
	}
	return r.Stdout, r.Err
}

func (f *Fake) EnsureNetwork(name string) error {
	if f.EnsureNetworkErr != nil {
		return f.EnsureNetworkErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Networks[name] = true
	return nil
}

func (f *Fake) ConnectNetwork(network, container string) error {
	if f.ConnectNetworkErr != nil {
		return f.ConnectNetworkErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.NetworkConnections[network+":"+container] = true
	if info, ok := f.Infos[container]; ok && !slices.Contains(info.Networks, network) {
		info.Networks = append(info.Networks, network)
		sort.Strings(info.Networks)
		f.Infos[container] = info
	}
	return nil
}

func (f *Fake) ImageID(ref string) (string, error) {
	if f.ImageIDErr != nil {
		return "", f.ImageIDErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ImageIDs[ref], nil
}

func (f *Fake) ContainerImageID(name string) (string, error) {
	if f.ContainerImageIDErr != nil {
		return "", f.ContainerImageIDErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ContainerImageIDs[name], nil
}

func (f *Fake) EngineInfo() (EngineInfo, error) {
	if f.EngineInfoErr != nil {
		return EngineInfo{}, f.EngineInfoErr
	}
	return f.Engine, nil
}

func (f *Fake) BridgeGateway() (string, error) {
	if f.GatewayErr != nil {
		return "", f.GatewayErr
	}
	return f.Gateway, nil
}

// InspectContainer reports Infos[name]. A container present in Containers
// but never given an Info (seeded by state alone) inspects as a plain
// bridge container on "bridge" plus whatever ConnectNetwork attached.
func (f *Fake) InspectContainer(name string) (ContainerInfo, error) {
	if f.InspectErr != nil {
		return ContainerInfo{}, f.InspectErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if info, ok := f.Infos[name]; ok {
		info.Networks = append([]string(nil), info.Networks...)
		return info, nil
	}
	if _, ok := f.Containers[name]; !ok {
		return ContainerInfo{}, fmt.Errorf("fake docker: no such container %q", name)
	}
	info := ContainerInfo{NetworkMode: "bridge", Networks: []string{"bridge"}}
	for k := range f.NetworkConnections {
		if n, ok := strings.CutSuffix(k, ":"+name); ok {
			info.Networks = append(info.Networks, n)
		}
	}
	sort.Strings(info.Networks)
	return info, nil
}
