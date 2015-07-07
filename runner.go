package bruto

import (
	"fmt"
	"io"
	"log"
	"time"

	"github.com/dullgiulio/bruto/backend/typo3"
	"github.com/dullgiulio/bruto/gen"
)

type broken chan gen.Login

func makeBroken() broken {
	return broken(make(chan gen.Login))
}

func (b broken) writeTo(w io.Writer) {
	for l := range b {
		fmt.Fprintf(w, "BROKEN: %s\n", &l)
	}
}

type Runner struct {
	// Target host domain
	host string
	// Receiver of session worker events
	sessions chan error
	// Signal that the login pair generator has finished
	pwdOver chan struct{}
	// Login pair generator
	logins *gen.Logins
	// Generate user agent strings
	agents gen.Agents
	// Receiver for successful login attempts
	broken broken
	// Pool of session workers
	pool pool
}

func NewRunner(host string) *Runner {
	return &Runner{
		host:     host,
		sessions: make(chan error),
		pwdOver:  make(chan struct{}),
		broken:   makeBroken(),
		logins:   gen.NewLogins(),
		agents:   gen.NewAgents(),
		pool:     newPool(),
	}
}

// Utility to create a new session
func (r *Runner) makeSession() {
	// TODO: Type comes from backend provier according to string name
	s := newSession(typo3.New(), r.host, r.sessions, r.logins.Chan(), r.agents.Chan(), r.broken)
	go s.run()
}

func (r *Runner) generateLogins() {
	r.logins.Generate()
	// Signal that we have no more passwords to try
	r.pwdOver <- struct{}{}
	close(r.pwdOver)
}

func (r *Runner) startWorkers(n int) {
	// Make some sessions to start
	for i := 0; i < n; i++ {
		r.makeSession()
	}
}

func (r *Runner) Run(w io.Writer, workers int) {
	var noPwd bool
	// TODO: This comes from flags
	if err := r.logins.Load("usernames.txt", "passwords.txt"); err != nil {
		log.Printf("Error: %s", err)
		return
	}
	// Generate username/password pairs and signal when there are no more
	go r.generateLogins()
	// Generate random user-agent strings forever
	go r.agents.Generate()
	// Print broken login pairs to stdout
	go r.broken.writeTo(w)
	// Start some workers
	r.startWorkers(workers)
	for {
		select {
		case s := <-r.sessions:
			if _, ok := s.(*sessionError); !ok {
				log.Printf("Error: %s", s)
				break
			}
			se := s.(*sessionError)
			if se.ready() {
				log.Printf("Starting attempt...")
				// Sets the time for future deltas
				r.pool.add(se.s)
				break
			}
			if se.attempt() {
				t := r.pool[se.s]
				d := time.Now().Sub(t)
				log.Printf("Attempt took: %s", &d)
				break
			}
			if !se.finished() {
				log.Printf("Error: %s", s)
				// For not return if the error is at initialization
				if t, ok := r.pool[se.s]; !ok || t.IsZero() {
					return
				}
			}
			// Remove a worker from the pool if it had an error and it's dead
			r.pool.del(se)
			// If no more sessions are working
			if !r.pool.alive() {
				// If we finished the passwords to try, exit
				if noPwd {
					close(r.broken)
					return
				}
				// Start up some more sessions to finish the logins
				r.startWorkers(workers)
			}
		case <-r.pwdOver:
			// No more passwords to try, just wait for all
			// the sessions to finish their attemps.
			noPwd = true
		}
	}
}
