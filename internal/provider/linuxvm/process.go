//go:build linux

package linuxvm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"vm-manager/internal/node"
)

type Process struct {
	Cmd     *exec.Cmd
	Console string
	Offset  int64
	IP      string
	done    chan struct{}
}

// AttachDisks returns the host path of each node.AttachOrder disk; on error it detaches the NBDs already attached.
func AttachDisks(f node.VMFile, dir string) ([]string, []string, error) {
	var paths, nbdIDs []string
	for _, d := range node.AttachOrder(f) {
		if !d.NBD {
			paths = append(paths, filepath.Join(dir, d.File))
			continue
		}
		dev, err := AttachNBD(dir, d.Name, d.File)
		if err != nil {
			DetachAll(f.Spec.ID, dir, nbdIDs)
			return nil, nil, err
		}
		nbdIDs = append(nbdIDs, d.Name)
		paths = append(paths, dev)
	}
	return paths, nbdIDs, nil
}

func DetachAll(vmID, dir string, nbdIDs []string) {
	for _, id := range nbdIDs {
		if err := DetachNBD(dir, id); err != nil {
			log.Printf("vm %s: detach %s: %v", vmID, id, err)
		}
	}
}

// Spawn starts cmd with the VM's taps (owned by tapOwner if > 0) and console; once it exits, the taps and the NBD disks are released.
// onExit, if set, runs when cmd is gone (or did not start) and before Done reports it.
func Spawn(f node.VMFile, dir string, cmd *exec.Cmd, nbdIDs []string, tapOwner int, onExit func()) (*Process, error) {
	console := filepath.Join(dir, node.ConsoleLog)
	offset, err := node.ConsoleOffset(console)
	if err != nil {
		return nil, err
	}
	out, err := os.OpenFile(console, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := SetupTaps(f, tapOwner); err != nil {
		out.Close()
		RemoveTaps(f)
		return nil, fmt.Errorf("taps: %w", err)
	}
	cmd.Stdout, cmd.Stderr = out, out
	// The node's death must take its VMs along, as with vz; see the LockOSThread below.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	p := &Process{Cmd: cmd, Console: console, Offset: offset, IP: node.MgmtIP(f), done: make(chan struct{})}
	started := make(chan error, 1)
	go func() {
		// Pdeathsig fires when the starting thread exits, so this goroutine keeps its thread until the VM is gone.
		runtime.LockOSThread()
		err := cmd.Start()
		started <- err
		if err == nil {
			cmd.Wait()
		}
		out.Close()
		RemoveTaps(f)
		if onExit != nil {
			onExit()
		}
		if err == nil {
			DetachAll(f.Spec.ID, dir, nbdIDs)
			close(p.done)
		}
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Process) Done() <-chan struct{} { return p.done }

func (p *Process) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.Cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-p.done
	return nil
}

func (p *Process) WaitIP(ctx context.Context) (string, error) {
	if err := node.WaitReady(ctx, p.Console, p.Offset, time.Second); err != nil {
		return "", err
	}
	return p.IP, nil
}

// KillOnConsole kills the process when the guest prints text (a halt or panic marker the VMM would otherwise idle on). Run it as a goroutine.
func (p *Process) KillOnConsole(text string) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-p.done
		cancel()
	}()
	err := node.WaitConsole(ctx, p.Console, p.Offset, time.Second, text)
	if err == nil {
		p.Cmd.Process.Kill()
	} else if !errors.Is(err, context.Canceled) {
		log.Printf("watching console %s: %v", p.Console, err)
	}
}

func DialSocket(sock string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
}
