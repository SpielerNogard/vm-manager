package cloudhypervisor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
	"vm-manager/internal/provider/linuxvm"
)

const testImageURL = "test://noble"

// testProvider needs root, KVM, bridge vmm0, cloud-hypervisor, the firmware and VMM_TEST_IMAGE (a raw bootable noble disk).
func testProvider(t *testing.T) *Provider {
	t.Helper()
	img := os.Getenv("VMM_TEST_IMAGE")
	if img == "" || os.Geteuid() != 0 {
		t.Skip("needs root and VMM_TEST_IMAGE")
	}
	fw := os.Getenv("VMM_TEST_FIRMWARE")
	if fw == "" {
		fw = DefaultFirmware
	}
	base := t.TempDir()
	images := filepath.Join(base, "images")
	if err := os.MkdirAll(images, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(testImageURL))
	if err := os.Link(img, filepath.Join(images, hex.EncodeToString(sum[:8])+".raw")); err != nil {
		t.Fatal(err)
	}
	section, _ := json.Marshal(map[string]string{"imageURL": testImageURL, "firmware": fw})
	p, err := New(node.Config{Provider: "cloud-hypervisor", Paths: struct {
		VMs    string `json:"vms"`
		Images string `json:"images"`
	}{VMs: filepath.Join(base, "vms"), Images: images}, Raw: map[string]json.RawMessage{"cloud-hypervisor": section}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testVM(id string) node.VMFile {
	return node.VMFile{MAC: "06:00:00:00:00:01", Spec: proto.VMSpec{ID: id, Name: id, CPUs: 1, MemoryMB: 1024, DiskGB: 10}}
}

func startVM(t *testing.T, p *Provider, f node.VMFile) node.Instance {
	t.Helper()
	dir := node.VMDir(p.VMsDir, f.Spec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.Prepare(context.Background(), f, dir); err != nil {
		t.Fatal(err)
	}
	inst, err := p.Start(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

// sshVM gives f an SSH key and returns a function running a command in the guest.
func sshVM(t *testing.T, p *Provider, f *node.VMFile) func(cmd string) ([]byte, error) {
	t.Helper()
	key := filepath.Join(t.TempDir(), "id")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	f.Spec.SSHPublicKeys = []string{string(pub)}
	ip, err := linuxvm.AllocNATIP(p.VMsDir, f.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	return func(cmd string) ([]byte, error) {
		return exec.Command("ssh", "-i", key, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=30", "-o", "BatchMode=yes",
			"-o", "ServerAliveInterval=2", "-o", "ServerAliveCountMax=3", "-o", "LogLevel=ERROR", "ubuntu@"+ip, cmd).CombinedOutput()
	}
}

func waitIP(t *testing.T, inst node.Instance) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := inst.WaitIP(ctx); err != nil {
		inst.Kill()
		t.Fatalf("WaitIP: %v", err)
	}
}

func waitDone(t *testing.T, inst node.Instance, d time.Duration, what string) {
	t.Helper()
	select {
	case <-inst.Done():
	case <-time.After(d):
		t.Fatalf("instance still running %s after %s", what, d)
	}
}

func TestStartStopVM(t *testing.T) {
	p := testProvider(t)
	f := testVM("chstartstop")
	inst := startVM(t, p, f)
	defer inst.Kill()
	waitIP(t, inst)
	if err := inst.Stop(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, inst, 15*time.Second, "after Stop")
	if _, err := net.InterfaceByName(linuxvm.TapName(f.Spec.ID, 0)); err == nil {
		t.Error("tap still exists after stop")
	}
}

func TestStartCleansStaleTaps(t *testing.T) {
	p := testProvider(t)
	f := testVM("chstaletap")
	if out, err := exec.Command("ip", "tuntap", "add", "dev", linuxvm.TapName(f.Spec.ID, 0), "mode", "tap").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	inst := startVM(t, p, f)
	inst.Kill()
	<-inst.Done()
}

func TestGuestPoweroffEndsInstance(t *testing.T) {
	p := testProvider(t)
	f := testVM("chpoweroff")
	ssh := sshVM(t, p, &f)
	inst := startVM(t, p, f)
	defer inst.Kill()
	waitIP(t, inst)
	if out, err := ssh("sudo poweroff"); err != nil && len(out) > 0 && !strings.Contains(string(out), "closed") {
		t.Logf("ssh: %v: %s", err, out)
	}
	waitDone(t, inst, 60*time.Second, "after guest poweroff")
}

func TestGuestRebootKeepsInstance(t *testing.T) {
	p := testProvider(t)
	f := testVM("chreboot")
	ssh := sshVM(t, p, &f)
	inst := startVM(t, p, f)
	defer inst.Kill()
	waitIP(t, inst)
	bootID, err := ssh("cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatalf("ssh before reboot: %v: %s", err, bootID)
	}
	ssh("sudo reboot")
	// Wait until the guest is gone before probing, or the old sshd answers.
	time.Sleep(10 * time.Second)
	deadline := time.Now().Add(80 * time.Second)
	back := false
	for !back && time.Now().Before(deadline) {
		select {
		case <-inst.Done():
			t.Fatal("instance ended on guest reboot")
		default:
		}
		if out, err := ssh("cat /proc/sys/kernel/random/boot_id"); err == nil {
			back = true
			if string(out) == string(bootID) {
				t.Fatal("boot_id unchanged: the guest did not reboot")
			}
		} else {
			time.Sleep(2 * time.Second)
		}
	}
	if !back {
		t.Fatal("guest not reachable over SSH within 90s of reboot")
	}
	if err := inst.Stop(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, inst, 15*time.Second, "after Stop")
}

func TestGuestPanicEndsInstance(t *testing.T) {
	p := testProvider(t)
	f := testVM("chpanic")
	ssh := sshVM(t, p, &f)
	inst := startVM(t, p, f)
	defer inst.Kill()
	waitIP(t, inst)
	ssh("echo c | sudo tee /proc/sysrq-trigger")
	waitDone(t, inst, 60*time.Second, "after guest panic")
}

func TestStopDuringBoot(t *testing.T) {
	p := testProvider(t)
	inst := startVM(t, p, testVM("chstopboot"))
	defer inst.Kill()
	stopped := make(chan error, 1)
	go func() { stopped <- inst.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Stop did not return within 20s")
	}
	select {
	case <-inst.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
}

func TestStartWithNBDDisk(t *testing.T) {
	url := os.Getenv("VMM_TEST_NBD")
	if url == "" {
		t.Skip("needs VMM_TEST_NBD")
	}
	p := testProvider(t)
	f := testVM("chnbd")
	f.Spec.Attachments = []proto.DiskAttachment{{DiskID: "d1", MountPath: "/data", URL: url}}
	ssh := sshVM(t, p, &f)
	inst := startVM(t, p, f)
	dir := node.VMDir(p.VMsDir, f.Spec.ID)
	waitIP(t, inst)
	disks, err := p.Disks(f, dir)
	if err != nil || len(disks) != 3 || disks[1].Name != "d1" || disks[1].Device != "vdb" || disks[1].Error != "" {
		inst.Kill()
		t.Fatalf("disks = %+v, %v", disks, err)
	}
	if out, err := ssh("findmnt -n -o SOURCE /data"); err != nil || strings.TrimSpace(string(out)) != "/dev/vdb" {
		inst.Kill()
		t.Fatalf("findmnt /data = %q, %v", out, err)
	}
	b, _ := os.ReadFile(linuxvm.NBDRecord(dir, "d1"))
	dev := strings.TrimSpace(string(b))
	if err := inst.Stop(); err != nil {
		t.Fatal(err)
	}
	<-inst.Done()
	if linuxvm.NBDConnected(dev) {
		t.Fatalf("%s still connected after the VM stopped", dev)
	}
}

func TestKillAfterNodeCrash(t *testing.T) {
	if os.Getenv("VMM_CH_HELPER") == "1" {
		p := testProvider(t)
		inst := startVM(t, p, testVM("chcrash"))
		fmt.Println("PID", inst.(*instance).Cmd.Process.Pid)
		time.Sleep(time.Hour)
		return
	}
	testProvider(t)
	cmd := exec.Command(os.Args[0], "-test.run", "^TestKillAfterNodeCrash$", "-test.v")
	cmd.Env = append(os.Environ(), "VMM_CH_HELPER=1")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	found := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(out)
		pid := 0
		for pid == 0 && sc.Scan() {
			fmt.Sscanf(sc.Text(), "PID %d", &pid)
		}
		found <- pid
	}()
	pid := 0
	select {
	case pid = <-found:
	case <-time.After(3 * time.Minute):
	}
	if pid == 0 {
		cmd.Process.Kill()
		t.Fatal("helper did not start a VM")
	}
	cmd.Process.Kill()
	cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("cloud-hypervisor %s survived its node", strconv.Itoa(pid))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
