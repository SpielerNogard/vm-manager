package main

import (
	"encoding/json"
	"io"

	"vm-manager/internal/netframe"
)

// handleConfigure applies one ConfigureFrame payload and answers with exactly one ConfigureResult.
func handleConfigure(w io.Writer, payload []byte, apply func(netframe.Configure) error) {
	var c netframe.Configure
	err := json.Unmarshal(payload, &c)
	if err == nil {
		err = apply(c)
	}
	var reply []byte
	if err != nil {
		reply = []byte(err.Error())
	}
	netframe.Write(w, netframe.ConfigureResult, 0, reply)
}
