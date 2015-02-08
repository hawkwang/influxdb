package httpd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"compress/gzip"

	"code.google.com/p/go-uuid/uuid"

	"github.com/bmizerany/pat"
	"github.com/influxdb/influxdb"
	"github.com/influxdb/influxdb/influxql"
)

// TODO: Standard response headers (see: HeaderHandler)
// TODO: Compression (see: CompressionHeaderHandler)

// TODO: Check HTTP response codes: 400, 401, 403, 409.

type route struct {
	name        string
	method      string
	pattern     string
	handlerFunc interface{}
}

// Handler represents an HTTP handler for the InfluxDB server.
type Handler struct {
	server                *influxdb.Server
	routes                []route
	mux                   *pat.PatternServeMux
	requireAuthentication bool
}

// NewHandler returns a new instance of Handler.
func NewHandler(s *influxdb.Server, requireAuthentication bool, version string) *Handler {
	h := &Handler{
		server: s,
		mux:    pat.New(),
		requireAuthentication: requireAuthentication,
	}

	weblog := log.New(os.Stderr, `[http] `, 0)

	h.routes = append(h.routes,
		route{
			"query", // Query serving route.
			"GET", "/query", h.serveQuery,
		},
		route{
			"write", // Data-ingest route.
			"POST", "/write", h.serveWrite,
		},
		route{ // List data nodes
			"data_nodes_index",
			"GET", "/data_nodes", h.serveDataNodes,
		},
		route{ // Create data node
			"data_nodes_create",
			"POST", "/data_nodes", h.serveCreateDataNode,
		},
		route{ // Delete data node
			"data_nodes_delete",
			"DELETE", "/data_nodes/:id", h.serveDeleteDataNode,
		},
		route{ // Metastore
			"metastore",
			"GET", "/metastore", h.serveMetastore,
		},
		route{ // Status
			"status",
			"GET", "/status", h.serveStatus,
		},
		route{ // Ping
			"ping",
			"GET", "/ping", h.servePing,
		},
		route{ // Tell data node to run CQs that should be run
			"process_continuous_queries",
			"POST", "/process_continuous_queries", h.serveProcessContinuousQueries,
		},
	)

	for _, r := range h.routes {
		var handler http.Handler

		// If it's a handler func that requires authorization, wrap it in authorization
		if hf, ok := r.handlerFunc.(func(http.ResponseWriter, *http.Request, *influxdb.User)); ok {
			handler = authenticate(hf, h, requireAuthentication)
		}
		// This is a normal handler signature and does not require authorization
		if hf, ok := r.handlerFunc.(func(http.ResponseWriter, *http.Request)); ok {
			handler = http.HandlerFunc(hf)
		}

		handler = gzipFilter(handler)
		handler = versionHeader(handler, version)
		handler = cors(handler)
		handler = requestID(handler)
		handler = logging(handler, r.name, weblog)
		handler = recovery(handler, r.name, weblog) // make sure recovery is always last

		h.mux.Add(r.method, r.pattern, handler)
	}

	return h
}

//ServeHTTP responds to HTTP request to the handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// serveQuery parses an incoming query and, if valid, executes the query.
func (h *Handler) serveQuery(w http.ResponseWriter, r *http.Request, user *influxdb.User) {
	q := r.URL.Query()
	p := influxql.NewParser(strings.NewReader(q.Get("q")))
	db := q.Get("db")
	pretty := q.Get("pretty") == "true"

	// Parse query from query string.
	query, err := p.ParseQuery()
	if err != nil {
		httpError(w, "error parsing query: "+err.Error(), pretty, http.StatusBadRequest)
		return
	}

	// Execute query. One result will return for each statement.
	results := h.server.ExecuteQuery(query, db, user)

	// Send results to client.
	httpResults(w, results, pretty)
}

