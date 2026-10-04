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
// Nothing printed may depend on the port the kernel picked, nor on the
// processor: rustygo's internal/cpu reports no features, so a cipher suite
// chosen for AES hardware is not the one gc picks here, and the suite is
// therefore not printed. The certificate's validity is judged at a fixed
// instant, so the program does not go stale.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"
)

// The server's certificate and key, fixed rather than made on the spot:
// x509.CreateCertificate marshals through encoding/asn1, which reaches
// reflect.New, and reflection's write half is not there yet. When it is, this
// becomes a GenerateKey, a CreateCertificate and a tls.X509KeyPair, and
// nothing below it changes. The certificate is self-signed, names 127.0.0.1,
// and carries a P-256 key whose scalar is keyScalar.
const keyScalar = "1f2e3d4c5b6a798807162534435261708f9eadbccbdae9f80f1e2d3c4b5a6978"

const certHex = "" +
	"308201863082012da003020102020101300a06082a8648ce3d0403023017311530130603550403130c7275737479676f" +
	"20746573743020170d3230303931333132323634305a180f32303936313030323037303634305a301731153013060355" +
	"0403130c7275737479676f20746573743059301306072a8648ce3d020106082a8648ce3d0301070342000479436d5f4f" +
	"43d7c04264aed4de9a78330fbe638a84954c3ad961b934e9366abd22c1e0de087ff6dda9aaf5e3f116fec5d0e1d65f5b" +
	"ac81f39da2b0984eaf9c46a3683066300e0603551d0f0101ff04040302028430130603551d25040c300a06082b060105" +
	"05070301300f0603551d130101ff040530030101ff301d0603551d0e041604142df46629351c488e2277ad8a4eaeba4b" +
	"78219738300f0603551d110408300687047f000001300a06082a8648ce3d040302034700304402202259f811881e1820" +
	"2c95a0b6c325ab0b5d4885cace3ed0a81f45557923836fa60220420ce1ad2b48b3a3e082dae9f468164a1051ca699e7e" +
	"b1b096b6e8727f53aa85"

// at is the instant both ends judge the certificate's validity by, so the
// program's output never depends on today's date.
func at() time.Time { return time.Unix(1700000000, 0).UTC() }

func main() {
	der, err := hex.DecodeString(certHex)
	if err != nil {
		fmt.Println("certificate hex:", err)
		return
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		fmt.Println("parse certificate:", err)
		return
	}
	fmt.Printf("certificate %q for %v, ca=%v\n", leaf.Subject.CommonName, leaf.IPAddresses, leaf.IsCA)

	key := serverKey()
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		fmt.Printf("certificate holds a %T, not an ECDSA key\n", leaf.PublicKey)
		return
	}
	fmt.Println("key matches certificate:", pub.Equal(&key.PublicKey))

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
			Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
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

// serverKey rebuilds the key the certificate was issued for from its scalar,
// the public half derived from it rather than stated, so the two cannot drift
// apart.
func serverKey() *ecdsa.PrivateKey {
	b, err := hex.DecodeString(keyScalar)
	if err != nil {
		panic(err)
	}
	d := new(big.Int).SetBytes(b)
	x, y := elliptic.P256().ScalarBaseMult(d.Bytes())
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: d}
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
