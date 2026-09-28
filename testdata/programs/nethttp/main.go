// Program nethttp serves HTTP on a loopback socket and fetches from it in the
// same process, which is net/http, the netpoller and the scheduler end to
// end: an accept loop, a handler, a client that writes a request and parses
// the response, and a connection kept alive between two requests.
//
// Nothing printed may depend on the port the kernel picked.
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Who", r.URL.Query().Get("who"))
		fmt.Fprintf(w, "hello %s from %s\n", r.URL.Query().Get("who"), r.Method)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fmt.Println("read request body:", err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "echo %d %s", len(body), strings.ToUpper(string(body)))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	base := "http://" + ln.Addr().String()
	get(base + "/hello?who=rustygo")
	// The same connection again, now that it is idle and kept alive.
	get(base + "/hello?who=again")
	post(base+"/echo", "a body")
	get(base + "/missing")
	if err := srv.Close(); err != nil {
		fmt.Println("close:", err)
	}
	fmt.Println("done")
}

func get(url string) {
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	show(resp)
}

func post(url, body string) {
	resp, err := http.Post(url, "text/plain", strings.NewReader(body))
	if err != nil {
		fmt.Println("post:", err)
		return
	}
	show(resp)
}

// show prints what the server decided, and nothing the transport or the clock
// decided.
func show(resp *http.Response) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("read body:", err)
		return
	}
	fmt.Printf("%d proto=%s who=%q type=%s len=%d body=%q\n",
		resp.StatusCode, resp.Proto, resp.Header.Get("X-Who"),
		resp.Header.Get("Content-Type"), len(body), body)
}
