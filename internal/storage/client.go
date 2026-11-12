package storage

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"vm-manager/internal/proto"
)

const (
	reconnectDelay = 2 * time.Second
	messageLimit   = 4 << 20
)

// Run keeps a link to the hub: it applies every desired list, then reports its files, and also reports every `report`.
func Run(ctx context.Context, hubURL, token, endpoint string, dir Dir, report time.Duration) error {
	for {
		if err := connect(ctx, hubURL, token, endpoint, dir, report); err != nil && ctx.Err() == nil {
			log.Printf("hub connection: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectDelay):
		}
	}
}

func connect(ctx context.Context, hubURL, token, endpoint string, dir Dir, report time.Duration) error {
	c, _, err := websocket.Dial(ctx, hubURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(messageLimit)
	if err := wsjson.Write(ctx, c, proto.StorageMessage{Type: "register", Endpoint: endpoint}); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	msgs, readErr := make(chan proto.StorageMessage), make(chan error, 1)
	go func() {
		for {
			var m proto.StorageMessage
			if err := wsjson.Read(ctx, c, &m); err != nil {
				readErr <- err
				return
			}
			select {
			case msgs <- m:
			case <-ctx.Done():
				return
			}
		}
	}()

	// One goroutine applies and reports, so a report never predates an Apply the hub already sees as done.
	sendReport := func() error {
		files, err := dir.Scan()
		if err != nil {
			log.Printf("scanning %s: %v", dir.Path, err)
			return nil
		}
		return wsjson.Write(ctx, c, proto.StorageMessage{Type: "report", Files: files})
	}
	tick := time.NewTicker(report)
	defer tick.Stop()
	for {
		select {
		case err := <-readErr:
			return err
		case m := <-msgs:
			if m.Type != "desired" {
				continue
			}
			if err := dir.Apply(m.Disks, m.Deleted); err != nil {
				log.Printf("applying desired disks: %v", err)
			}
			if err := sendReport(); err != nil {
				return err
			}
		case <-tick.C:
			if err := sendReport(); err != nil {
				return err
			}
		}
	}
}
