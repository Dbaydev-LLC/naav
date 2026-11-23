// naavd is the Naav daemon: a tiny local URL alias router.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	defPort := 4747
	if v := os.Getenv("NAAV_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			log.Fatalf("naavd: invalid NAAV_PORT %q", v)
		}
		defPort = p
	}
	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "install":
			err = runInstall(os.Args[2:], defPort)
		case "uninstall":
			err = runUninstall()
		default:
			goto serve
		}
		if err != nil {
			log.Fatalf("naavd: %v", err)
		}
		return
	}
serve:
	port := flag.Int("port", defPort, "port to listen on (127.0.0.1 only); env NAAV_PORT")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("naavd", version)
		return
	}

	dir := os.Getenv("NAAV_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("naavd: %v", err)
		}
		dir = filepath.Join(home, ".naav")
	}

	store, err := OpenStore(filepath.Join(dir, "aliases.bin"))
	if err != nil {
		log.Fatalf("naavd: %v", err)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatalf("naavd: %v", err)
	}
	srv := &http.Server{
		Handler:           NewServer(store),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("naavd: %v", err)
		}
	}()
	log.Printf("naavd %s listening on http://%s", version, ln.Addr())

	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	if err := store.Close(); err != nil {
		log.Printf("naavd: final save failed: %v", err)
	}
}
