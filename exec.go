package pool

import (
	"fmt"
	"io"
	"log"
)

const (
	context_start = 512
	context_end   = 512
)

func (p *Pool) ExecCombinedOutput(host string, command string) ([]byte, error) {
	session, err := p.Get(host)
	if err != nil {
		return nil, fmt.Errorf("Error executing on host %s command %#v: %v", host, command, err)
	}

	out_b, err := session.CombinedOutput(command)
	err2 := session.Close()
	if err2 != nil && io.EOF != err2 {
		log.Printf("ssh_pool: close failed %v", err2)
		if err == nil {
			err = err2
		}
	}
	return output_error_wrapper(out_b, err, host, command)
}

func (p *Pool) ExecOutput(host string, command string) ([]byte, error) {
	session, err := p.Get(host)
	if err != nil {
		return nil, fmt.Errorf("Error executing on host %s command %#v: %v", host, command, err)
	}
	defer session.Close()

	out_b, err := session.Output(command)
	return output_error_wrapper(out_b, err, host, command)
}

func output_error_wrapper(out_b []byte, err error, host, command string) ([]byte, error) {
	if err != nil {
		// give some nice context to the error message
		msg := err.Error()
		if out_b != nil && len(out_b) > 0 {
			out_s := string(out_b)
			if len(out_s) > context_start+context_end {
				msg += ": " + out_s[:context_start] + " ... (trimmed output) ... " + out_s[len(out_s)-context_end:]
			} else {
				msg += ": " + out_s
			}
		}
		err = fmt.Errorf("Error executing on host %s command %#v: %s", host, command, msg)
	}
	return out_b, err
}

func (p *Pool) ExecCombinedOutputString(host string, command string) (string, error) {
	b, err := p.ExecCombinedOutput(host, command)
	s := ""
	if b != nil {
		s = string(b)
	}
	return s, err
}

func (p *Pool) ExecOutputString(host string, command string) (string, error) {
	b, err := p.ExecOutput(host, command)
	s := ""
	if b != nil {
		s = string(b)
	}
	return s, err
}
