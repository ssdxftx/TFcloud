package webui

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Handler 返回前端静态资源处理器：命中文件直接返回，未命中回退到 index.html（SPA）。
//
// 资源来源优先级：
//  1. distDir 指定且目录里有 index.html —— 便于本地改前端后直接跑；
//  2. 编译期嵌入的 dist（-tags embedui，正式发布用）；
//  3. 都没有时返回一段说明页，而不是空白 404。
func Handler(distDir string) http.Handler {
	var root fs.FS
	if dir := pickDistDir(distDir); dir != "" {
		root = os.DirFS(dir)
	} else if embedded != nil {
		if sub, err := fs.Sub(embedded, "dist"); err == nil {
			root = sub
		}
	}
	if root == nil {
		return placeholderHandler{}
	}
	return &spaHandler{root: root}
}

// pickDistDir 返回第一个真正含有 index.html 的目录。
func pickDistDir(distDir string) string {
	candidates := []string{distDir, "internal/webui/dist", "dist", "/app/dist"}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !info.IsDir() {
			return dir
		}
	}
	return ""
}

// placeholderHandler 在没有任何前端产物时给出可读提示。
type placeholderHandler struct{}

func (placeholderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>天方云签</title><style>body{font:14px/1.7 system-ui,sans-serif;max-width:640px;margin:12vh auto;padding:0 24px;color:#333}
code{background:#f4f4f5;padding:2px 6px;border-radius:4px}</style></head><body>
<h2>前端资源尚未构建</h2>
<p>后端已正常运行，但没有找到前端产物。任选一种方式：</p>
<ul>
<li><code>make ui</code> 构建前端后重跑</li>
<li><code>make build</code> 一步构建（前端 + 内嵌二进制的二进制）</li>
<li><code>docker compose up -d --build</code> 用镜像启动</li>
</ul>
<p>接口仍可用，例如 <code>/api/site</code>、<code>/health/ready</code>。</p>
</body></html>`))
}

type spaHandler struct {
	root fs.FS
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if strings.Contains(upath, "..") {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(upath, "/")
	if rel == "" {
		h.serveIndex(w, r)
		return
	}
	if info, err := fs.Stat(h.root, rel); err == nil && !info.IsDir() {
		h.serveFile(w, r, rel, info)
		return
	}
	// 带扩展名的资源缺失直接 404，避免把 index.html 当成 js/css 返回
	if filepath.Ext(rel) != "" {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r)
}

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(h.root, "index.html")
	if err != nil {
		http.Error(w, "前端资源缺失", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo) {
	data, err := fs.ReadFile(h.root, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "fonts/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeContent(w, r, name, info.ModTime(), strings.NewReader(string(data)))
}
