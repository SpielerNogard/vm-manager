package node

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"vm-manager/internal/hub"
	"vm-manager/internal/proto"
)

type fakeInstance struct {
	once    sync.Once
	done    chan struct{}
	stopped bool
}

func (i *fakeInstance) WaitIP(context.Context) (string, error) { return "127.0.0.1", nil }
func (i *fakeInstance) Done() <-chan struct{}                  { return i.done }
func (i *fakeInstance) Stop() error {
	i.once.Do(func() { i.stopped = true; close(i.done) })
	return nil
}

type fakeProvider struct {
	mu          sync.Mutex
	prepared    []string
	started     map[string]*fakeInstance
	failPrepare map[string]bool
	failStart   map[string]bool
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{started: map[string]*fakeInstance{}, failPrepare: map[string]bool{}, failStart: map[string]bool{}}
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Prepare(_ context.Context, f VMFile, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepared = append(p.prepared, f.Spec.Name)
	if p.failPrepare[f.Spec.Name] {
		return errors.New("prepare boom")
	}
	return nil
}

func (p *fakeProvider) Start(f VMFile, _ string) (Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failStart[f.Spec.Name] {
		return nil, errors.New("start boom")
	}
	inst := &fakeInstance{done: make(chan struct{})}
	p.started[f.Spec.Name] = inst
	return inst, nil
}

func (p *fakeProvider) instance(name string) *fakeInstance {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started[name]
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type testEnv struct {
	reg      *hub.Registry
	agent    *Agent
	provider *fakeProvider
	hubPort  int
}

func startEnv(t *testing.T, prep func(a *Agent, p *fakeProvider)) *testEnv {
	t.Helper()
	port := freeTCPPort(t)
	reg := hub.NewRegistry(port, port)
	var listeners []io.Closer
	reg.OnPortOpen = func(p int) error {
		l, err := hub.ServeSSHPort(reg, p)
		if err == nil {
			listeners = append(listeners, l)
		}
		return err
	}
	srv := httptest.NewServer(hub.NodeHandler(reg, "tok"))

	p := newFakeProvider()
	a := &Agent{
		HubURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/nodes/connect", Token: "tok",
		NodeID: "n1", VMsDir: t.TempDir(), CPUs: 4, MemoryMB: 8192, Provider: p,
	}
	if prep != nil {
		prep(a, p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		<-done
		for _, l := range listeners {
			l.Close()
		}
	})
	eventually(t, "node online", func() bool { n := reg.Nodes(); return len(n) == 1 && n[0].Online })
	return &testEnv{reg: reg, agent: a, provider: p, hubPort: port}
}

func createReq(name string) hub.CreateVMRequest {
	return hub.CreateVMRequest{NodeID: "n1", Name: name, CPUs: 1, MemoryMB: 1024, DiskGB: 10, SSHPublicKey: "ssh-ed25519 AAA"}
}

func vmState(reg *hub.Registry, id string) (hub.VM, bool) {
	for _, vm := range reg.VMs() {
		if vm.ID == id {
			return vm, true
		}
	}
	return hub.VM{}, false
}

func TestAgentRegistersProvider(t *testing.T) {
	env := startEnv(t, nil)
	if p := env.reg.Nodes()[0].Provider; p != "fake" {
		t.Fatalf("provider = %q", p)
	}
}

func TestAgentCreateReachesRunning(t *testing.T) {
	env := startEnv(t, nil)
	vm, err := env.reg.CreateVM(createReq("dev"))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	if v, _ := vmState(env.reg, vm.ID); v.IP != "127.0.0.1" {
		t.Fatalf("ip = %q", v.IP)
	}
	files, _ := LoadVMs(env.agent.VMsDir)
	if len(files) != 1 || files[0].Spec.ID != vm.ID || files[0].Spec.SSHPort != vm.SSHPort {
		t.Fatalf("vm.json = %+v", files)
	}
	if mac, err := net.ParseMAC(files[0].MAC); err != nil || mac[0]&0x02 == 0 || mac[0]&0x01 != 0 {
		t.Fatalf("mac %q not locally administered unicast (%v)", files[0].MAC, err)
	}
}

func TestAgentStoppedWhenGuestShutsDown(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	close(env.provider.instance("dev").done)
	eventually(t, "stopped", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateStopped })
}

func TestAgentPrepareFails(t *testing.T) {
	env := startEnv(t, func(_ *Agent, p *fakeProvider) { p.failPrepare["bad"] = true })
	vm, _ := env.reg.CreateVM(createReq("bad"))
	eventually(t, "error", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateError })
	if v, _ := vmState(env.reg, vm.ID); !strings.Contains(v.Error, "prepare boom") {
		t.Fatalf("error = %q", v.Error)
	}
}

func TestAgentDelete(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	if err := env.reg.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "dir removed", func() bool { _, err := os.Stat(VMDir(env.agent.VMsDir, vm.ID)); return os.IsNotExist(err) })
	if !env.provider.instance("dev").stopped {
		t.Fatal("instance not stopped")
	}
}

func TestAgentRestoreAfterRestart(t *testing.T) {
	var good, bad VMFile
	env := startEnv(t, func(a *Agent, p *fakeProvider) {
		p.failStart["bad"] = true
		good = VMFile{Spec: proto.VMSpec{ID: "g1", Name: "good", CPUs: 1, MemoryMB: 1024, DiskGB: 10, SSHPublicKey: "k", SSHPort: 0}, MAC: "02:00:00:00:00:01"}
		bad = VMFile{Spec: proto.VMSpec{ID: "b1", Name: "bad", CPUs: 1, MemoryMB: 1024, DiskGB: 10, SSHPublicKey: "k"}, MAC: "02:00:00:00:00:02"}
		SaveVM(a.VMsDir, good)
		SaveVM(a.VMsDir, bad)
	})
	eventually(t, "both reported", func() bool {
		g, ok1 := vmState(env.reg, "g1")
		b, ok2 := vmState(env.reg, "b1")
		return ok1 && ok2 && g.State == proto.StateRunning && b.State == proto.StateError
	})
	if len(env.provider.prepared) != 0 {
		t.Fatalf("Prepare called on restore: %v", env.provider.prepared)
	}
}

func TestAgentStreamToVM(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c)
		}
	}()
	env := startEnv(t, func(a *Agent, _ *fakeProvider) { a.sshPort = echo.Addr().(*net.TCPAddr).Port })
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })

	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(env.hubPort))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	buf := make([]byte, 5)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("read %q err %v", buf, err)
	}
}
