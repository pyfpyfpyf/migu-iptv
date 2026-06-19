package main

// migu 使用 Go 复刻原 PHP 的咪咕播放链接签名与加密逻辑，
// 提供与原项目等价的 HTTP 接口并返回 302 重定向至 OTT 播放地址。
// 也支持 generate 子命令，直接生成包含真实播放地址的 m3u/txt 静态文件。

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

type cacheEntry struct {
	URL  string `json:"url"`
	Time int64  `json:"time"`
	TTL  int64  `json:"ttl"`
}

type authCred struct {
	UserID    string
	UserToken string
}

var (
	memCache    = make(map[string]cacheEntry)
	cacheMu     sync.RWMutex
	proxyClient *http.Client
)

func init() {
	noProxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		Proxy:               noProxy,
	}
	proxyClient = &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
	}
}

// saltTable 每个版本对应的盐值
func saltTable(ver string) string {
	switch ver {
	case "2600034600":
		return "2cac4f2c6c3346a5b34e085725ef7e33"
	case "2600037000":
		return "3ce941cc3cbc40528bfd1c64f9fdf6c0"
	default:
		return "2cac4f2c6c3346a5b34e085725ef7e33"
	}
}

func getPlayUrlCache(key string) (string, int) {
	cacheMu.RLock()
	ce, ok := memCache[key]
	cacheMu.RUnlock()
	if !ok {
		return "", 0
	}
	if time.Now().Unix()-ce.Time > ce.TTL {
		cacheMu.Lock()
		delete(memCache, key)
		cacheMu.Unlock()
		return "", 0
	}
	return ce.URL, int(ce.TTL - (time.Now().Unix() - ce.Time))
}

func setPlayUrlCache(key, url string, ttlSeconds int64) {
	cacheMu.Lock()
	memCache[key] = cacheEntry{URL: url, Time: time.Now().Unix(), TTL: ttlSeconds}
	cacheMu.Unlock()
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func urlSign(md5string string, ver string) (string, string, error) {
	salt, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", "", err
	}
	// 新版盐值格式：随机6位数字 + "25"
	saltstr := fmt.Sprintf("%06d", salt.Int64()) + "25"
	salttable := saltTable(ver)
	text := md5string + salttable + "migu" + saltstr[:4]
	sign := md5Hex(text)
	return saltstr, sign, nil
}

func getSignConfig(contID string, appVersion string) (string, string, string, error) {
	tm := fmt.Sprintf("%d", time.Now().UnixMilli())
	md5string := md5Hex(tm + contID + appVersion[:8])
	salt, sign, err := urlSign(md5string, appVersion)
	if err != nil {
		return "", "", "", err
	}
	return tm, salt, sign, nil
}

func sendGetRequest(reqURL string, headers map[string]string) (string, error) {
	noProxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
		Proxy:             noProxy,
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func getRedirectURL(reqURL string, headers map[string]string) (string, error) {
	noProxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
		Proxy:             noProxy,
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: tr,
		Timeout:   10 * time.Second,
	}
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		return resp.Header.Get("Location"), nil
	}
	return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
}

func toRunes(s string) []rune { return []rune(s) }

func miguEncryptedURL(str string) string {
	if strings.TrimSpace(str) == "" {
		return ""
	}
	u, err := url.Parse(str)
	if err != nil || u.RawQuery == "" {
		return ""
	}
	q, _ := url.ParseQuery(u.RawQuery)
	S := q.Get("puData")
	U := q.Get("userid")
	T := q.Get("timestamp")
	P := q.Get("ProgramID")
	C := q.Get("Channel_ID")
	V := q.Get("playurlVersion")

	sRunes := toRunes(S)
	N := len(sRunes)
	if N == 0 {
		return ""
	}
	half := (N + 1) / 2
	var b strings.Builder
	for i := 0; i < half; i++ {
		if N%2 == 1 && i == half-1 {
			b.WriteRune(sRunes[i])
			break
		}
		b.WriteRune(sRunes[N-1-i])
		b.WriteRune(sRunes[i])
		switch i {
		case 1:
			uRunes := toRunes(U)
			if len(uRunes) > 2 {
				b.WriteRune(uRunes[2])
			} else {
				vRunes := toRunes(V)
				if len(vRunes) > 0 {
					r := vRunes[len(vRunes)-1]
					b.WriteRune(unicode.ToLower(r))
				}
			}
		case 2:
			tRunes := toRunes(T)
			if len(tRunes) > 6 {
				b.WriteRune(tRunes[6])
			} else {
				b.WriteRune(sRunes[i])
			}
		case 3:
			pRunes := toRunes(P)
			if len(pRunes) > 2 {
				b.WriteRune(pRunes[2])
			} else {
				b.WriteRune(sRunes[i])
			}
		case 4:
			cRunes := toRunes(C)
			if len(cRunes) >= 4 {
				b.WriteRune(cRunes[len(cRunes)-4])
			} else {
				b.WriteRune(sRunes[i])
			}
		}
	}
	base := str
	if idx := strings.Index(str, "?"); idx >= 0 {
		base = str[:idx]
	}
	dd := b.String()
	return fmt.Sprintf("%s?%s&ddCalcu=%s", base, u.RawQuery, dd)
}

