// Program httpstls serves HTTPS on a loopback socket and fetches from it in
// the same process. Where nethttp covers the netpoller and the scheduler, this
// covers the handshake: ML-KEM and X25519 key agreement, ECDSA over P-256,
// SHA-2 and SHA-3, HKDF, AEAD record protection, and certificate verification
// against a pool the program fills itself — all of it the standard library's
// own crypto, compiled.
//
// Five requests, each a different path through crypto/tls: a TLS 1.3
// handshake, a second request over the same kept-alive connection, a POST with
// a body the handler reads back, a fresh connection that resumes from the
// ticket the server sealed, and a TLS 1.2 handshake, whose key exchange, PRF
// and record layer are a different half of the package. Then one connection
// more, refused because its client trusts nobody, which is what says the
// verification on the others is real rather than skipped.
//
// The certificate is made on the spot, so the program needs no files: a P-256
// key from crypto/rand, a self-signed certificate over it, and the PEM pair put
// back together by tls.X509KeyPair, which is x509's writer and its parser both.
// Nothing about the key is printed, because a fresh one is different every run.
//
// Nothing printed may depend on the port the kernel picked, nor on the
// processor: rustygo's internal/cpu reports no features, so a cipher suite
// chosen for AES hardware is not the one gc picks here, and the suite is
// therefore not printed. The certificate's validity is judged at a fixed
// instant, so the program does not go stale.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
	"strings"
	"time"
)

// at is the instant both ends judge the certificate's validity by, so the
// program's output never depends on today's date.
func at() time.Time { return time.Unix(1700000000, 0).UTC() }

func main() {
	cert, err := selfSigned()
	if err != nil {
		fmt.Println("certificate:", err)
		return
	}
	leaf := cert.Leaf
	fmt.Printf("certificate %q for %v, ca=%v\n", leaf.Subject.CommonName, leaf.IPAddresses, leaf.IsCA)

	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		fmt.Printf("certificate holds a %T, not an ECDSA key\n", leaf.PublicKey)
		return
	}
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		fmt.Printf("the key pair holds a %T\n", cert.PrivateKey)
		return
	}
	fmt.Println("key matches certificate:", pub.Equal(&key.PublicKey))
	if err := leaf.CheckSignatureFrom(leaf); err != nil {
		fmt.Println("self signature:", err)
		return
	}
	fmt.Println("self signature verifies")

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Who", r.URL.Query().Get("who"))
		fmt.Fprintf(w, "hello %s over %s\n", r.URL.Query().Get("who"), tls.VersionName(r.TLS.Version))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fmt.Println("read request body:", err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "echo %d %s", len(body), strings.ToUpper(string(body)))
	})
	// The refused handshake below is one the server reports on its error log,
	// with the client's port and the time of day in it. The program says what
	// the client saw instead, so the log goes nowhere.
	srv := &http.Server{Handler: mux, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		_ = srv.Serve(tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			Time:         at,
		}))
	}()

	addr := ln.Addr().String()
	// TLS 1.3, then the same connection again now that it is idle and kept
	// alive, then a body the handler reads back.
	c13, tr13 := client(pool, tls.VersionTLS13)
	get(c13, "https://"+addr+"/hello?who=rustygo")
	get(c13, "https://"+addr+"/hello?who=again")
	post(c13, "https://"+addr+"/echo", "a body")
	// A fresh connection, which resumes from the ticket the server sealed
	// after the first handshake instead of agreeing a key again.
	tr13.CloseIdleConnections()
	get(c13, "https://"+addr+"/hello?who=resumed")
	// And TLS 1.2, whose key exchange, PRF and record layer are a different
	// path through the same packages.
	c12, _ := client(pool, tls.VersionTLS12)
	get(c12, "https://"+addr+"/hello?who=twelve")

	// A client that trusts nobody must be turned away, which is what says the
	// verification above happened. Dialed without net/http, so the message does
	// not carry the port the kernel picked, and against an empty pool rather
	// than no pool: no pool means the machine's own certificate store, which is
	// neither this program's business nor quick to read.
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: x509.NewCertPool(), Time: at})
	if err == nil {
		conn.Close()
		fmt.Println("untrusted: the handshake was accepted")
	} else {
		fmt.Println("untrusted:", err)
	}

	if err := srv.Close(); err != nil {
		fmt.Println("close:", err)
	}
	fmt.Println("done")
}

// selfSigned makes the server's key pair: a P-256 key from crypto/rand and a
// certificate that signs itself, naming the loopback address the client will
// dial. It goes out through PEM and comes back through tls.X509KeyPair rather
// than being assembled by hand, so the DER the program writes is also DER it
// reads: x509.CreateCertificate marshals the certificate through encoding/asn1,
// MarshalPKCS8PrivateKey marshals the key, and X509KeyPair parses both again and
// checks that they belong together.
//
// The dates are fixed, as `at` is, so the certificate is the same age on every
// run. The serial number is fixed for the same reason — nothing here is a real
// certificate authority, and a random serial would be printed nowhere but would
// still make the DER differ run to run.
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
	// X509KeyPair parses the leaf to check it against the key but does not keep
	// it, and both the server's own name matching and this program's pool want
	// it parsed.
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

// client returns an HTTPS client pinned to one TLS version, trusting pool and
// nothing else.
//
// It has no Timeout, deliberately: a handshake under GC torture takes tens of
// seconds, and a deadline would make what the program prints depend on how
// loaded the machine was. The harness's own run cap is what catches a program
// that has stopped.
func client(pool *x509.CertPool, version uint16) (*http.Client, *http.Transport) {
	tr := &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:            pool,
		MinVersion:         version,
		MaxVersion:         version,
		Time:               at,
		ClientSessionCache: tls.NewLRUClientSessionCache(4),
	}}
	return &http.Client{Transport: tr}, tr
}

func get(c *http.Client, url string) {
	resp, err := c.Get(url)
	if err != nil {
		fmt.Println("get:", unport(err))
		return
	}
	show(resp)
}

func post(c *http.Client, url, body string) {
	resp, err := c.Post(url, "text/plain", strings.NewReader(body))
	if err != nil {
		fmt.Println("post:", unport(err))
		return
	}
	show(resp)
}

// show prints what the server and the handshake decided, and nothing the
// processor or the clock decided.
func show(resp *http.Response) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("read body:", err)
		return
	}
	cs := resp.TLS
	fmt.Printf("%d proto=%s %s curve=%d resumed=%v peer=%q who=%q body=%q\n",
		resp.StatusCode, resp.Proto, tls.VersionName(cs.Version), cs.CurveID,
		cs.DidResume, cs.PeerCertificates[0].Subject.CommonName,
		resp.Header.Get("X-Who"), body)
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
