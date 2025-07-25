package main

import (
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
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
const EnvEndpointPrefix = "GO_SSL_ENDPOINT_"

const EnvSkipGoSslToken = "SKIP_GO_SSL_TOKEN"

const KeyGoSslToken = "go-ssl-token"

var endpointsByHost = map[string]*url.URL{}
var skipTokenHosts = os.Getenv(EnvSkipGoSslToken)

func main() {
	servicego.Run(&Service{})
}

type Service struct {
	servicego.Defaults
	serverShutdown func(ctx context.Context) error
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

	for _, v := range os.Environ() {
		if strings.HasPrefix(v, EnvEndpointPrefix) {
			parts := strings.Split(v, "=")
			host := strings.ReplaceAll(strings.TrimPrefix(parts[0], EnvEndpointPrefix), "_", ".")
			remoteUrl, urlErr := url.Parse(parts[1])
			if urlErr != nil || remoteUrl.Scheme == "" {
				s.Errorf("unable to parse %q", v)
				continue
			}
			endpointsByHost[host] = remoteUrl
			s.Infof("added host %s remote %s", host, remoteUrl)
		}
	}

	s.Infof("skipTokenHosts = %q", skipTokenHosts)

	var listener net.Listener
	if listener, err = net.Listen("tcp4", os.Getenv(EnvAddress)); err != nil {
		return
	}

	server := &http.Server{Handler: s, TLSConfig: s.TLSConfig()}

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

	if request.URL != nil && request.URL.Path == "/"+os.Getenv(EnvGoSslToken) {
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
		return
	}

	var noPortHost = request.Host

	if strings.Contains(noPortHost, ":") {
		hostParts := strings.Split(noPortHost, ":")
		noPortHost = hostParts[0]
	}

	skipToken := strings.Contains(skipTokenHosts, noPortHost)

	if !skipToken {
		cook, err := request.Cookie(KeyGoSslToken)
		if errors.Is(err, http.ErrNoCookie) || cook.Value != os.Getenv(EnvGoSslToken) {
			s.Infof("DENIED %s %s %s", request.RemoteAddr, request.Host, request.RequestURI)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	remoteAddrParts := strings.Split(request.RemoteAddr, ":")
	s.Infof("ALLOW %s %s %s %s", remoteAddrParts[0], remoteAddrParts[1], request.Host, request.RequestURI)

	if remoteUrl, ok := endpointsByHost[noPortHost]; ok {
		proxy := httputil.NewSingleHostReverseProxy(remoteUrl)
		request.Header.Set("X-HomeSsl-Forwarded", "true")
		proxy.ServeHTTP(writer, request)
		return
	}
	writer.WriteHeader(http.StatusNotFound)
}

func (s *Service) hostPolicy(_ context.Context, host string) error {
	if _, ok := endpointsByHost[host]; ok {
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
