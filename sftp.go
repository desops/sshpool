package sshpool

import (
	"fmt"
	"time"

	"github.com/pkg/sftp"
)

type SFTPSession struct {
	*sftp.Client
	pool      *Pool
	client    *client
	host      string
	sessionid int
}

func (s *SFTPSession) String() string {
	return fmt.Sprintf("sftp session %d host %s", s.sessionid, s.host)
}

func (p *Pool) GetSFTP(host string) (*SFTPSession, error) {
	client, sessionid, err := p.get_client(host)
	if err != nil {
		return nil, err
	}

	s, err := sftp.NewClient(client.Client)
	if err != nil {
		_ = <-client.sessions
		return nil, err
	}

	session := &SFTPSession{
		Client:    s,
		sessionid: sessionid,
		pool:      p,
		host:      host,
		client:    client,
	}

	return session, nil
}

func (s *SFTPSession) Put() {
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
