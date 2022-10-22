/*
 * ShiTTYSSHClient
 * A just-working way to wrap commands and tote their stdin/stdout streams
 * to various TTYs and processes
 *
 * Copyright 2022, Travis Biehn
 * All rights reserved.
 *
 * This source code is licensed under the MIT license found in the
 * LICENSE file in the root directory of this source tree.
 *
 */

package main

import (
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"

	"github.com/anmitsu/go-shlex"
)

var e = log.New(os.Stderr, "[ShiTTY] ", 0)
var r = log.New(os.Stderr, "[ShiTTY] ", 0)

var connWrite, termInput = ConPipe()
var termOutputStd, connStdRead = ConPipe()
var termOutputErr, connErrRead = ConPipe()

func main() {
	command := flag.String("cmd", "nc -l 2323", "Command to run")
	listenAddr := flag.String("listen", "127.0.0.1:2222", "Which port to serve.")
	keepStdErr := flag.Bool("stderr", true, "Keep cmd's stderr here.")

	flag.Parse()

	listen, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatal(err)
		os.Exit(1)
	}

	lexedCmd, err := shlex.Split(*command, true)
	if err != nil {
		log.Fatal(err)
	}

	cmd := exec.Command(lexedCmd[0], lexedCmd[1:]...)

	cmd.Stdin = connWrite
	if *keepStdErr {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = connErrRead
	}
	cmd.Stdout = connStdRead

	err = cmd.Start()

	if err != nil {
		e.Fatal(err)
	}

	e.Println("Command executed.")

	// close listener
	defer listen.Close()
	for {
		e.Println("Accepting commanding clients on", *listenAddr)

		conn, err := listen.Accept()
		e.Println("New connection on", conn.LocalAddr(), "from", conn.RemoteAddr())
		if err != nil {
			log.Fatal(err)
			os.Exit(1)
		}

		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			io.Copy(conn, termOutputStd)

			wg.Done()
		}()

		if !*keepStdErr {
			wg.Add(1)
			go func() {
				io.Copy(conn, termOutputErr)

				wg.Done()

			}()
		}

		io.Copy(termInput, conn)
		conn.Close()
		//Force the other ones to close.
		connStdRead.Write([]byte{0x00})
		if !*keepStdErr {
			connErrRead.Write([]byte{0x00})
		}
		wg.Wait()

		e.Println("Closed connection from", conn.RemoteAddr())
	}
	cmd.Wait()
}

//io.Pipe code.

// onceError is an object that will only store an error once.
type onceError struct {
	sync.Mutex // guards following
	err        error
}

func (a *onceError) Store(err error) {
	a.Lock()
	defer a.Unlock()
	if a.err != nil {
		return
	}
	a.err = err
}
func (a *onceError) Load() error {
	a.Lock()
	defer a.Unlock()
	return a.err
}

// ErrClosedPipe is the error used for read or write operations on a closed pipe.
var ErrClosedPipe = errors.New("io: read/write on closed pipe")

// A pipe is the shared pipe structure underlying PipeReader and PipeWriter.
type pipe struct {
	wrMu sync.Mutex // Serializes Write operations
	wrCh chan []byte
	rdCh chan int

	once sync.Once // Protects closing done
	done chan struct{}
	rerr onceError
	werr onceError
}

func (p *pipe) Read(b []byte) (n int, err error) {
	select {
	case <-p.done:
		return 0, p.readCloseError()
	default:
	}

	select {
	case bw := <-p.wrCh:
		nr := copy(b, bw)
		p.rdCh <- nr
		return nr, nil
	case <-p.done:
		return 0, p.readCloseError()
	}
}

func (p *pipe) readCloseError() error {
	rerr := p.rerr.Load()
	if werr := p.werr.Load(); rerr == nil && werr != nil {
		return werr
	}
	return ErrClosedPipe
}

func (p *pipe) CloseRead(err error) error {
	if err == nil {
		err = ErrClosedPipe
	}
	p.rerr.Store(err)
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *pipe) Write(b []byte) (n int, err error) {
	select {
	case <-p.done:
		return 0, p.writeCloseError()
	default:
		p.wrMu.Lock()
		defer p.wrMu.Unlock()
	}

	for once := true; once || len(b) > 0; once = false {
		select {
		case p.wrCh <- b:
			nw := <-p.rdCh
			b = b[nw:]
			n += nw
		case <-p.done:
			return n, p.writeCloseError()
		}
	}
	return n, nil
}

func (p *pipe) writeCloseError() error {
	werr := p.werr.Load()
	if rerr := p.rerr.Load(); werr == nil && rerr != nil {
		return rerr
	}
	return ErrClosedPipe
}

func (p *pipe) CloseWrite(err error) error {
	if err == nil {
		err = io.EOF
	}
	p.werr.Store(err)
	p.once.Do(func() { close(p.done) })
	return nil
}

// A PipeReader is the read half of a pipe.
type ConReader struct {
	p *pipe
}

// Read implements the standard Read interface:
// it reads data from the pipe, blocking until a writer
// arrives or the write end is closed.
// If the write end is closed with an error, that error is
// returned as err; otherwise err is EOF.
func (r *ConReader) Read(data []byte) (n int, err error) {
	return r.p.Read(data)
}

// Close closes the reader; subsequent writes to the
// write half of the pipe will return the error ErrClosedPipe.
func (r *ConReader) Close() error {
	return r.CloseWithError(nil)
}

// CloseWithError closes the reader; subsequent writes
// to the write half of the pipe will return the error err.
//
// CloseWithError never overwrites the previous error if it exists
// and always returns nil.
func (r *ConReader) CloseWithError(err error) error {
	return r.p.CloseRead(err)
}

// A PipeWriter is the write half of a pipe.
type ConWriter struct {
	p *pipe
}

// Write implements the standard Write interface:
// it writes data to the pipe, blocking until one or more readers
// have consumed all the data or the read end is closed.
// If the read end is closed with an error, that err is
// returned as err; otherwise err is ErrClosedPipe.
func (w *ConWriter) Write(data []byte) (n int, err error) {
	return w.p.Write(data)
}

// Close closes the writer; subsequent reads from the
// read half of the pipe will return no bytes and EOF.
func (w *ConWriter) Close() error {
	return w.CloseWithError(nil)
}

// CloseWithError closes the writer; subsequent reads from the
// read half of the pipe will return no bytes and the error err,
// or EOF if err is nil.
//
// CloseWithError never overwrites the previous error if it exists
// and always returns nil.
func (w *ConWriter) CloseWithError(err error) error {
	return w.p.CloseWrite(err)
}

// Pipe creates a synchronous in-memory pipe.
// It can be used to connect code expecting an io.Reader
// with code expecting an io.Writer.
//
// Reads and Writes on the pipe are matched one to one
// except when multiple Reads are needed to consume a single Write.
// That is, each Write to the PipeWriter blocks until it has satisfied
// one or more Reads from the PipeReader that fully consume
// the written data.
// The data is copied directly from the Write to the corresponding
// Read (or Reads); there is no internal buffering.
//
// It is safe to call Read and Write in parallel with each other or with Close.
// Parallel calls to Read and parallel calls to Write are also safe:
// the individual calls will be gated sequentially.
func ConPipe() (*ConReader, *ConWriter) {
	p := &pipe{
		wrCh: make(chan []byte),
		rdCh: make(chan int),
		done: make(chan struct{}),
	}
	return &ConReader{p}, &ConWriter{p}
}
