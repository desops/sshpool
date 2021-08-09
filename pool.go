package sshpool

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// DefaultMaxSessions is set to the default /etc/ssh/sshd_config value.
	// Most servers have not set MaxSessions, so they get the default limit of 10.
	DefaultMaxSessions = 10

	// DefaultMaxConnections is a bit arbitrary. It's a tradeoff between how long
	// you wait for dials, and how long you wait for concurrent operations to finish.
	DefaultMaxConnections = 10

	// DefaultSessionCloseDelay was found by testing. 10ms was ALMOST enough. (3 / 1000 would fail)
	DefaultSessionCloseDelay = time.Millisecond * 20
)

type Pool struct {
	config     *ssh.ClientConfig
	poolconfig *PoolConfig

	clients_mu    sync.Mutex
	clients       map[string][]*client
	nextclientid  int
	nextsessionid int

	dialing_mu sync.Mutex
	dialing    map[string]chan struct{}
}

type client struct {
	*ssh.Client
	sessions chan struct{} // this channel is used for MaxSessions limiting
	clientid int
}

type PoolConfig struct {
	Debug bool

	// MaxSessions defines the maximum sessions per-connection. If left at 0,
	// DefaultMaxSessions is used.
	MaxSessions int

	// MaxConnections limits the number of connections to the same host. If
	// left at 0, DefaultMaxConnections is used. (Note each connection can have up to
	// MaxSessions concurrent things happening on it.) Setting this to 1 is not
	// a bad idea if you want to be gentle to your servers.
	MaxConnections int

	// SSH seems to take a moment to actually clean up a session after you close
	// it. This delay seems to prevent "no more sessions" errors from happening
	// by giving a very slight delay after closing but before allowing another
	// connection. If 0, DefaultSessionCloseDelay is used.
	SessionCloseDelay time.Duration
}

type Session struct {
	*ssh.Session
	pool      *Pool
	client    *client
	host      string
	sessionid int
}

func (s *Session) String() string {
	return fmt.Sprintf("ssh session %d host %s", s.sessionid, s.host)
}

func (s *Session) Put() {
	// NOTE see also SFTPSession.Put()

	// This doesn't seem to actually do anything if your process finished.
	// I think it would only matter if you put() a session that wasn't finished.
	// Need to think about this more.
	//	if err := s.Session.Close(); err != nil && err != io.EOF {
	//		fmt.Printf("sshpool %s c%d s%d close error %v\n", s.host, s.client.clientid, s.sessionid, err)
	//	}

	//	if s.pool.poolconfig.Debug {
	//		fmt.Printf("sshpool %s c%d s%d put\n", s.host, s.client.clientid, s.sessionid)
	//	}

	go func() {
		if s.pool.poolconfig.SessionCloseDelay == 0 {
			time.Sleep(DefaultSessionCloseDelay)
		} else {
			time.Sleep(s.pool.poolconfig.SessionCloseDelay)
		}
		_ = <-s.client.sessions
	}()

	return
}

func New(config *ssh.ClientConfig, poolconfig *PoolConfig) *Pool {
	if poolconfig == nil {
		poolconfig = &PoolConfig{}
	}
	return &Pool{
		config:     config,
		poolconfig: poolconfig,
		clients:    make(map[string][]*client),
		dialing:    make(map[string]chan struct{}),
	}
}

// Get() creates a session to a specific host. If successful, err will be nil
// and you must call Put() on the returned *ssh.Session to ensure cleanup. If
// the host connection already has MaxSessions sessions and MaxConnections is met,
// Get() will block until another connection somewhere calls Put().
func (p *Pool) Get(host string) (*Session, error) {
	// NOTE see also GetSFTP()

	// get_client will already have done a send on client.sessions for us.
	client, sessionid, err := p.get_client(host)
	if err != nil {
		return nil, err
	}

	if p.poolconfig.Debug {
		//fmt.Printf("sshpool %s c%d s%d new session\n", host, client.clientid, sessionid)
	}

	s, err := client.Client.NewSession()
	if err != nil {
		_ = <-client.sessions
		return nil, err
	}

	session := &Session{
		Session:   s,
		sessionid: sessionid,
		pool:      p,
		host:      host,
		client:    client,
	}

	return session, nil
}

