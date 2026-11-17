// Package fakeplatform is not written yet: plan item A2, tests first. Every
// function is a stub, so the tests fail because the fake does nothing.
package fakeplatform

import (
	"net/http"
	"testing"
)

type Platform struct{ URL string }

type Option func(*Platform)

func AllowContractErrors() Option { return func(*Platform) {} }

func New(testing.TB, string, ...Option) *Platform { return &Platform{} }

type Reply struct{}

func JSON(int, any) Reply                  { return Reply{} }
func Hang() Reply                          { return Reply{} }
func NodeRemoved() Reply                   { return Reply{} }
func UpgradeRequired(string, string) Reply { return Reply{} }

type Request struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

func (p *Platform) Set(string, string, Reply)                               {}
func (p *Platform) Next(string, string, ...Reply)                           {}
func (p *Platform) Requests(string, string) []Request                       { return nil }
func (p *Platform) AwaitRequests(testing.TB, string, string, int) []Request { return nil }
func (p *Platform) ContractErrors() []string                                { return nil }