type playResp struct {
	Body struct {
		URLInfo struct {
			URL string `json:"url"`
		} `json:"urlInfo"`
	} `json:"body"`
}

func handleMiguMainRequest(id string, cred *authCred) (string, error) {
	if url, ttl := getPlayUrlCache(id); ttl > 0 {
		return url, nil
	}
	var reqURL string
	appVersion := os.Getenv("APP_VERSION")
	if strings.TrimSpace(appVersion) == "" {
		appVersion = "2600034600"
	}
	tm, salt, sign, err := getSignConfig(id, appVersion)
	if err != nil {
		fmt.Printf("getSignConfig err: %v\n", err)
		return "", err
	}
	chid, _ := rand.Int(rand.Reader, big.NewInt(10000))
	chidstr := fmt.Sprintf("%04d", chid.Int64()%10000)
	headers := map[string]string{
		"Host":                   "play.miguvideo.com",
		"appId":                  "miguvideo",
		"terminalId":             "android",
		"User-Agent":             "Dalvik%2F2.1.0+%28Linux%3B+U%3B+Android+15%3B+V2227A+Build%2FAP3A.240905.015.A2%29",
		"MG-BH":                  "true",
		"appVersion":             appVersion,
		"appCode":                "miguvideo_default_android",
		"Phone-Info":             "V2227A",
		"X-UP-CLIENT-CHANNEL-ID": appVersion + "-99000-20160001001" + chidstr,
		"APP-VERSION-CODE":       "260530009",
		"Accept":                 "*/*",
		"Connection":             "keep-alive",
	}
	rateType := 0
	if cred == nil {
		rateType = 3
	} else {
		rateType = 8
		headers["userToken"] = cred.UserToken
		headers["userId"] = cred.UserID
	}
	reqURL = fmt.Sprintf("https://play.miguvideo.com/playurl/v1/play/playurl?sign=%s&rateType=%d&contId=%s&timestamp=%s&salt=%s&flvEnable=true&super4k=true&h265N=true", sign, rateType, id, tm, salt)
	raw := ""
	for {
		body, err := sendGetRequest(reqURL, headers)
		if err != nil {
			fmt.Printf("sendGetRequest err: %v\n", err)
			return "", err
		}
		var pr playResp
		if err = json.Unmarshal([]byte(body), &pr); err != nil {
			fmt.Printf("json.Unmarshal err: %v\n", err)
			return "", err
		}
		raw = pr.Body.URLInfo.URL
		if strings.TrimSpace(raw) == "" {
			fmt.Printf("empty url from migu, rateType: %d, contId: %s\n", rateType, id)
			if rateType == 8 {
				rateType = 3
			} else if rateType == 3 {
				rateType = 1
			} else {
				break
			}
			delete(headers, "userId")
			delete(headers, "userToken")
			reqURL = fmt.Sprintf("https://play-pre.miguvideo.com/playurl/v1/play/playurl?sign=%s&rateType=%d&contId=%s&timestamp=%s&salt=%s&flvEnable=true&super4k=true&h265N=true", sign, rateType, id, tm, salt)
			continue
		}
		break
	}

	if strings.TrimSpace(raw) == "" {
		return "", errors.New("empty url from migu")
	}
	ott := miguEncryptedURL(raw)
	if strings.TrimSpace(ott) == "" {
		return "", errors.New("empty ott url")
	}
	ott, err = getRedirectURL(ott, map[string]string{
		"User-Agent": "Dalvik%2F2.1.0+%28Linux%3B+U%3B+Android+15%3B+V2227A+Build%2FAP3A.240905.015.A2%29",
	})
	if err != nil {
		fmt.Printf("getRedirectURL err: %v\n", err)
		return "", err
	}

	setPlayUrlCache(id, ott, 1800)
	return ott, nil
}

