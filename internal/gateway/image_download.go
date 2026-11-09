package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const maxCodarImageBytes = 32 << 20

var publicIPv6Range = netip.MustParsePrefix("2000::/3")
var nonPublicImageNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure host services
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicImageIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is6() && !publicIPv6Range.Contains(ip) {
		return false
	}
	for _, network := range nonPublicImageNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

// Check the actual destination on every new connection, including redirects.
// Dial the vetted numeric address so DNS cannot change after validation.
func dialPublicImage(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "80" && port != "443" {
		return nil, errors.New("不允许的图片下载地址")
	}
	var ips []netip.Addr
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	for _, ip := range ips {
		if !publicImageIP(ip) {
			continue
		}
		conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if e == nil {
			return conn, nil
		}
	}
	return nil, errors.New("无法连接公网图片地址")
}

func newCodarImageClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: dialPublicImage, ForceAttemptHTTP2: true,
			TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns: 16, IdleConnTimeout: 45 * time.Second, MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 || !codarImageURL(r.URL.String()) {
				return errors.New("不允许的图片下载跳转")
			}
			r.Header.Del("Referer")
			return nil
		},
	}
}

func (a *App) downloadCodarImage(ctx context.Context, imageURL string) ([]byte, error) {
	if !codarImageURL(imageURL) {
		return nil, &UpstreamError{Status: 502, Message: "Codar 返回的图片链接无效"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, &UpstreamError{Status: 502, Message: "Codar 返回的图片链接无效"}
	}
	req.Header.Set("Accept", "image/*")
	resp, err := a.imageClient.Do(req)
	if err != nil {
		return nil, &UpstreamError{Status: 502, Message: "下载 Codar 结果图片失败"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &UpstreamError{Status: 502, Message: "图片服务器返回 HTTP " + strconv.Itoa(resp.StatusCode)}
	}
	tooLarge := &UpstreamError{Status: 502, Message: "Codar 结果图片超过 32 MiB 下载上限"}
	if resp.ContentLength > maxCodarImageBytes {
		return nil, tooLarge
	}
	image, err := io.ReadAll(io.LimitReader(resp.Body, maxCodarImageBytes+1))
	if err != nil {
		return nil, &UpstreamError{Status: 502, Message: "读取 Codar 结果图片失败"}
	}
	if len(image) > maxCodarImageBytes {
		return nil, tooLarge
	}
	if !strings.HasPrefix(codarImageMediaType(image), "image/") {
		return nil, &UpstreamError{Status: 502, Message: "Codar 图片链接未返回可识别的图片"}
	}
	return image, nil
}

func codarImageMediaType(b []byte) string {
	if len(b) >= 16 && string(b[4:8]) == "ftyp" && (string(b[8:12]) == "avif" || string(b[8:12]) == "avis") {
		return "image/avif"
	}
	return http.DetectContentType(b)
}

func (a *App) writeCodarImage(w http.ResponseWriter, r *http.Request, out M, imageURL string) error {
	result := M{"url": imageURL}
	if yes(a.store.Settings()["image_result_base64"]) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		select {
		case a.imageDownloads <- struct{}{}:
			defer func() { <-a.imageDownloads }()
		case <-ctx.Done():
			return &UpstreamError{Status: 504, Message: "等待下载 Codar 结果图片超时"}
		}
		image, err := a.downloadCodarImage(ctx, imageURL)
		if err != nil {
			return err
		}
		// encoding/json encodes a byte slice as standard Base64.
		result = M{"b64_json": image}
		if yes(out["ok"]) { // MIME is needed for the panel's image preview.
			result["mime_type"] = codarImageMediaType(image)
		}
	}
	out["data"] = []any{result}
	jsonResponse(w, 200, out)
	return nil
}
