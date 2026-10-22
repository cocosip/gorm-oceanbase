package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// fakeServer is the in-process server stand-in used by unit tests. It records
// statements and answers exact-match queries from a reply table.
type fakeServer struct {
	mu      sync.Mutex
	queries []string
	execs   []string
	replies map[string]fakeReply
	pingErr error
}

type fakeReply struct {
	value string
	err   error
}

func newFakeServer() *fakeServer {
	return &fakeServer{replies: map[string]fakeReply{}}
}

func (f *fakeServer) reply(query string, values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value := ""
	if len(values) > 0 {
		value = values[0]
	}
	f.replies[query] = fakeReply{value: value}
}

func (f *fakeServer) replyError(query string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[query] = fakeReply{err: err}
}

func (f *fakeServer) recordedQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func (f *fakeServer) answer(query string) fakeReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	if reply, ok := f.replies[query]; ok {
		return reply
	}
	return fakeReply{err: fmt.Errorf("fakeServer: no reply for %q", query)}
}

// fakePool adapts a fake server to gorm.ConnPool through database/sql, so tests
// receive real *sql.Rows and *sql.Row values without a network connection.
type fakePool struct {
	server *fakeServer
	db     *sql.DB
}

func newFakePool(t interface{ Fatalf(string, ...interface{}) }) *fakePool {
	server := newFakeServer()
	registry.mu.Lock()
	registry.seq++
	name := fmt.Sprintf("fake-%d", registry.seq)
	registry.servers[name] = server
	registry.mu.Unlock()

	db, err := sql.Open(fakeDriverName, name)
	if err != nil {
		t.Fatalf("open fake pool: %v", err)
	}
	return &fakePool{server: server, db: db}
}

func (p *fakePool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}

func (p *fakePool) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	p.server.mu.Lock()
	p.server.execs = append(p.server.execs, query)
	p.server.mu.Unlock()
	return p.db.ExecContext(ctx, query, args...)
}

func (p *fakePool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	p.server.mu.Lock()
	p.server.queries = append(p.server.queries, query)
	p.server.mu.Unlock()
	return p.db.QueryContext(ctx, query, args...)
}

func (p *fakePool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	p.server.mu.Lock()
	p.server.queries = append(p.server.queries, query)
	p.server.mu.Unlock()
	return p.db.QueryRowContext(ctx, query, args...)
}

const fakeDriverName = "oceanbase_fake"

var registry = &struct {
	mu      sync.Mutex
	seq     int
	servers map[string]*fakeServer
}{servers: map[string]*fakeServer{}}

func init() { sql.Register(fakeDriverName, fakeDriver{}) }

type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) {
	registry.mu.Lock()
	server, ok := registry.servers[name]
	registry.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fakeDriver: unknown database %q", name)
	}
	return &fakeConn{server: server}, nil
}

type fakeConn struct{ server *fakeServer }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: Prepare is not supported")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeConn: Begin is not supported")
}
func (c *fakeConn) Ping(context.Context) error {
	c.server.mu.Lock()
	defer c.server.mu.Unlock()
	return c.server.pingErr
}

func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	reply := c.server.answer(query)
	if reply.err != nil {
		return nil, reply.err
	}
	return &fakeRows{value: reply.value}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.server.mu.Lock()
	c.server.execs = append(c.server.execs, query)
	c.server.mu.Unlock()
	return fakeResult{}, nil
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) {
	return 0, errors.New("fakeResult: LastInsertId is not supported")
}
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeRows struct {
	done  bool
	value string
}

func (r *fakeRows) Columns() []string { return []string{"value"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}

// containsQuery reports whether any recorded query contains substr.
func containsQuery(queries []string, substr string) bool {
	for _, q := range queries {
		if strings.Contains(q, substr) {
			return true
		}
	}
	return false
}

var (
	_ driver.QueryerContext = (*fakeConn)(nil)
	_ driver.ExecerContext  = (*fakeConn)(nil)
	_ driver.Pinger         = (*fakeConn)(nil)
)
