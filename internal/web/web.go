package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:assets
var files embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(files, "assets")
	server := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "media" || strings.HasPrefix(name, "media/") || strings.HasPrefix(name, "api/") || name == "admin" || strings.HasPrefix(name, "admin/") {
			http.NotFound(w, r)
			return
		}
		if _, err := fs.Stat(root, name); err == nil && name != "" && name != "." {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public,max-age=31536000,immutable")
			}
			server.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "Frontend build unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

func Check() error {
	_, err := fs.ReadFile(files, "assets/index.html")
	return err
}
