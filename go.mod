module github.com/annulo/annulo

go 1.26.0

require (
	github.com/JohannesKaufmann/html-to-markdown/v2 v2.5.2
	github.com/PuerkitoBio/goquery v1.13.0
	github.com/andybalholm/cascadia v1.3.4
	github.com/br41n10/qetag v0.2.1
	github.com/chromedp/cdproto v0.0.0-20260714215040-dc233986426f
	github.com/chromedp/chromedp v0.16.0
	github.com/dop251/goja v0.0.0-20260917113740-793a2a65c13b
	github.com/evanw/esbuild v0.28.2
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/sky-valley/pi v0.87.25-0.20260928092708-07d479e4780d
	github.com/spf13/cobra v1.10.2
	golang.org/x/net v0.59.0
	golang.org/x/oauth2 v0.35.0
	golang.org/x/sys v0.48.0
	modernc.org/sqlite v1.60.1
)

require (
	github.com/aymanbagabas/go-udiff v0.4.1 // indirect
	github.com/creght-dev/creght-cli v0.23.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

require (
	github.com/JohannesKaufmann/dom v0.3.1 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/coder/websocket v1.8.15
	github.com/dlclark/regexp2/v2 v2.5.2 // indirect
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/image v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

// 打了一个小补丁，见 third_party/pi/SHUTTLE_PATCH.md
replace github.com/sky-valley/pi => ./third_party/pi
