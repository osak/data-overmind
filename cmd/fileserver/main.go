package main

import (
	"data-overmind/internal/fileserver"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

const defaultReadHeaderTimeout = 10 * time.Second

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	flag.Parse()

	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: fileserver [-addr host:port]")
		os.Exit(2)
	}

	root, err := os.Getwd()
	if err != nil {
		slog.Error("determine working directory", "error", err)
		os.Exit(1)
	}

	handler, err := fileserver.New(root)
	if err != nil {
		slog.Error("create file server", "error", err)
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		slog.Error("listen", "address", *addr, "error", err)
		os.Exit(1)
	}

	url := listenerURL(listener.Addr())
	fmt.Printf("Serving %s at %s\n", root, url)

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}

func listenerURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