// Take care here, there are tricky nested mutex locks.
func (p *Pool) get_client(host string) (*client, int, error) {

	var maxc = p.poolconfig.MaxConnections
	if maxc == 0 {
		maxc = DefaultMaxConnections
	}

	var (
		dialchan  chan struct{}
		sessionid int
	)

retry:

	// if another Get() to the same host is blocked on dial, we want to block
	p.dialing_mu.Lock()
	dialchan = p.dialing[host]
	p.dialing_mu.Unlock()

	if dialchan != nil {
		_, _ = <-dialchan
	}

	var (
		noblock *client
		block   []*client
	)

	// Choose a client that won't block if possible. If not possible, copy out a list of possible
	// clients so we can block outside the lock.
	p.clients_mu.Lock()
	if sessionid == 0 {
		p.nextsessionid++
		sessionid = p.nextsessionid
	}
	for _, client := range p.clients[host] {
		select {
		case client.sessions <- struct{}{}:
			noblock = client
			break
		default:
		}
	}
	if noblock == nil && len(p.clients[host]) == maxc {
		for _, c := range p.clients[host] {
			block = append(block, c)
		}
	}
	p.clients_mu.Unlock()

	if noblock != nil {
		// best case: we found a client and it had a free spot and we have already reserved it.
		return noblock, sessionid, nil
	}

	if block != nil {
		// second best case: we found some full clients and it doesn't look like we should open
		// any new ones. block here until one of the candidates has a free channel.

		// fast case: we only have one candidate
		if len(block) == 1 {
			block[0].sessions <- struct{}{}
			return block[0], sessionid, nil
		}

		// slow case: use the reflect package for a dynamic select
		cases := make([]reflect.SelectCase, len(block))
		for i, b := range block {
			cases[i] = reflect.SelectCase{Dir: reflect.SelectSend, Chan: reflect.ValueOf(b.sessions), Send: reflect.ValueOf(struct{}{})}
		}
		chosen, _, _ := reflect.Select(cases)
		return block[chosen], sessionid, nil
	}

	// now we dial, unless another call to Get() beat us in the race.
	dialchan = make(chan struct{}, 0)
	p.dialing_mu.Lock()
	if p.dialing[host] != nil {
		close(dialchan)
		dialchan = nil
	} else {
		p.dialing[host] = dialchan
	}
	p.dialing_mu.Unlock()

	// failed the race
	if dialchan == nil {
		goto retry
	}

	defer func() {
		p.dialing_mu.Lock()
		delete(p.dialing, host)
		p.dialing_mu.Unlock()
		close(dialchan)
	}()

	var err error

	if p.poolconfig.Debug {
		fmt.Printf("sshpool %s dial\n", host)
	}

	addr := host
	if strings.IndexByte(addr, ':') == -1 {
		addr += ":22"
	}

	config := p.config
	at := strings.IndexByte(addr, '@')
	if at > -1 {
		user := addr[:at]
		addr = addr[at+1:]

		// don't modify the original config structure, just make a shallow copy
		// so we can override the user.
		newconfig := *config
		newconfig.User = user
		config = &newconfig
	}

	sshclient, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, 0, fmt.Errorf("ssh dial %#v: %v", host, err)
	}

	max := p.poolconfig.MaxSessions

	if max == 0 {
		max = DefaultMaxSessions
	}

	c := &client{
		Client:   sshclient,
		sessions: make(chan struct{}, max),
	}

	// reserve first session
	c.sessions <- struct{}{}

	p.clients_mu.Lock()
	p.nextclientid++
	c.clientid = p.nextclientid
	p.clients[host] = append(p.clients[host], c)
	p.clients_mu.Unlock()

	return c, sessionid, nil
}

func (p *Pool) Close() {
	p.clients_mu.Lock()
	defer p.clients_mu.Unlock()

	for host, clients := range p.clients {
		for _, client := range clients {
			client.Client.Close()
		}
		delete(p.clients, host)
		if p.poolconfig.Debug {
			fmt.Printf("sshpool %s close (%d connections)\n", host, len(clients))
		}
	}
}
