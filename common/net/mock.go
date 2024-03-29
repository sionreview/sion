package net

import (
	"fmt"
	"regexp"
	"sync/atomic"

	mock "github.com/jordwest/mock-conn"
	"github.com/sionreview/sion/common/util/hashmap"
)

var (
	// Shortcut Registry for build-in shortcut connections.
	Shortcut *shortcut

	// Concurency Estimate concurrency required.
	Concurrency = 1000

	shortcutAddress    = "shortcut:%d:%s"
	shortcutRecognizer = regexp.MustCompile(`^shortcut:[0-9]+:(.+)$`)
)

type shortcut struct {
	ports hashmap.HashMap
}

func InitShortcut() *shortcut {
	if Shortcut == nil {
		Shortcut = &shortcut{
			ports: hashmap.NewMapWithStringKey(Concurrency),
		}
	}
	return Shortcut
}

func (s *shortcut) Prepare(addr string, id int, nums ...int) *ShortcutConn {
	n := 1
	if len(nums) > 0 {
		n = nums[0]
	}
	if n < 1 {
		n = 1
	}

	// To keep consistent of hash ring, the address must be recoverable and keep consistent.
	address := fmt.Sprintf(shortcutAddress, id, addr)
	conn, existed := s.ports.Load(address) // For specified id, GetOrInsert is not necessary.
	if !existed {
		newConn := NewShortcutConn(address, n)
		s.ports.Store(address, newConn)
		return newConn
	} else {
		return conn.(*ShortcutConn)
	}
}

func (s *shortcut) Validate(address string) (string, bool) {
	match := shortcutRecognizer.FindStringSubmatch(address)
	if len(match) > 0 {
		return match[1], true
	} else {
		return "", false
	}
}

func (s *shortcut) GetConn(address string) (*ShortcutConn, bool) {
	conn, existed := s.ports.Load(address)
	if !existed {
		return nil, false
	} else {
		return conn.(*ShortcutConn), true
	}
}

func (s *shortcut) Dial(address string) ([]*MockConn, bool) {
	conn, existed := s.ports.Load(address)
	if !existed {
		return nil, false
	} else {
		return conn.(*ShortcutConn).Validate().Conns, true
	}
}

func (s *shortcut) Invalidate(conn *ShortcutConn) {
	s.ports.Delete(conn.Address)
}

type MockConn struct {
	Server *MockEnd
	Client *MockEnd

	mock   *mock.Conn
	parent *ShortcutConn
	idx    int
	id     int32
}

func NewMockConn(scn *ShortcutConn, idx int, id int32) *MockConn {
	mock := mock.NewConn()
	conn := &MockConn{
		mock:   mock,
		parent: scn,
		idx:    idx,
		id:     id,
	}
	conn.Server = &MockEnd{End: mock.Server, parent: conn}
	conn.Client = &MockEnd{End: mock.Client, parent: conn}
	return conn
}

func (c *MockConn) String() string {
	return fmt.Sprintf("%s(%d)[%d]", c.parent.Address, c.idx, c.id)
}

func (c *MockConn) Close() error {
	return c.parent.close(c.idx, c)
}

func (c *MockConn) invalid() bool {
	return c.parent.invalid(c.idx, c)
}

func (c *MockConn) close() error {
	c.Server.setStatus("dropped")
	c.Client.setStatus("dropped")
	return c.mock.Close()
}

type ShortcutConn struct {
	Conns      []*MockConn
	Client     interface{}
	Address    string
	OnValidate func(*MockConn)
	seq        int32
}

func NewShortcutConn(addr string, n int) *ShortcutConn {
	conn := &ShortcutConn{Address: addr}
	conn.OnValidate = conn.defaultValidateHandler
	if n == 1 {
		conn.Conns = []*MockConn{nil}
	} else {
		conn.Conns = make([]*MockConn, n)
	}

	return conn
}

func (cn *ShortcutConn) Close(idxes ...int) {
	if len(idxes) == 0 {
		for i, conn := range cn.Conns {
			cn.close(i, conn)
		}
	} else {
		for _, i := range idxes {
			cn.close(i, cn.Conns[i])
		}
	}
}

func (cn *ShortcutConn) close(i int, conn *MockConn) error {
	if conn != nil && conn == cn.Conns[i] {
		cn.Conns[i] = nil
		return conn.close()
	}

	return nil
}

func (cn *ShortcutConn) invalid(i int, conn *MockConn) bool {
	if conn == cn.Conns[i] {
		cn.Conns[i] = nil
		return true
	}
	return false
}

func (cn *ShortcutConn) Validate(idxes ...int) *ShortcutConn {
	if len(idxes) == 0 {
		for i, conn := range cn.Conns {
			cn.validate(i, conn)
		}
	} else {
		for _, i := range idxes {
			cn.validate(i, cn.Conns[i])
		}
	}
	return cn
}

func (cn *ShortcutConn) validate(i int, conn *MockConn) {
	if conn == nil {
		cn.Conns[i] = NewMockConn(cn, i, atomic.AddInt32(&cn.seq, 1))
		cn.OnValidate(cn.Conns[i])
	}
}

func (cn *ShortcutConn) defaultValidateHandler(_ *MockConn) {
}
