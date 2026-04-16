package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"time"

	"github.com/kardianos/service"
	"github.com/mlctrez/servicego"
	"golang.org/x/crypto/acme/autocert"
)

const CertsDir = "certs"

const EnvAddress = "ADDRESS"
const EnvAcmeDomain = "ACME_DOMAIN"
const EnvGoSslToken = "GO_SSL_TOKEN"
const KeyGoSslToken = "go-ssl-token"

type Service struct {
	servicego.Defaults
	serverShutdown func(ctx context.Context) error
	store          *EndpointStore
	dnsClient      DNSClient
}

func (s *Service) Start(_ service.Service) (err error) {
	if err = os.MkdirAll(CertsDir, 0700); err != nil {
		return err
	}

	for _, e := range []string{EnvAddress, EnvAcmeDomain, EnvGoSslToken} {
		if os.Getenv(e) == "" {
			return fmt.Errorf("%s environment not set", e)
		}
	}

	s.store, err = NewEndpointStore("endpoints.json")
	if err != nil {
		return err
	}

	for _, ep := range s.store.All() {
		s.Infof("added host %s remote %s", ep.Host, ep.URL)
	}

	if rc := NewRoute53Client(s); rc != nil {
		s.dnsClient = rc
	}

	var listener net.Listener
	if listener, err = net.Listen("tcp4", os.Getenv(EnvAddress)); err != nil {
		return
	}

	server := &http.Server{
		Handler:   s,
		TLSConfig: s.TLSConfig(),
		ErrorLog:  log.New(&tlsHandshakeFilter{s: s}, "", 0),
	}

	s.serverShutdown = server.Shutdown

	go func() {
		serveErr := server.ServeTLS(listener, "", "")
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.Errorf("error serving https: %v", serveErr)
		}
	}()
	return nil
}

func (s *Service) Stop(_ service.Service) (err error) {
	if s.serverShutdown != nil {
		stopContext, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		err = s.serverShutdown(stopContext)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			os.Exit(-1)
		}
	} else {
		s.Infof("http.Server.Shutdown success")
	}
	return
}

func (s *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var noPortHost = request.Host
	if strings.Contains(noPortHost, ":") {
		hostParts := strings.Split(noPortHost, ":")
		noPortHost = hostParts[0]
	}

	// Admin host routing
	if strings.HasPrefix(noPortHost, "admin.") {
		// Validate token for admin host
		if !s.validateToken(request) {
			// Allow token-setting path through without cookie
			if request.URL != nil && request.URL.Path == "/"+os.Getenv(EnvGoSslToken) {
				s.setTokenCookie(writer)
				return
			}
			s.Infof("DENIED %s %s %s", request.RemoteAddr, request.Host, request.RequestURI)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/api/") {
			apiHandler := &APIHandler{store: s.store, dnsClient: s.dnsClient, logger: s}
			apiHandler.ServeHTTP(writer, request)
			return
		}
		webHandler := NewWebHandler(s.store, s.dnsClient, s)
		webHandler.ServeHTTP(writer, request)
		return
	}

	// Token-setting path
	if request.URL != nil && request.URL.Path == "/"+os.Getenv(EnvGoSslToken) {
		s.setTokenCookie(writer)
		return
	}

	skipToken := s.store.IsSkipToken(noPortHost)

	if !skipToken {
		if !s.validateToken(request) {
			s.Infof("DENIED %s %s %s", request.RemoteAddr, request.Host, request.RequestURI)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	remoteAddrParts := strings.Split(request.RemoteAddr, ":")
	s.Infof("ALLOW %s %s %s %s", remoteAddrParts[0], remoteAddrParts[1], request.Host, request.RequestURI)

	if remoteUrl, ok := s.store.Lookup(noPortHost); ok {
		proxy := httputil.NewSingleHostReverseProxy(remoteUrl)
		request.Header.Set("X-HomeSsl-Forwarded", "true")
		proxy.ServeHTTP(writer, request)
		return
	}
	writer.WriteHeader(http.StatusNotFound)
}

// validateToken checks the go-ssl-token cookie using constant-time comparison.
func (s *Service) validateToken(request *http.Request) bool {
	cook, err := request.Cookie(KeyGoSslToken)
	if errors.Is(err, http.ErrNoCookie) || cook == nil {
		return false
	}
	expected := os.Getenv(EnvGoSslToken)
	return subtle.ConstantTimeCompare([]byte(cook.Value), []byte(expected)) == 1
}

// setTokenCookie sets the go-ssl-token cookie and redirects to /.
func (s *Service) setTokenCookie(writer http.ResponseWriter) {
	cookie := &http.Cookie{
		Name:     KeyGoSslToken,
		Value:    os.Getenv(EnvGoSslToken),
		Path:     "/",
		Domain:   os.Getenv(EnvAcmeDomain),
		Expires:  time.Now().AddDate(1, 0, 0),
		Secure:   true,
		HttpOnly: true,
	}
	writer.Header().Set("Set-Cookie", cookie.String())
	writer.Header().Set("Location", "/")
	writer.WriteHeader(http.StatusTemporaryRedirect)
}

func (s *Service) hostPolicy(_ context.Context, host string) error {
	if s.store.Has(host) {
		return nil
	}
	return fmt.Errorf("acme/autocert: host %q not configured in HostWhitelist", host)
}

func (s *Service) TLSConfig() *tls.Config {
	certManager := autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: s.hostPolicy,
		Cache:      autocert.DirCache(CertsDir),
	}
	tlsConfig := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1", "acme-tls/1"},
		GetCertificate: certManager.GetCertificate,
	}
	return tlsConfig
}

// tlsHandshakeFilter is an io.Writer that suppresses noisy TLS handshake
// errors from non-SNI clients while forwarding everything else.
type tlsHandshakeFilter struct {
	s *Service
}

var _ io.Writer = (*tlsHandshakeFilter)(nil)

func (f *tlsHandshakeFilter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if strings.Contains(msg, "TLS handshake error") {
		return len(p), nil
	}
	f.s.Errorf("%s", strings.TrimSpace(msg))
	return len(p), nil
}