// serveWrite receives incoming series data and writes it to the database.
func (h *Handler) serveWrite(w http.ResponseWriter, r *http.Request, user *influxdb.User) {
	var bp influxdb.BatchPoints

	dec := json.NewDecoder(r.Body)

	var writeError = func(result influxdb.Result, statusCode int) {
		w.WriteHeader(statusCode)
		w.Header().Add("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(&result)
		return
	}

	if err := dec.Decode(&bp); err != nil {
		if err.Error() == "EOF" {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeError(influxdb.Result{Err: err}, http.StatusInternalServerError)
		return
	}

	if bp.Database == "" {
		writeError(influxdb.Result{Err: fmt.Errorf("database is required")}, http.StatusInternalServerError)
		return
	}

	if !h.server.DatabaseExists(bp.Database) {
		writeError(influxdb.Result{Err: fmt.Errorf("database not found: %q", bp.Database)}, http.StatusNotFound)
		return
	}

	if h.requireAuthentication && !user.Authorize(influxql.WritePrivilege, bp.Database) {
		writeError(influxdb.Result{Err: fmt.Errorf("%q user is not authorized to write to database %q", user.Name, bp.Database)}, http.StatusUnauthorized)
		return
	}

	points, err := influxdb.NormalizeBatchPoints(bp)
	if err != nil {
		writeError(influxdb.Result{Err: err}, http.StatusInternalServerError)
		return
	}

	if _, err := h.server.WriteSeries(bp.Database, bp.RetentionPolicy, points); err != nil {
		writeError(influxdb.Result{Err: err}, http.StatusInternalServerError)
		return
	}
}

// serveMetastore returns a copy of the metastore.
func (h *Handler) serveMetastore(w http.ResponseWriter, r *http.Request) {
	// Set headers.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="meta"`)

	if err := h.server.CopyMetastore(w); err != nil {
		httpError(w, err.Error(), false, http.StatusInternalServerError)
	}
}

// serveStatus returns a set of states that the server is currently in.
func (h *Handler) serveStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("content-type", "application/json")

	pretty := r.URL.Query().Get("pretty") == "true"

	data := struct {
		Id    uint64 `json:"id"`
		Index uint64 `json:"index"`
	}{
		Id:    h.server.ID(),
		Index: h.server.Index(),
	}
	var b []byte
	if pretty {
		b, _ = json.MarshalIndent(data, "", "    ")
	} else {
		b, _ = json.Marshal(data)
	}
	w.Write(b)
}

// servePing returns a simple response to let the client know the server is running.
func (h *Handler) servePing(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// serveDataNodes returns a list of all data nodes in the cluster.
func (h *Handler) serveDataNodes(w http.ResponseWriter, r *http.Request) {
	// Generate a list of objects for encoding to the API.
	a := make([]*dataNodeJSON, 0)
	for _, n := range h.server.DataNodes() {
		a = append(a, &dataNodeJSON{
			ID:  n.ID,
			URL: n.URL.String(),
		})
	}

	w.Header().Add("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

// serveCreateDataNode creates a new data node in the cluster.
func (h *Handler) serveCreateDataNode(w http.ResponseWriter, r *http.Request) {
	// Read in data node from request body.
	var n dataNodeJSON
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		httpError(w, err.Error(), false, http.StatusBadRequest)
		return
	}

	// Parse the URL.
	u, err := url.Parse(n.URL)
	if err != nil {
		httpError(w, "invalid data node url", false, http.StatusBadRequest)
		return
	}

	// Create the data node.
	if err := h.server.CreateDataNode(u); err == influxdb.ErrDataNodeExists {
		httpError(w, err.Error(), false, http.StatusConflict)
		return
	} else if err != nil {
		httpError(w, err.Error(), false, http.StatusInternalServerError)
		return
	}

	// Retrieve data node reference.
	node := h.server.DataNodeByURL(u)

	// Create a new replica on the broker.
	if err := h.server.Client().CreateReplica(node.ID); err != nil {
		httpError(w, err.Error(), false, http.StatusBadGateway)
		return
	}

	// Write new node back to client.
	w.WriteHeader(http.StatusCreated)
	w.Header().Add("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(&dataNodeJSON{ID: node.ID, URL: node.URL.String()})
}

// serveDeleteDataNode removes an existing node.
func (h *Handler) serveDeleteDataNode(w http.ResponseWriter, r *http.Request) {
	// Parse node id.
	nodeID, err := strconv.ParseUint(r.URL.Query().Get(":id"), 10, 64)
	if err != nil {
		httpError(w, "invalid node id", false, http.StatusBadRequest)
		return
	}

	// Delete the node.
	if err := h.server.DeleteDataNode(nodeID); err == influxdb.ErrDataNodeNotFound {
		httpError(w, err.Error(), false, http.StatusNotFound)
		return
	} else if err != nil {
		httpError(w, err.Error(), false, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// serveProcessContinuousQueries will execute any continuous queries that should be run
func (h *Handler) serveProcessContinuousQueries(w http.ResponseWriter, r *http.Request, u *influxdb.User) {
	if err := h.server.RunContinuousQueries(); err != nil {
		httpError(w, err.Error(), false, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

type dataNodeJSON struct {
	ID  uint64 `json:"id"`
	URL string `json:"url"`
}

func isAuthorizationError(err error) bool {
	_, ok := err.(influxdb.ErrAuthorize)
	return ok
}

// httpResult writes a Results array to the client.
func httpResults(w http.ResponseWriter, results influxdb.Results, pretty bool) {
	if results.Error() != nil {
		if isAuthorizationError(results.Error()) {
			w.WriteHeader(http.StatusUnauthorized)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
	w.Header().Add("content-type", "application/json")
	var b []byte
	if pretty {
		b, _ = json.MarshalIndent(results, "", "    ")
	} else {
		b, _ = json.Marshal(results)
	}
	w.Write(b)
}

// httpError writes an error to the client in a standard format.
func httpError(w http.ResponseWriter, error string, pretty bool, code int) {
	w.Header().Add("content-type", "application/json")
	w.WriteHeader(code)

	results := influxdb.Results{Err: errors.New(error)}
	var b []byte
	if pretty {
		b, _ = json.MarshalIndent(results, "", "    ")
	} else {
		b, _ = json.Marshal(results)
	}
	w.Write(b)
}

// Filters and filter helpers

// parseCredentials returns the username and password encoded in
// a request. The credentials may be present as URL query params, or as
// a Basic Authentication header.
// as params: http://127.0.0.1/query?u=username&p=password
// as basic auth: http://username:password@127.0.0.1
func parseCredentials(r *http.Request) (string, string, error) {
	q := r.URL.Query()

	if u, p := q.Get("u"), q.Get("p"); u != "" && p != "" {
		return u, p, nil
	}
	if u, p, ok := r.BasicAuth(); ok {
		return u, p, nil
	} else {
		return "", "", fmt.Errorf("unable to parse Basic Auth credentials")
	}
}

// authenticate wraps a handler and ensures that if user credentials are passed in
// an attempt is made to authenticate that user. If authentication fails, an error is returned.
//
// There is one exception: if there are no users in the system, authentication is not required. This
// is to facilitate bootstrapping of a system with authentication enabled.
func authenticate(inner func(http.ResponseWriter, *http.Request, *influxdb.User), h *Handler, requireAuthentication bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return early if we are not authenticating
		if !requireAuthentication {
			inner(w, r, nil)
			return
		}
		var user *influxdb.User

		// TODO corylanou: never allow this in the future without users
		if requireAuthentication && h.server.UserCount() > 0 {
			username, password, err := parseCredentials(r)
			if err != nil {
				httpError(w, err.Error(), false, http.StatusUnauthorized)
				return
			}
			if username == "" {
				httpError(w, "username required", false, http.StatusUnauthorized)
				return
			}

			user, err = h.server.Authenticate(username, password)
			if err != nil {
				httpError(w, err.Error(), false, http.StatusUnauthorized)
				return
			}
		}
		inner(w, r, user)
	})
}

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// determines if the client can accept compressed responses, and encodes accordingly
func gzipFilter(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			inner.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gzw := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		inner.ServeHTTP(gzw, r)
	})
}

// versionHeader taks a HTTP handler and returns a HTTP handler
// and adds the X-INFLUXBD-VERSION header to outgoing responses.
func versionHeader(inner http.Handler, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Influxdb-Version", version)
		inner.ServeHTTP(w, r)
	})
}

// cors responds to incoming requests and adds the appropriate cors headers
// TODO: corylanou: add the ability to configure this in our config
func cors(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set(`Access-Control-Allow-Origin`, origin)
			w.Header().Set(`Access-Control-Allow-Methods`, strings.Join([]string{
				`DELETE`,
				`GET`,
				`OPTIONS`,
				`POST`,
				`PUT`,
			}, ", "))

			w.Header().Set(`Access-Control-Allow-Headers`, strings.Join([]string{
				`Accept`,
				`Accept-Encoding`,
				`Authorization`,
				`Content-Length`,
				`Content-Type`,
				`X-CSRF-Token`,
				`X-HTTP-Method-Override`,
			}, ", "))
		}

		if r.Method == "OPTIONS" {
			return
		}

		inner.ServeHTTP(w, r)
	})
}

func requestID(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := uuid.NewUUID()
		r.Header.Set("Request-Id", uid.String())
		w.Header().Set("Request-Id", r.Header.Get("Request-Id"))

		inner.ServeHTTP(w, r)
	})
}

func logging(inner http.Handler, name string, weblog *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		l := &responseLogger{w: w}
		inner.ServeHTTP(l, r)
		logLine := buildLogLine(l, r, start)
		weblog.Println(logLine)
	})
}

func recovery(inner http.Handler, name string, weblog *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		l := &responseLogger{w: w}
		inner.ServeHTTP(l, r)
		if err := recover(); err != nil {
			logLine := buildLogLine(l, r, start)
			logLine = fmt.Sprintf(`%s [err:%s]`, logLine, err)
			weblog.Println(logLine)
		}
	})
}
