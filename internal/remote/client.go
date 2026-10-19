// Package remote drives the agent-manager on another machine over SSH,
// through that machine's own CLI.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	callTimeout   = 15 * time.Second
	createTimeout = 60 * time.Second
	maxInFlight   = 4
	// exitSSH is ssh's own failure; the remote command's status passes
	// through otherwise.
	exitSSH = 255
	// exitNotFound is the login shell's answer to a command not on PATH.
	exitNotFound = 127
)

type Client struct {
	controlDir string
	run        Runner
	now        func() time.Time
	inFlight   chan struct{}

	mu          sync.Mutex
	connections []Connection
	invalid     []error
	hostSlots   map[string]chan struct{}
	states      map[string]hostRecord
	starts      map[string]time.Time
}

type Option func(*Client)

// WithRunner replaces ssh, so a test sees the exact argv.
func WithRunner(run Runner) Option {
	return func(c *Client) { c.run = run }
}

func WithClock(now func() time.Time) Option {
	return func(c *Client) { c.now = now }
}

// New keeps one control socket directory per profile, so two profiles on
// one machine never share an SSH master.
func New(profileDir string, opts ...Option) *Client {
	c := &Client{
		controlDir: ControlDir(profileDir),
		run:        runSSH,
		now:        time.Now,
		inFlight:   make(chan struct{}, maxInFlight),
		hostSlots:  map[string]chan struct{}{},
		states:     map[string]hostRecord{},
		starts:     map[string]time.Time{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SetConnections replaces the connection list. A connection that is gone,
// or now points at another destination or path, loses its cached state. Each one is
// validated again, since the store can be edited by hand: one that fails
// stays listed but every call to it fails with the reason, before ssh
// could read its destination as an option.
func (c *Client) SetConnections(connections []Connection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connections = slices.Clone(connections)
	c.invalid = make([]error, len(connections))
	for i, conn := range c.connections {
		c.invalid[i] = ValidateConnection(conn, c.connections[:i])
	}
	for name, record := range c.states {
		if !c.unchanged(name, record.conn) {
			delete(c.states, name)
			delete(c.starts, name)
		}
	}
}

func (c *Client) Connections() []Connection {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.connections)
}

func (c *Client) connection(host string) (Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.find(host)
}

// find looks host up; c.mu is held.
func (c *Client) find(host string) (Connection, error) {
	for i, conn := range c.connections {
		if conn.Name != host {
			continue
		}
		if err := c.invalid[i]; err != nil {
			return Connection{}, &Error{Host: host, Err: err}
		}
		return conn, nil
	}
	return Connection{}, &Error{Host: host, Err: errUnknownConnection}
}

var errUnknownConnection = errors.New("no connection has that name")

// unchanged reports whether host still names the same connection; c.mu is
// held.
func (c *Client) unchanged(host string, was Connection) bool {
	for _, conn := range c.connections {
		if conn.Name == host {
			return conn == was
		}
	}
	return false
}

func (c *Client) hostSlot(host string) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	slot, ok := c.hostSlots[host]
	if !ok {
		slot = make(chan struct{}, 1)
		c.hostSlots[host] = slot
	}
	return slot
}

// call runs agent-manager on host with args. The host's slot is taken
// before the global one, so a call queued behind its own host never holds
// a slot another host could use. stdout comes back on failure too.
func (c *Client) call(ctx context.Context, host string, timeout time.Duration, args ...string) ([]byte, error) {
	conn, err := c.connection(host)
	if err != nil {
		return nil, err
	}
	slot := c.hostSlot(host)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return nil, &Error{Host: host, Err: ctx.Err()}
	}
	select {
	case c.inFlight <- struct{}{}:
		defer func() { <-c.inFlight }()
	case <-ctx.Done():
		return nil, &Error{Host: host, Err: ctx.Err()}
	}
	if err := prepareSharing(c.controlDir); err != nil {
		return nil, &Error{Host: host, Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := c.sshArgv(conn.Destination, false, remoteCommand(conn.program(), args))
	stdout, stderr, err := c.run(ctx, argv)
	out, marked := answer(stdout)
	if err != nil {
		return out, classify(ctx, conn, stderr, err)
	}
	if !marked {
		return nil, &Error{Host: host, Err: errors.New("the login shell there exited before it ran agent-manager")}
	}
	return out, nil
}

// Error is a failed call to one connection.
type Error struct {
	Host    string
	Err     error
	trouble Trouble
}

// Trouble is the kind of failure that kept a call from a host's manager, so
// a row can say it in a word or two.
type Trouble uint8

const (
	// TroubleFailed is any other failure, such as the command refusing.
	TroubleFailed Trouble = iota
	// TroubleUnreachable is ssh's own failure, or a host that did not
	// answer in time.
	TroubleUnreachable
	// TroubleMissing is no agent-manager where the call looked for it.
	TroubleMissing
	// TroubleOutdated is an agent-manager there too old for this one.
	TroubleOutdated
	// TroubleNewer is an agent-manager there newer than this one.
	TroubleNewer
)

func TroubleOf(err error) Trouble {
	if errors.Is(err, context.DeadlineExceeded) {
		return TroubleUnreachable
	}
	var remote *Error
	if errors.As(err, &remote) {
		return remote.trouble
	}
	return TroubleFailed
}

// Error cleans the host's name too: one read back from the store or passed
// by an agent may not have been validated.
func (e *Error) Error() string {
	return cleanText(e.Host) + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Unreachable reports a failure of the connection itself rather than of the
// remote command: ssh failed, or the host did not answer in time.
func Unreachable(err error) bool {
	return TroubleOf(err) == TroubleUnreachable
}

func classify(ctx context.Context, conn Connection, stderr []byte, err error) error {
	host := conn.Name
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Host: host, Err: fmt.Errorf("did not answer in time: %w", context.DeadlineExceeded)}
	}
	if ctx.Err() != nil {
		return &Error{Host: host, Err: ctx.Err()}
	}
	text := lastLine(stderr)
	var exited interface{ ExitCode() int }
	if !errors.As(err, &exited) {
		return &Error{Host: host, Err: err}
	}
	switch exited.ExitCode() {
	case exitSSH:
		if text == "" {
			text = err.Error()
		}
		return &Error{Host: host, Err: errors.New("unreachable over SSH: " + text), trouble: TroubleUnreachable}
	case exitNotFound:
		if conn.Binary != "" {
			return &Error{Host: host, Err: fmt.Errorf("agent-manager is not at %s there; install it there or fix the connection's path", conn.Binary), trouble: TroubleMissing}
		}
		return &Error{Host: host, Err: errors.New("agent-manager is not on the login shell's PATH there; install it, add it to PATH in the login profile, or set the connection's path"), trouble: TroubleMissing}
	}
	if text == "" {
		return &Error{Host: host, Err: err}
	}
	return &Error{Host: host, Err: errors.New(strings.TrimPrefix(text, "agent-manager: "))}
}

// lastLine skips whatever a login profile or an ssh warning printed before
// the line that says what went wrong.
func lastLine(output []byte) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return strings.TrimSpace(cleanText(lines[len(lines)-1]))
}

func (c *Client) decode(ctx context.Context, host string, timeout time.Duration, into any, args ...string) error {
	out, err := c.call(ctx, host, timeout, args...)
	if err != nil {
		// A command an older agent-manager does not know falls through to
		// its interactive manager, which fails without a terminal after
		// printing escape sequences instead of JSON.
		if !Unreachable(err) && len(bytes.TrimSpace(out)) > 0 && !json.Valid(out) {
			return outdated(host)
		}
		return err
	}
	return unreadable(host, args[0], json.Unmarshal(out, into))
}

func outdated(host string) error {
	return &Error{Host: host, Err: errors.New("agent-manager there is too old for SSH connections; update agent-manager on that host"), trouble: TroubleOutdated}
}

func newer(host string) error {
	return &Error{Host: host, Err: errors.New("agent-manager there is newer than this one; update agent-manager on this machine"), trouble: TroubleNewer}
}

func unreadable(host, command string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Host: host, Err: fmt.Errorf("unreadable answer to %s: %w", command, err)}
}
