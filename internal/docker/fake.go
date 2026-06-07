package docker

import (
	"context"
	"fmt"
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
	if f.CreateAndStartErr != nil {
		// Mirror docker reality: when `docker run -d` fails part-way
		// (e.g. networking) the container record is still left behind in
		// "Created" state. Tests of the rollback path rely on this.
		f.Containers[spec.Name] = StateStopped
		return f.CreateAndStartErr
	}
	f.Containers[spec.Name] = StateRunning
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
	return nil
}

func (f *Fake) Exec(_ context.Context, name string, argv []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := name + ":" + strings.Join(argv, " ")
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
	return nil
}

func (f *Fake) NetworkAttached(network, container string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.NetworkConnections[network+":"+container], nil
}
