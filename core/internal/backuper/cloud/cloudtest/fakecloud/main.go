// Command fakecloud runs the fake Yandex Disk as a standalone server, for
// end-to-end checks of the real helper and plugin without a real account.
//
// It prints two lines — the API address and an access token — and serves
// until killed. GET /__files lists what has been uploaded.
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/cloudtest"
)

func main() {
	y := cloudtest.NewYandex()
	inner := y.Server.Config.Handler
	y.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__files" {
			inner.ServeHTTP(w, r)
			return
		}
		type file struct {
			Path string `json:"path"`
			Size int    `json:"size"`
			MD5  string `json:"md5"`
		}
		out := []file{}
		for _, p := range y.Paths() {
			data, _ := y.File(p)
			sum := md5.Sum(data)
			out = append(out, file{p, len(data), hex.EncodeToString(sum[:])})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	fmt.Println(y.Server.URL)
	fmt.Println(y.Token())
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
