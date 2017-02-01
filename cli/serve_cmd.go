package cli

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/inconshreveable/log15.v2"

	"context"

	"github.com/NYTimes/gziphandler"
	"github.com/gorilla/mux"
	"github.com/keegancsmith/tmpfriend"
	"sourcegraph.com/sourcegraph/sourcegraph/app"
	"sourcegraph.com/sourcegraph/sourcegraph/app/assets"
	app_router "sourcegraph.com/sourcegraph/sourcegraph/app/router"
	"sourcegraph.com/sourcegraph/sourcegraph/cli/cli"
	"sourcegraph.com/sourcegraph/sourcegraph/cli/internal/loghandlers"
	"sourcegraph.com/sourcegraph/sourcegraph/cli/internal/middleware"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/conf"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/debugserver"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/env"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/graphstoreutil"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/handlerutil"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/httptrace"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/sysreq"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/traceutil"
	"sourcegraph.com/sourcegraph/sourcegraph/services/backend"
	"sourcegraph.com/sourcegraph/sourcegraph/services/httpapi"
	"sourcegraph.com/sourcegraph/sourcegraph/services/httpapi/router"
	srclib "sourcegraph.com/sourcegraph/srclib/cli"
)

var (
	logLevel       = env.Get("SRC_LOG_LEVEL", "info", "upper log level to restrict log output to (dbug, dbug-dev, info, warn, error, crit)")
	trace          = env.Get("SRC_LOG_TRACE", "HTTP", "space separated list of trace logs to show. Options: all, HTTP, build, github")
	traceThreshold = env.Get("SRC_LOG_TRACE_THRESHOLD", "", "show traces that take longer than this")

	httpAddr  = env.Get("SRC_HTTP_ADDR", ":3080", "HTTP listen address for app and HTTP API")
	httpsAddr = env.Get("SRC_HTTPS_ADDR", ":3443", "HTTPS (TLS) listen address for app and HTTP API")

	profBindAddr = env.Get("SRC_PROF_HTTP", ":6060", "net/http/pprof http bind address")

	appURL     = env.Get("SRC_APP_URL", "http://<http-addr>", "publicly accessible URL to web app (e.g., what you type into your browser)")
	enableHSTS = env.Get("SG_ENABLE_HSTS", "false", "enable HTTP Strict Transport Security")
	corsOrigin = env.Get("CORS_ORIGIN", "", "value for the Access-Control-Allow-Origin header returned with all requests")

	certFile = env.Get("SRC_TLS_CERT", "", "certificate file for TLS")
	keyFile  = env.Get("SRC_TLS_KEY", "", "key file for TLS")

	graphstoreRoot = os.ExpandEnv(env.Get("SRC_GRAPHSTORE_ROOT", "$SGPATH/repos", "root dir, HTTP VFS (http[s]://...), or S3 bucket (s3://...) in which to store graph data"))
)

func init() {
	srclib.CacheLocalRepo = false
}

func configureAppURL() (*url.URL, error) {
	var hostPort string
	if strings.HasPrefix(httpAddr, ":") {
		// Prepend localhost if HTTP listen addr is just a port.
		hostPort = "localhost" + httpAddr
	} else {
		hostPort = httpAddr
	}
	if appURL == "" {
		appURL = "http://<http-addr>"
	}
	appURL = strings.Replace(appURL, "<http-addr>", hostPort, -1)

	u, err := url.Parse(appURL)
	if err != nil {
		return nil, err
	}

	return u, nil
}