// getHntvPlayURL 获取芒果TV频道播放地址
func getHntvPlayURL(id string) (string, error) {
	channels := map[string]int{
		"cpd":   578,
		"hnds":  346,
		"hndsj": 484,
		"hngg":  261,
		"hngj":  229,
		"hnjs":  280,
		"hnyl":  344,
		"hndy":  221,
		"jyjs":  316,
		"jykt":  287,
		"klcd":  218,
		"klg":   267,
		"xfpy":  329,
		"csxw":  269,
		"csnx":  230,
		"cszf":  254,
	}
	cid, ok := channels[id]
	if !ok {
		return "", fmt.Errorf("unknown channel id: %s", id)
	}

	api := fmt.Sprintf("http://pwlp.bz.mgtv.com/v1/epg/turnplay/getLivePlayUrlMPP?version=PCweb_1.0&platform=1&buss_id=2000001&channel_id=%d", cid)
	body, err := sendGetRequest(api, map[string]string{})
	if err != nil {
		return "", err
	}
	var resp struct {
		Msg  string `json:"msg"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Data.URL) == "" {
		return "", errors.New("empty play url")
	}
	return resp.Data.URL, nil
}

// ─── HTTP 服务模式 ────────────────────────────────────────────

func miguHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("收到请求 来源地址：", r.RemoteAddr)
	id := r.URL.Query().Get("id")
	if strings.TrimSpace(id) == "" {
		id = "608807420"
	}
	uid := r.URL.Query().Get("userId")
	ut := r.URL.Query().Get("userToken")
	if uid == "" || ut == "" {
		if envUID := os.Getenv("MIGU_USER_ID"); envUID != "" {
			uid = envUID
		}
		if envUT := os.Getenv("MIGU_USER_TOKEN"); envUT != "" {
			ut = envUT
		}
	}
	var cred *authCred
	if uid != "" && ut != "" {
		cred = &authCred{UserID: uid, UserToken: ut}
	}

	url, err := handleMiguMainRequest(id, cred)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

func iptvHandler(w http.ResponseWriter, r *http.Request) {
	content, err := os.ReadFile("migu_plist.txt")
	if err != nil {
		http.Error(w, "无法读取频道列表文件", http.StatusInternalServerError)
		return
	}
	host := r.Host
	protocol := "http"
	if r.TLS != nil {
		protocol = "https"
	}
	currentDomain := fmt.Sprintf("%s://%s", protocol, host)
	updatedContent := strings.ReplaceAll(string(content), "http://xxx.xxx.xxx/", currentDomain+"/")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(updatedContent))
}

func tvm3u8Handler(w http.ResponseWriter, r *http.Request) {
	content, err := os.ReadFile("migu_plist.txt")
	if err != nil {
		http.Error(w, "无法读取频道列表文件", http.StatusInternalServerError)
		return
	}
	host := r.Host
	protocol := "http"
	if r.TLS != nil {
		protocol = "https"
	}
	currentDomain := fmt.Sprintf("%s://%s", protocol, host)

	var m3u8Content strings.Builder
	m3u8Content.WriteString("#EXTM3U\n")
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" || i == 0 {
			continue
		}
		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			continue
		}
		channelName := parts[0]
		url := parts[1]
		updatedURL := strings.ReplaceAll(url, "http://xxx.xxx.xxx/", currentDomain+"/")
		m3u8Content.WriteString(fmt.Sprintf("#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\",%s\n", channelName, channelName, channelName))
		m3u8Content.WriteString(updatedURL + "\n")
	}
	w.Header().Set("Content-Type", "application/x-mpegURL; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(m3u8Content.String()))
}

func hntvHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if strings.TrimSpace(id) == "" {
		id = "cpd"
	}
	playURL, err := getHntvPlayURL(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, playURL, http.StatusFound)
}

func startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cacheMu.Lock()
			now := time.Now().Unix()
			for k, v := range memCache {
				if now-v.Time > v.TTL {
					delete(memCache, k)
				}
			}
			cacheMu.Unlock()
		}
	}()
}

func runServer() {
	startCacheCleanup()
	http.HandleFunc("/migu.php", miguHandler)
	http.HandleFunc("/hntv.php", hntvHandler)
	http.HandleFunc("/iptv", iptvHandler)
	http.HandleFunc("/tvm3u8", tvm3u8Handler)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{Addr: ":" + port}
	fmt.Printf("服务启动，监听 :%s\n", port)
	_ = srv.ListenAndServe()
}

// ─── 静态文件生成模式 ─────────────────────────────────────────

// channelEntry 频道条目
type channelEntry struct {
	Name string
	URL  string // 真实播放地址
}

// generateStaticFiles 读取 migu_plist.txt，获取每个频道的真实播放地址，生成 m3u/txt 文件
func generateStaticFiles(outputDir string) error {
	content, err := os.ReadFile("migu_plist.txt")
	if err != nil {
		return fmt.Errorf("读取 migu_plist.txt 失败: %w", err)
	}

	var groups []string                          // 分组名列表
	var channels [][]channelEntry                // 每组对应的频道
	var currentChannels []channelEntry

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		rawURL := strings.TrimSpace(parts[1])

		// 检测分组行（如 "咪咕专线,#genre#"）
		if strings.Contains(rawURL, "#genre#") {
			if len(currentChannels) > 0 || len(groups) > 0 {
				channels = append(channels, currentChannels)
				currentChannels = nil
			}
			groups = append(groups, name)
			continue
		}

		// 解析频道类型和ID
		var playURL string
		if strings.Contains(rawURL, "migu.php") {
			// 咪咕频道：使用中转服务（GitHub Actions 海外IP无法直接获取咪咕播放地址）
			u, err := url.Parse(rawURL)
			if err != nil {
				fmt.Printf("解析URL失败: %s, err: %v\n", rawURL, err)
				continue
			}
			id := u.Query().Get("id")
			if id == "" {
				continue
			}
			miguServer := os.Getenv("MIGU_SERVER")
			if miguServer == "" {
				miguServer = "http://xxx.xxx.xxx"
			}
			playURL = fmt.Sprintf("%s/migu.php?id=%s", miguServer, id)
			fmt.Printf("✓ 咪咕(中转) %s (id=%s)\n", name, id)
		} else if strings.Contains(rawURL, "hntv.php") {
			// 芒果TV频道：GitHub Actions 可以直接获取真实播放地址
			u, err := url.Parse(rawURL)
			if err != nil {
				fmt.Printf("解析URL失败: %s, err: %v\n", rawURL, err)
				continue
			}
			id := u.Query().Get("id")
			if id == "" {
				continue
			}
			realURL, err := getHntvPlayURL(id)
			if err != nil {
				fmt.Printf("获取芒果TV播放地址失败: id=%s, err: %v\n", id, err)
				continue
			}
			playURL = realURL
			fmt.Printf("✓ 芒果 %s (id=%s)\n", name, id)
		} else {
			// 其他URL直接使用
			playURL = rawURL
		}

		currentChannels = append(currentChannels, channelEntry{Name: name, URL: playURL})
	}
	// 最后一组
	if len(currentChannels) > 0 {
		channels = append(channels, currentChannels)
	}

	// 确保输出目录存在
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	// ─── 生成 m3u 文件 ───
	var m3u strings.Builder
	m3u.WriteString("#EXTM3U\n")
	for gi, group := range groups {
		if gi < len(channels) {
			for _, ch := range channels[gi] {
				m3u.WriteString(fmt.Sprintf("#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" group-title=\"%s\",%s\n", ch.Name, ch.Name, group, ch.Name))
				m3u.WriteString(ch.URL + "\n")
			}
		}
	}
	m3uPath := outputDir + "/tv.m3u"
	if err := os.WriteFile(m3uPath, []byte(m3u.String()), 0644); err != nil {
		return fmt.Errorf("写入 m3u 文件失败: %w", err)
	}
	fmt.Printf("\n生成 m3u 文件: %s (%d 频道)\n", m3uPath, countChannels(channels))

	// ─── 生成 txt 文件 ───
	var txt strings.Builder
	for gi, group := range groups {
		txt.WriteString(fmt.Sprintf("%s,#genre#\n", group))
		if gi < len(channels) {
			for _, ch := range channels[gi] {
				txt.WriteString(fmt.Sprintf("%s,%s\n", ch.Name, ch.URL))
			}
		}
	}
	txtPath := outputDir + "/tv.txt"
	if err := os.WriteFile(txtPath, []byte(txt.String()), 0644); err != nil {
		return fmt.Errorf("写入 txt 文件失败: %w", err)
	}
	fmt.Printf("生成 txt 文件: %s\n", txtPath)

	return nil
}

func countChannels(channels [][]channelEntry) int {
	total := 0
	for _, g := range channels {
		total += len(g)
	}
	return total
}

// ─── main ─────────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 && os.Args[1] == "generate" {
		outputDir := "docs"
		if len(os.Args) > 2 {
			outputDir = os.Args[2]
		}
		fmt.Println("=== 静态文件生成模式 ===")
		fmt.Printf("输出目录: %s\n\n", outputDir)
		if err := generateStaticFiles(outputDir); err != nil {
			fmt.Fprintf(os.Stderr, "生成失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("\n=== 生成完成 ===")
		return
	}

	// 默认：HTTP 服务模式
	runServer()
}
