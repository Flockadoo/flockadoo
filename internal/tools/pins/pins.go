package main

import (
	"context"
	"io"
	"net/http"
)

type options struct {
	Root      string
	API       string
	Download  string
	Client    *http.Client
	UVVersion string
	LlamaTag  string
	Compile   func(ctx context.Context, root string) error
	Out       io.Writer
}

func update(ctx context.Context, o options) error { return nil }