func Main() error {
	log.SetFlags(0)
	log.SetPrefix("")

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "help", "-h", "--help":
			log.Printf("Version: %s", env.Version)
			log.Print()

			env.PrintHelp()

			log.Print()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, st := range sysreq.Check(ctx, skippedSysReqs()) {
				log.Printf("%s:", st.Name)
				if st.OK() {
					log.Print("\tOK")
					continue
				}
				if st.Skipped {
					log.Print("\tSkipped")
					continue
				}
				if st.Problem != "" {
					log.Print("\t" + st.Problem)
				}
				if st.Err != nil {
					log.Printf("\tError: %s", st.Err)
				}
				if st.Fix != "" {
					log.Printf("\tPossible fix: %s", st.Fix)
				}
			}

			return nil
		}
	}

	cleanup := tmpfriend.SetupOrNOOP()
	defer cleanup()

	logHandler := log15.StderrHandler

	// We have some noisey debug logs, so to aid development we have a
	// special dbug level which excludes the noisey logs
	if logLevel == "dbug-dev" {
		logLevel = "dbug"
		logHandler = log15.FilterHandler(loghandlers.NotNoisey, logHandler)
	}

	// Filter trace logs
	d, _ := time.ParseDuration(traceThreshold)
	logHandler = log15.FilterHandler(loghandlers.Trace(strings.Fields(trace), d), logHandler)

	// Filter log output by level.
	lvl, err := log15.LvlFromString(logLevel)
	if err != nil {
		return err
	}
	log15.Root().SetHandler(log15.LvlFilterHandler(lvl, logHandler))

	traceutil.InitTracer()

	// Don't proceed if system requirements are missing, to avoid
	// presenting users with a half-working experience.
	if err := checkSysReqs(context.Background(), os.Stderr); err != nil {
		return err
	}

	log15.Debug("GraphStore", "at", graphstoreRoot)

	for _, f := range cli.ServeInit {
		f()
	}

	if profBindAddr != "" {
		go debugserver.Start(profBindAddr)
		log15.Debug("Profiler available", "on", fmt.Sprintf("%s/pprof", profBindAddr))
	}

	app.Init()

	conf.AppURL, err = configureAppURL()
	if err != nil {
		return err
	}

	backend.SetGraphStore(graphstoreutil.New(graphstoreRoot, nil))

	sm := http.NewServeMux()
	newRouter := func() *mux.Router {
		router := mux.NewRouter()
		// httpctx.Base will clear the context for us
		router.KeepContext = true
		return router
	}
	subRouter := func(r *mux.Route) *mux.Router {
		router := r.Subrouter()
		// httpctx.Base will clear the context for us
		router.KeepContext = true
		return router
	}
	sm.Handle("/.api/", gziphandler.GzipHandler(httpapi.NewHandler(router.New(subRouter(newRouter().PathPrefix("/.api/"))))))
	sm.Handle("/", gziphandler.GzipHandler(handlerutil.NewHandlerWithCSRFProtection(app.NewHandler(app_router.New(newRouter())))))
	assets.Mount(sm)

	if (certFile != "" || keyFile != "") && httpsAddr == "" {
		return errors.New("HTTPS listen address must be specified if TLS cert and key are set")
	}
	useTLS := certFile != "" || keyFile != ""

	if useTLS && conf.AppURL.Scheme == "http" {
		log15.Warn("TLS is enabled but app url scheme is http", "appURL", conf.AppURL)
	}

	if !useTLS && conf.AppURL.Scheme == "https" {
		log15.Warn("TLS is disabled but app url scheme is https", "appURL", conf.AppURL)
	}

	var h http.Handler = sm
	h = middleware.SourcegraphComGoGetHandler(h)
	h = middleware.BlackHole(h)
	h = httptrace.Middleware(h)
	h = (func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// headers for security
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			w.Header().Set("X-Frame-Options", "DENY")
			if v, _ := strconv.ParseBool(enableHSTS); v {
				w.Header().Set("Strict-Transport-Security", "max-age=8640000")
			}

			// no cache by default
			w.Header().Set("Cache-Control", "no-cache, max-age=0")

			// CORS
			if corsOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", corsOrigin)
			}

			next.ServeHTTP(w, r)
		})
	})(h)

	srv := &http.Server{
		Handler:      h,
		ReadTimeout:  75 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	// Start HTTP server.
	if httpAddr != "" {
		l, err := net.Listen("tcp", httpAddr)
		if err != nil {
			return err
		}

		log15.Debug("HTTP running", "on", httpAddr)
		go func() { log.Fatal(srv.Serve(l)) }()
	}

	// Start HTTPS server.
	if useTLS && httpsAddr != "" {
		l, err := net.Listen("tcp", httpsAddr)
		if err != nil {
			return err
		}

		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return err
		}

		l = tls.NewListener(l, &tls.Config{
			NextProtos:   []string{"h2"},
			Certificates: []tls.Certificate{cert},
		})

		log15.Debug("HTTPS running", "on", httpsAddr)
		go func() { log.Fatal(srv.Serve(l)) }()
	}

	// Connection test
	log15.Info(fmt.Sprintf("✱ Sourcegraph running at %s", appURL))

	select {}
}
