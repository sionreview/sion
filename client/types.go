package client

import (
	"context"

	"github.com/sionreview/sion/common/redeo/client"
)

type WaitGroup interface {
	Add(int)
	Done()
	Wait()
}

type ClientConnMeta struct {
	Addr    string
	AddrIdx int
}

type ClientRequest struct {
	client.Request
	Cmd    string
	ReqId  string
	Cancel context.CancelFunc
}
