//go:build !dev

package api

import "net/http"

const DevBuild = false

func registerDev(*handler, *http.ServeMux) {}
