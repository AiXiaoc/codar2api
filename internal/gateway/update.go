package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const projectRepositoryURL = "https://github.com/AiXiaoc/codar2api"
const latestReleaseEndpoint = "https://api.github.com/repos/AiXiaoc/codar2api/releases/latest"

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type updateResult struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	Status         string `json:"status"`
	ReleaseURL     string `json:"release_url"`
	CheckedAt      int64  `json:"checked_at"`
}

type releaseChecker struct {
	mu        sync.Mutex
	client    *http.Client
	endpoint  string
	release   *githubRelease
	checkedAt time.Time
}

func newReleaseChecker() *releaseChecker {
	return &releaseChecker{
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		endpoint: latestReleaseEndpoint,
	}
}

func (c *releaseChecker) check(ctx context.Context, current string) (updateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return updateResult{}, errors.New("检测更新已取消，请重试")
	}
	// Share a recent successful check across administrators and concurrent clicks.
	if c.release == nil || time.Since(c.checkedAt) >= time.Minute {
		release, err := c.fetch(ctx)
		if err != nil {
			return updateResult{}, err
		}
		c.release, c.checkedAt = release, time.Now()
	}
	result := updateResult{
		CurrentVersion: current,
		LatestVersion:  c.release.Tag,
		Status:         "unknown",
		ReleaseURL:     projectRepositoryURL + "/releases/tag/" + url.PathEscape(c.release.Tag),
		CheckedAt:      c.checkedAt.Unix(),
	}
	if comparison, ok := compareVersions(current, c.release.Tag); ok {
		switch {
		case comparison < 0:
			result.Status = "available"
		case comparison == 0:
			result.Status = "current"
		default:
			result.Status = "newer"
		}
	}
	return result, nil
}

func (c *releaseChecker) fetch(ctx context.Context) (*githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, errors.New("无法创建更新检测请求")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Codar2API/"+Version)
	response, err := c.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("检测更新超时，请稍后重试")
		}
		return nil, errors.New("无法连接 GitHub，请检查网关的网络连接后重试")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.New("GitHub 暂无可用的正式版本")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, errors.New("GitHub 暂时限制了请求，请稍后重试")
	default:
		return nil, fmt.Errorf("GitHub 返回异常状态（HTTP %d），请稍后重试", response.StatusCode)
	}
	const bodyLimit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
	if err != nil || len(body) > bodyLimit {
		return nil, errors.New("读取 GitHub 版本信息失败，请稍后重试")
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, errors.New("GitHub 返回的版本信息无法识别")
	}
	if release.Draft || release.Prerelease {
		return nil, errors.New("GitHub 暂无可用的正式版本")
	}
	if _, ok := parseVersion(release.Tag); !ok {
		return nil, errors.New("GitHub 返回的版本号格式无法识别")
	}
	return &release, nil
}

type semanticVersion struct {
	core [3]string
	pre  []string
}

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func parseVersion(value string) (semanticVersion, bool) {
	parts := versionPattern.FindStringSubmatch(value)
	if parts == nil {
		return semanticVersion{}, false
	}
	v := semanticVersion{core: [3]string{parts[1], parts[2], parts[3]}}
	if parts[4] != "" {
		v.pre = strings.Split(parts[4], ".")
		for _, identifier := range v.pre {
			if numericVersionID(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return semanticVersion{}, false
			}
		}
	}
	return v, true
}

func numericVersionID(value string) bool {
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return value != ""
}

func compareVersionID(a, b string) int {
	an, bn := numericVersionID(a), numericVersionID(b)
	if an && bn && len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	if an != bn {
		if an {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func compareVersions(current, latest string) (int, bool) {
	a, aok := parseVersion(current)
	b, bok := parseVersion(latest)
	if !aok || !bok {
		return 0, false
	}
	for i := range a.core {
		if comparison := compareVersionID(a.core[i], b.core[i]); comparison != 0 {
			return comparison, true
		}
	}
	if len(a.pre) == 0 && len(b.pre) != 0 {
		return 1, true
	}
	if len(a.pre) != 0 && len(b.pre) == 0 {
		return -1, true
	}
	for i := 0; i < min(len(a.pre), len(b.pre)); i++ {
		if comparison := compareVersionID(a.pre[i], b.pre[i]); comparison != 0 {
			return comparison, true
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1, true
	}
	if len(a.pre) > len(b.pre) {
		return 1, true
	}
	return 0, true
}
