package multihttp

import (
	"os"
	"net"
	"net/url"
	"net/http"
	"crypto/tls"
	"time"
	//"fmt"
)

// Specifies an address (in URL format) and it's TLS cert file.
type TLSAddress struct {
	Address string
	CertFile string
	KeyFile string
}

func ParseAddress(address string) (string, string, error) {
	urlp, err := url.Parse(address)
	if err != nil {
		return "", "", err
	}
	
	if urlp.Path != "" {	// file-likes
		return urlp.Scheme, urlp.Path, nil
	} else {	// actual network sockets
		return urlp.Scheme, urlp.Host, nil
	} 
}

// Runs clean up on a list of listeners, namely deleting any Unix socket files
func CloseAndCleanUpListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		listener.Close()
		addr := listener.Addr()
		switch addr.(type) {
			case *net.UnixAddr:
				os.Remove(addr.String())
		}
	}
}

// Non-blocking function to listen on multiple http sockets. Returns a list of
// the created listener interfaces. Even in the case of errors, successfully
// listening interfaces are returned to allow for clean up.
func Listen(addresses []string, handler http.Handler) ([]net.Listener, error) {
	var listeners []net.Listener
	
	for _, address := range addresses {		
		protocol, address, err := ParseAddress(address)
		if err != nil {
			return listeners, err
		}
		
		listener, err := net.Listen(protocol, address)
		if err != nil {
			return listeners, err
		}
		
		// Append and start serving on listener
		listener = maybeKeepAlive(listener)
		listeners = append(listeners, listener)
		go http.Serve(listener, handler)
	}
	
	return listeners, nil
}

// Non-blocking function serve on multiple HTTPS sockets
// Requires a list of certs
func ListenTLS(addresses []TLSAddress, handler http.Handler) ([]net.Listener, error) {
	var listeners []net.Listener
	
	for _, tlsAddressInfo := range addresses {
		protocol, address, err := ParseAddress(tlsAddressInfo.Address)
		if err != nil {
			return listeners, err
		}
		
		listener, err := net.Listen(protocol, address)
		if err != nil {
			return listeners, err
		}
		
		config := &tls.Config{}
		
		config.NextProtos = []string{"http/1.1"}
		
		config.Certificates = make([]tls.Certificate, 1)
		config.Certificates[0], err = tls.LoadX509KeyPair(tlsAddressInfo.CertFile, tlsAddressInfo.KeyFile)
		if err != nil {
			return listeners, err
		}
		
		listener = maybeKeepAlive(listener)
		
		tlsListener := tls.NewListener(listener, config)
		if err != nil {
			return listeners, err
		}
		
		// Append and start serving on listener
		listeners = append(listeners, tlsListener)
		go http.Serve(tlsListener, nil)
	}
	
	return listeners, nil
}

// Checks if a listener is a TCP and needs a keepalive handler
func maybeKeepAlive(ln net.Listener) net.Listener {
	if o, ok := ln.(*net.TCPListener); ok {
		return tcpKeepAliveListener{o}
	}
	return ln
} 

// Irritatingly the tcpKeepAliveListener is not public, so we need to recreate it.
// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted connections.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}

// Returns a dialer which ignores the address string and connects to the
// given socket always.
func newDialer(addr string) (func (proto, addr string) (conn net.Conn, err error), error) {
	realProtocol, realAddress, err := ParseAddress(addr)
	if err != nil {
		return nil, err
	}
	
	return func (proto, addr string) (conn net.Conn, err error) {
		return net.Dial(realProtocol, realAddress)
	}, nil
}

// Initialize an HTTP client which connects to the provided socket address to
// service requests. The hostname in requests is parsed as a header only.
func NewClient(addr string) (*http.Client, error) {
	dialer, err := newDialer(addr)
	if err != nil {
		return nil, err
	}
	
	tr := &http.Transport{ Dial: dialer, }
	client := &http.Client{Transport: tr}
	
	return client, nil
}