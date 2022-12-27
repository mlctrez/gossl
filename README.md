# gossl

A reverse proxy using [acme/autocert](https://pkg.go.dev/golang.org/x/crypto/acme/autocert) for automatically creating / renewing certificates. 

Access is controlled by a not-easily-guessable token.

```bash

# Environment variables and descriptions. 
# A file with these can be placed /etc/sysconfig/<service> if run as a service.

# Listen address in http.ListenAndServe format
ADDRESS=EXTERNAL_IP:443

# Domain to use in the cookie
ACME_DOMAIN=COOKIE_DOMAIN

# The token to allow access. Visit one of the configured domains with this
# in the path to set it. Rotate frequently and don't share in public. 
GO_SSL_TOKEN=LONG_UUID_NOT_EASILY_GUESSABLE

# reverse proxy entries must have prefix GO_SSL_ENDPOINT_
# followed by hostname with "." replaced with "_" 
GO_SSL_ENDPOINT_one_example_com=http://10.0.0.1:9000
GO_SSL_ENDPOINT_two_example_com=http://10.0.0.2:9000

# this environment variable indicates which host names can skip the token verification
# i.e. strings.Contains(os.getEnv(SKIP_GO_SSL_TOKEN), hostName)
SKIP_GO_SSL_TOKEN=two.example.com

```

[![Go Report Card](https://goreportcard.com/badge/github.com/mlctrez/gossl)](https://goreportcard.com/report/github.com/mlctrez/gossl)

created by [tigwen](https://github.com/mlctrez/tigwen)
