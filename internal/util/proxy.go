// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for proxy configuration, HTTP client setup,
// log level management, and other common operations used across the application.
package util

import (
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// SetProxy configures the provided HTTP client with proxy settings from the configuration.
// It supports SOCKS5, HTTP, and HTTPS proxies. The function modifies the client's transport
// to route requests through the configured proxy server.
func SetProxy(cfg *config.SDKConfig, httpClient *http.Client) *http.Client {
	if cfg == nil || httpClient == nil {
		return httpClient
	}

	transport, _, errBuild := proxyutil.BuildHTTPTransport(cfg.ProxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
	}
	if transport != nil {
		httpClient.Transport = transport
	}
	return httpClient
}

// RefuseRedirect is an http.Client CheckRedirect policy that stops before any
// redirect, so a request body and its credential reach only the URL the caller
// chose.
func RefuseRedirect(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("upstream redirect to host %q refused", req.URL.Host)
}
