package sshpool

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var Debug bool

type Pool struct {
	config *ssh.ClientConfig

	clients_mu sync.Mutex
	clients    map[string]*ssh.Client

	dialing_mu sync.Mutex
	dialing    map[string]chan struct{}

	sessions_mu sync.Mutex
	sessions    map[string][]Session
}

type Session struct {
	*ssh.Session
	pool *Pool
	host string
}

func (s *Session) Close() error {
	s.pool.sessions_mu.Lock()
	ss := s.pool.sessions
	for idx, s1 := range ss[s.host] {
		if s1.Session == s.Session {
			ss[s.host] = append(ss[s.host][:idx], ss[s.host][idx+1:]...)
			//log.Printf("close %-20s %p %d", s.host, s.Session, len(ss[s.host]))
			break
		}
	}
	s.pool.sessions = ss
	s.pool.sessions_mu.Unlock()

	err := s.Session.Close()
	//if err != nil && err != io.EOF {
	//	log.Printf("fu %v %p", err, s.Session)
	//}
	return err
}

func New(config *ssh.ClientConfig) *Pool {
	return &Pool{
		config:   config,
		clients:  make(map[string]*ssh.Client),
		dialing:  make(map[string]chan struct{}),
		sessions: make(map[string][]Session),
	}
}

// Get() creates a session to a specific host. If successful, err will be nil
// and you must call Close() on the returned *ssh.Session to ensure cleanup.
func (p *Pool) Get(host string) (*Session, error) {

	client, err := p.get_client(host)
	if err != nil {
		return nil, err
	}

	var s *ssh.Session
	// 5 seconds
	for tries := 0; tries < 500; tries++ {
		s, err = client.NewSession()
		if err == nil {
			p.sessions_mu.Lock()
			p.sessions[host] = append(p.sessions[host], Session{s, p, host})
			//log.Printf("open %-20s %p %d", host, s, len(p.sessions[host]))
			p.sessions_mu.Unlock()

			return &Session{s, p, host}, nil
		}

		// some other error? return it
		if !strings.Contains(err.Error(), "administratively prohibited (open failed)") {
			return nil, err
		}
		// sleep for a bit and try again, we probably hit MaxSessions
		time.Sleep(10 * time.Millisecond)
	}
	return nil, err
}

// Take care here, there are tricky nested mutex locks.
func (p *Pool) get_client(host string) (*ssh.Client, error) {

	var (
		dialchan chan struct{}
		client   *ssh.Client
	)

retry:

	// if another Get() to the same host is blocked on dial, we want to block
	p.dialing_mu.Lock()
	dialchan = p.dialing[host]
	p.dialing_mu.Unlock()

	if dialchan != nil {
		_, _ = <-dialchan
	}

	p.clients_mu.Lock()
	client = p.clients[host]
	p.clients_mu.Unlock()

	// best case
	if client != nil {
		return client, nil
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

	if Debug {
		fmt.Println("ssh open", host)
	}
	client, err = ssh.Dial("tcp", host+":22", p.config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %#v: %v", host, err)
	}

	p.clients_mu.Lock()
	p.clients[host] = client
	p.clients_mu.Unlock()

	return client, nil
}

func (p *Pool) Close() {
	// take care here so we don't deadlock: copy clients so we can release the lock
	clients := make(map[string]*ssh.Client)
	p.clients_mu.Lock()
	for host, client := range p.clients {
		clients[host] = client
		delete(p.clients, host)
	}
	p.clients_mu.Unlock()

	for host, client := range clients {
		if Debug {
			fmt.Println("ssh quit", host)
		}
		client.Close()
	}
}
