package node

import "context"

type Provider interface {
	Name() string
	// Prepare runs once at create time; Start runs on create and again after every node restart.
	Prepare(ctx context.Context, f VMFile, dir string) error
	Start(f VMFile, dir string) (Instance, error)
}

type Instance interface {
	WaitIP(ctx context.Context) (string, error)
	Stop() error
	Done() <-chan struct{}
}
