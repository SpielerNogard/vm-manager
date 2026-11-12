package hub

import (
	_ "embed"
	"net/http"
)

//go:embed install.sh
var InstallScript []byte

// InstallScriptHandler serves the static node installer; it holds no secrets, so it needs no token.
func InstallScriptHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /node/install.sh", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript")
		w.Write(InstallScript)
	})
	return mux
}
