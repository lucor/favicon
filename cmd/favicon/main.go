package main

import (
	"context"
	"flag"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/url"
	"os"

	"go.lucor.dev/favicon"
)

func main() {
	debug := flag.Bool("debug", false, "enable diagnostic logging")
	output := flag.String("output", "", "output file path (defaults to favicon.png or favicon.svg)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: favicon [-debug] [-output path] URL")
		os.Exit(2)
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	u, err := url.Parse(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	result, err := favicon.Download(context.Background(), u, favicon.Options{}.WithLogger(logger))
	if err != nil {
		fail(err)
	}
	path := *output
	if path == "" {
		path = "favicon.png"
		if result.Icon.Format == "svg" {
			path = "favicon.svg"
		}
	}
	f, err := os.Create(path)
	if err != nil {
		fail(err)
	}
	var writeErr error
	if result.Icon.Format == "svg" {
		_, writeErr = f.Write(result.Icon.SVG)
	} else if result.Icon.Raster != nil {
		writeErr = png.Encode(f, result.Icon.Raster)
	} else {
		writeErr = fmt.Errorf("unsupported favicon format %q", result.Icon.Format)
	}
	if writeErr != nil {
		_ = f.Close()
		fail(writeErr)
	}
	if err := f.Close(); err != nil {
		fail(err)
	}
	if _, err := io.WriteString(os.Stdout, path+"\n"); err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, "favicon:", err); os.Exit(1) }
