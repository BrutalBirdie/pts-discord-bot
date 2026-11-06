package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"
)

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicOnly refuses connections to loopback, private and link-local
// addresses, so a URL cannot reach the Cloudron server or its internal network.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || cgnat.Contains(ip) {
		return fmt.Errorf("%s is not a public address", ip)
	}
	return nil
}

var fetchClient = &http.Client{
	Timeout: 2 * time.Minute,
	Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return checkURL(req.URL)
	},
}

func checkURL(u *url.URL) error {
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("only http and https links work")
	}
	return nil
}

// Fetch downloads a file from a public URL. The name comes from the
// Content-Disposition header or the path, with an extension from the content type when it has none.
func Fetch(ctx context.Context, raw string) (io.ReadCloser, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, "", errors.New("this is not a link")
	}
	if err := checkURL(u); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "pts-discord-bot")
	resp, err := fetchClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("download failed: %s", resp.Status)
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct == "text/html" {
		resp.Body.Close()
		return nil, "", errors.New("the link leads to a web page, not to a file. Video pages like YouTube are not supported")
	}
	name := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	if name == "" {
		name = path.Base(resp.Request.URL.Path)
	}
	if name == "" || name == "/" || name == "." {
		name = "download"
	}
	if path.Ext(name) == "" {
		if exts, _ := mime.ExtensionsByType(resp.Header.Get("Content-Type")); len(exts) > 0 {
			name += exts[0]
		}
	}
	return resp.Body, name, nil
}
