package control

import (
	"net/http"

	webui "nightshift/control/web"
)

// The control-plane UI is a Vite/React SPA (control/web/) built to web/dist and
// embedded here, so `go build` still produces one self-contained binary with no
// runtime asset dependency. Rebuild the assets with `make web` (or
// `pnpm -C control/web build`) after changing anything under web/src.
//
// spaHandler serves the embedded SPA: real files (index.html, hashed assets)
// are served directly; any other path falls back to index.html so client-side
// routes (e.g. /tickets/<agent>/<id>) survive a refresh. /api/* is registered
// on more specific mux patterns and never reaches here.
func spaHandler() http.HandlerFunc {
	return webui.Handler()
}
