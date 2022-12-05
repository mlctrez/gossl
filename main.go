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

const KeyGoSslToken = "go-ssl-token"

var endpointsByHost = map[string]*url.URL{}

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
				s.Log().Error("unable to parse", v)
				continue
			}
			endpointsByHost[host] = remoteUrl
			s.Log().Infof("added host %s remote %s", host, remoteUrl)
		}
	}

	var listener net.Listener
	if listener, err = net.Listen("tcp4", os.Getenv(EnvAddress)); err != nil {
		return
	}

	server := &http.Server{Handler: s, TLSConfig: s.TLSConfig()}

	s.serverShutdown = server.Shutdown

	go func() {
		serveErr := server.ServeTLS(listener, "", "")
		if serveErr != nil && serveErr != http.ErrServerClosed {
			_ = s.Log().Error(err)
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
		_ = s.Log().Info("http.Server.Shutdown success")
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

	cook, err := request.Cookie(KeyGoSslToken)
	if err == http.ErrNoCookie || cook.Value != os.Getenv(EnvGoSslToken) {
		fmt.Println("DENIED", request.RemoteAddr, request.Host, request.RequestURI)
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}

	if remoteUrl, ok := endpointsByHost[request.Host]; ok {
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
		NextProtos:     []string{"h2", "http/1.1", "acme-tls/1"},
		GetCertificate: certManager.GetCertificate,
	}
	return tlsConfig
}
