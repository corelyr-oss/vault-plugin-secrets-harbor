// Command fakeharbor runs the in-memory Harbor fake on a fixed address for
// manual testing of the plugin against a Vault dev server.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor/harbortest"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8089", "listen address")
	flag.Parse()

	s := harbortest.New()
	s.Close() // discard the random-port listener; re-serve on addr
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: s.Config.Handler, ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("fake harbor listening on http://%s (admin / Harbor12345)\n", ln.Addr())
	go func() { _ = srv.Serve(ln) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	_ = srv.Close()
}
