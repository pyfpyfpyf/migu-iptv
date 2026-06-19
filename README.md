# 咪咕 IPTV 频道列表

Go 语言实现的咪咕视频/芒果TV播放链接签名，通过 GitHub Actions 自动生成包含真实播放地址的 m3u/txt 文件，托管在 GitHub Pages。

## 订阅链接

部署后替换 `你的用户名`：

| 格式 | 链接 | 适用 |
|------|------|------|
| M3U | `https://你的用户名.github.io/migu-iptv/tv.m3u` | VLC / IPTV 播放器 |
| TXT | `https://你的用户名.github.io/migu-iptv/tv.txt` | 影视仓 / DIYP |

## 自动更新

GitHub Actions 每 6 小时自动运行一次，重新获取所有频道的真实播放地址并更新文件。也可手动触发（Actions → Update M3U → Run workflow）。

## 本地运行

```bash
# HTTP 服务模式
go run main.go

# 生成静态文件模式
go run main.go generate docs
```

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `APP_VERSION` | 咪咕版本号 | `2600000900` |
| `MIGU_USER_ID` | 咪咕用户ID（可选，用于高清） | - |
| `MIGU_USER_TOKEN` | 咪咕用户Token（可选，用于高清） | - |
