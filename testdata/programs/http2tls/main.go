// Program http2tls serves HTTP/2 over TLS on a loopback socket and fetches
// from it in the same process. Where httpstls covers the handshake, this covers
// what net/http does once ALPN has agreed `h2`: HPACK, the frame layer, several
// streams multiplexed on one connection at once, and a body long enough to need
// more than one DATA frame and a window update to carry it.
//
// net/http's HTTP/2 is the bundled x/net/http2, which the server reaches
// through ServeTLS and the client through a Transport that asks for it.
//
// Nothing printed may depend on the port the kernel picked, on the processor,
// on the key the program generated, or on the order in which concurrent streams
// happen to finish: the concurrent replies are sorted before they are printed.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// bodyLen is longer than one HTTP/2 frame and longer than a stream's initial
// flow-control window, so the transfer needs both continuation frames and a
// window update.
const bodyLen = 100 << 10

func at() time.Time { return time.Unix(1700000000, 0).UTC() }

func main() {
	cert, err := selfSigned()
	if err != nil {
		fmt.Println("certificate:", err)
		return
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		fmt.Fprintf(w, "hello %s\n", r.URL.Query().Get("who"))
	})
	mux.HandleFunc("/long", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := w.Write(pattern(bodyLen)); err != nil {
			fmt.Println("write long body:", err)
		}
	})
	mux.HandleFunc("/digest", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.New()
		n, err := io.Copy(sum, r.Body)
		if err != nil {
			fmt.Println("read request body:", err)
		}
		fmt.Fprintf(w, "%d %x", n, sum.Sum(nil))
	})
	srv := &http.Server{
		Handler:  mux,
		ErrorLog: log.New(io.Discard, "", 0),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			Time:         at,
		},
	}
	// ServeTLS, not Serve over a tls.Listener: it is ServeTLS that puts `h2`
	// in NextProtos and registers the HTTP/2 handler for it.
	go func() { _ = srv.ServeTLS(ln, "", "") }()

	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, Time: at},
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: tr}
	base := "https://" + ln.Addr().String()

	get(client, base+"/who?who=rustygo")

	// Four streams at once on the one connection, which is what HTTP/2 is for.
	var wg sync.WaitGroup
	lines := make([]string, 4)
	for i := range lines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lines[i] = describe(client, fmt.Sprintf("%s/who?who=stream%d", base, i))
		}(i)
	}
	wg.Wait()
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}

	// A response long enough to be framed and flow-controlled, reported by its
	// length and digest rather than its bytes.
	resp, err := client.Get(base + "/long")
	if err != nil {
		fmt.Println("get long:", unport(err))
		return
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		fmt.Println("read long:", err)
		return
	}
	fmt.Printf("long %s %d %x\n", resp.Proto, len(body), sha256.Sum256(body))

	// And a request body of the same size, which the handler digests.
	resp, err = client.Post(base+"/digest", "application/octet-stream", strings.NewReader(string(pattern(bodyLen))))
	if err != nil {
		fmt.Println("post:", unport(err))
		return
	}
	echo, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		fmt.Println("read digest:", err)
		return
	}
	fmt.Printf("digest %s %s\n", resp.Proto, echo)

	tr.CloseIdleConnections()
	if err := srv.Close(); err != nil {
		fmt.Println("close:", err)
	}
	fmt.Println("done")
}

// pattern is a body whose every byte is decided by its position, so both ends
// can agree on it without either sending it to the other.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

// selfSigned makes the server's key pair the same way httpstls does: a P-256
// key from crypto/rand, a certificate that signs itself and names the loopback
// address, out through PEM and back through tls.X509KeyPair. The dates and the
// serial number are fixed so that nothing about the certificate differs between
// runs; the key itself is fresh, which is why nothing about it is printed.
func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rustygo test"},
		NotBefore:             time.Unix(1600000000, 0).UTC(),
		NotAfter:              time.Unix(4000000000, 0).UTC(),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

func get(c *http.Client, url string) { fmt.Println(describe(c, url)) }

// describe returns one line about a response, so a concurrent caller can hold
// it until the order is settled.
func describe(c *http.Client, url string) string {
	resp, err := c.Get(url)
	if err != nil {
		return "get: " + unport(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "read body: " + err.Error()
	}
	return fmt.Sprintf("%d proto=%s served=%s tls=%s body=%q",
		resp.StatusCode, resp.Proto, resp.Header.Get("X-Proto"),
		tls.VersionName(resp.TLS.Version), body)
}

// unport hides the port the kernel picked, which a transport error quotes as
// part of the URL it was given.
func unport(err error) string {
	s := err.Error()
	if i := strings.Index(s, "127.0.0.1:"); i >= 0 {
		if j := strings.IndexAny(s[i+len("127.0.0.1:"):], "/\" "); j >= 0 {
			return s[:i] + "127.0.0.1:PORT" + s[i+len("127.0.0.1:")+j:]
		}
	}
	return s
}
