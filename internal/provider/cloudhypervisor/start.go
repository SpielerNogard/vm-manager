//go:build linux

package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"vm-manager/internal/node"
	"vm-manager/internal/provider/linuxvm"
)

func (p *Provider) Start(f node.VMFile, dir string) (node.Instance, error) {
	if err := p.Reseed(context.Background(), f, dir); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	drives, nbds, err := linuxvm.AttachDisks(f, dir)
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "ch.sock")
	a, err := args(f, p.Firmware, sock, drives)
	if err != nil {
		linuxvm.DetachAll(f.Spec.ID, dir, nbds)
		return nil, err
	}
	os.Remove(sock)
	proc, err := linuxvm.Spawn(f, dir, exec.Command("cloud-hypervisor", a...), nbds, 0, nil)
	if err != nil {
		linuxvm.DetachAll(f.Spec.ID, dir, nbds)
		return nil, err
	}
	// A panicked guest hangs with the VMM still running; ending it lets the node restart the VM.
	go proc.KillOnConsole("Kernel panic - not syncing")
	for _, id := range nbds {
		go p.nbd.Watch(f.Spec.ID, dir, id, proc.Done())
	}
	return &instance{Process: proc, sock: sock}, nil
}

type instance struct {
	*linuxvm.Process
	sock string
}

// Stop presses the ACPI power button and kills the VM if it is still running after 10s.
func (i *instance) Stop() error {
	if err := i.powerButton(); err == nil {
		select {
		case <-i.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	return i.Kill()
}

func (i *instance) powerButton() error {
	req, err := http.NewRequest(http.MethodPut, "http://localhost/api/v1/vm.power-button", nil)
	if err != nil {
		return err
	}
	resp, err := linuxvm.DialSocket(i.sock).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("vm.power-button: %s", resp.Status)
	}
	return nil
}
