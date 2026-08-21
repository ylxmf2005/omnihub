// Package dashboardassets 承载 `pnpm build` 产出的 Dashboard 静态资源，
// 让 `omnihub serve` 不依赖任何外部进程或目录即可提供界面。
//
// 资源以 `all:` 前缀嵌入，因此仓库里只要保留 dist 目录（即使只有占位文件）就能编译；
// 是否真的构建过界面由 Available 在运行时判断，而不是让缺失产物变成编译失败。
package dashboardassets

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

const indexPath = "dist/index.html"

// Available 报告当前二进制里是否嵌入了可用的 Dashboard 构建产物。
// 没有构建过时 serve 必须保持原有的 endpoint_not_found 行为，
// 不能返回一个空白页面假装界面存在。
func Available() bool {
	file, err := embedded.Open(indexPath)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	return err == nil && info.Size() > 0
}

// Stale 报告磁盘上的构建产物是否比二进制里嵌入的更新。
//
// `go:embed` 在编译期取快照，因此「重新构建前端但忘记重新 go build」会让 serve
// 继续提供旧界面，而且表面上一切正常——这类问题只会表现为「我改的东西没生效」。
// 这里比较 index.html 引用的入口资源名：Vite 给它加了内容哈希，名字不同就意味着
// 产物不同。dir 通常是 web/dashboard 的 build.outDir，源码树之外调用时不存在，
// 此时返回 false 而不是报错，因为发行版二进制本来就没有源码目录。
func Stale(dir string) (bool, string) {
	onDisk, err := entryAsset(os.DirFS(dir))
	if err != nil || onDisk == "" {
		return false, ""
	}
	embeddedSub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return false, ""
	}
	inBinary, err := entryAsset(embeddedSub)
	if err != nil || inBinary == "" {
		return false, ""
	}
	if onDisk == inBinary {
		return false, ""
	}
	return true, onDisk
}

// entryAsset 从 index.html 中取出入口 JS 的带哈希文件名。
func entryAsset(files fs.FS) (string, error) {
	payload, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return "", err
	}
	match := entryPattern.FindSubmatch(payload)
	if match == nil {
		return "", nil
	}
	return string(match[1]), nil
}

var entryPattern = regexp.MustCompile(`assets/(index-[A-Za-z0-9_-]+\.js)`)

// Handler 提供构建产物，并按 SPA 约定处理前端路由。
//
// 只接受 GET 与 HEAD：静态资源没有写语义，其余方法应当得到明确的 405 而不是被
// fallback 成一个 200 的 HTML。
func Handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		requested := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
		if requested == "" || requested == "." {
			serveIndex(writer, request)
			return
		}

		if serveFile(writer, request, "dist/"+requested) {
			return
		}

		// 带扩展名的路径是资源请求：缺失就是缺失，不能回落成 HTML，
		// 否则一个改名后的 chunk 会以 200 + text/html 的形式让浏览器报解析错误。
		if path.Ext(requested) != "" {
			http.NotFound(writer, request)
			return
		}

		// 无扩展名的路径属于前端路由（/channels/xxx 这类），交给 SPA 自己解析。
		serveIndex(writer, request)
	})
}

func serveFile(writer http.ResponseWriter, request *http.Request, name string) bool {
	file, err := embedded.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		return false
	}

	// Vite 给资源名加了内容哈希，因此同名文件的内容永不改变，可以长期强缓存；
	// index.html 是入口，必须每次校验，否则升级后仍会加载旧的 chunk 引用。
	if strings.HasPrefix(name, "dist/assets/") {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		writer.Header().Set("Cache-Control", "no-cache")
	}

	http.ServeContent(writer, request, path.Base(name), info.ModTime(), seeker)
	return true
}

func serveIndex(writer http.ResponseWriter, request *http.Request) {
	if !serveFile(writer, request, indexPath) {
		http.NotFound(writer, request)
	}
}

// FS 暴露嵌入的 dist 子树，供测试或其他宿主复用同一份产物。
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}
