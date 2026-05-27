package docker

import "sync"

// Fake is an in-memory Client for app-layer tests. Records calls and
// transitions container state per CreateAndStart/Start/Stop.
type Fake struct {
	mu sync.Mutex

	DaemonErr error
	PullErr   error

	Volumes    map[string]bool
	PulledImg  map[string]bool
	Containers map[string]ContainerState
	Spec       ContainerSpec
}

// NewFake returns a Fake with empty maps and a reachable daemon.
func NewFake() *Fake {
	return &Fake{
		Volumes:    map[string]bool{},
		PulledImg:  map[string]bool{},
		Containers: map[string]ContainerState{},
	}
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
