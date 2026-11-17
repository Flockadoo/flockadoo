// Package fakeengine is not written yet: plan item A2, tests first. Every
// function is a stub, so the tests fail because the fake does nothing.
package fakeengine

import (
	"errors"
	"testing"
	"time"
)

type Flavour string

type Engine struct{ URL string }

type Option func(*Engine)

func Model(string) Option                    { return func(*Engine) {} }
func MaxModelLen(int64) Option               { return func(*Engine) {} }
func Version(string) Option                  { return func(*Engine) {} }
func Embedding() Option                      { return func(*Engine) {} }
func VLLM(testing.TB, ...Option) *Engine     { return &Engine{} }
func LlamaCpp(testing.TB, ...Option) *Engine { return &Engine{} }

func (e *Engine) Flavour() Flavour              { return "" }
func (e *Engine) Next(...Response)              {}
func (e *Engine) SetVersion(string)             {}
func (e *Engine) SetReady(bool)                 {}
func (e *Engine) SetMetrics(int32, float64)     {}
func (e *Engine) Requests() []Request           { return nil }
func (e *Engine) InFlight() int                 { return 0 }
func (e *Engine) MaxInFlight() int              { return 0 }
func (e *Engine) AwaitIdle(testing.TB)          {}
func (e *Engine) AwaitInFlight(testing.TB, int) {}

type Request struct {
	Path      string
	Body      []byte
	Cancelled bool
}

type Usage struct{ PromptTokens, CompletionTokens int }

type Response struct{}

func Complete(string, Usage) Response          { return Response{} }
func Stream(...string) Response                { return Response{} }
func Replay(testing.TB, string) Response       { return Response{} }
func InvalidJSON() Response                    { return Response{} }
func MalformedSSE() Response                   { return Response{} }
func NoUsage(...string) Response               { return Response{} }
func InflatedUsage(int, ...string) Response    { return Response{} }
func Status(int, string) Response              { return Response{} }
func Hang() Response                           { return Response{} }
func Crash() Response                          { return Response{} }
func (r Response) Slow(time.Duration) Response { return r }
func (r Response) Partial(int) Response        { return r }
func (r Response) Stall(int) Response          { return r }

func CorpusDir() string   { return "" }
func VerifyCorpus() error { return errors.New("no corpus yet") }
