// Package fakegateway is not written yet: plan item A2, tests first. Every
// function is a stub, so the tests fail because the fake does nothing.
package fakegateway

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

type Gateway struct{ URL string }

type Dial struct {
	At                           time.Time
	Status                       int
	Authorization, ClientVersion string
}

func New(testing.TB, string) *Gateway { return &Gateway{} }

func (g *Gateway) AwaitSession(testing.TB)    {}
func (g *Gateway) AwaitDials(testing.TB, int) {}
func (g *Gateway) Connected() bool            { return false }
func (g *Gateway) Dials() []Dial              { return nil }
func (g *Gateway) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("not written yet")
}
func (g *Gateway) Drop()                  {}
func (g *Gateway) Replace()               {}
func (g *Gateway) Blackhole()             {}
func (g *Gateway) RejectNext(int, string) {}
