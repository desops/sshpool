package pool

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
)

type Tunnel struct {
	listener net.Listener
	host     string
	remote   string
	pool     *Pool
}

// Tunnel() creates an SSH tunnel to host. A local TCP socket will listen on local. Any connections
// will be proxied to remote via host. Be sure to call Close() to clean up.
func (p *Pool) Tunnel(host string, local, remote string) (*Tunnel, error) {

	listener, err := net.Listen("tcp", local)
	if err != nil {
		return nil, err
	}

	tunnel := &Tunnel{
		listener: listener,
		host:     host,
		remote:   remote,
		pool:     p,
	}

	go tunnel.accept()

	return tunnel, nil
}

func (tunnel *Tunnel) Close() error {
	return tunnel.listener.Close()
}

func (tunnel *Tunnel) Addr() string {
	return tunnel.listener.Addr().String()
}

func (tunnel *Tunnel) accept() {
	for {
		conn, err := tunnel.listener.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				// probably a graceful shutdown
				return
			}
			log.Println("tunnel listener accept:", err)
			return
		}

		go tunnel.forward(conn)
	}
}

func (tunnel *Tunnel) forward(local net.Conn) {
	defer local.Close()

	client, err := tunnel.pool.get_client(tunnel.host)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.Close()

	remote, err := client.Dial("tcp", tunnel.remote)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer remote.Close()

	go func() {
		defer client.Close() // hopefully it's ok if we double-close
		defer remote.Close()

		_, err := io.Copy(remote, local)
		if err != nil {
			log.Println("copy remote, local:", err)
			return
		}
	}()

	_, err = io.Copy(local, remote)
	if err != nil {
		log.Println("copy local, remote:", err)
	}

	return
}
