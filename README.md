# sshpool
Connection pool for x/crypto/ssh connections

API docs: https://pkg.go.dev/github.com/desops/sshpool

This is a connection pooler for golang.org/x/crypto/ssh. It's good for making hundreds (or thousands) of SSH connections to do a lot of things on a lot of hosts.

The pool itself has no configuration apart from a ClientConfig:

```go
package main

import (
	"os"
	"ioutil"

	"golang.org/x/crypto/ssh"
	"github.com/desops/sshpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
   buf, err := ioutil.ReadFile("./keys/my_private_ssh_key")
   if err != nil {
   	return err
   }

   key, err := ssh.ParsePrivateKey(buf)
   if err != nil {
   	return err
   }

	config := &ssh.ClientConfig{
		User: "myuser",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(key),
		},
	}

	pool := sshpool.New(config)

	for i := 0; i < 100; i++ {
		go func() {
			err := func() error {
				session, err := pool.Get("myhost")
				if err != nil {
					return err
				}
				defer session.Close() // important: this returns it to the pool

				session.Stdout = os.Stdout
				session.Stderr = os.Stderr

				if err := session.Run("sleep 10"); err != nil {
					ee, ok := err.(*ssh.ExitError)
					if ok {
						return fmt.Errorf("remote command exit status %d", ee.ExitStatus())
					}
					return err
				}
				return nil
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
	}

	return nil
}
```
