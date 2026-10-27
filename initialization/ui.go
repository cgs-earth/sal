package initialization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	"github.com/cgs-earth/sal/serve"
)

// moduleInitServer answers the module init form `sal init --salmodule`
// opens in the browser. The form assembles ontology.jsonld and asks the server
// to check it as it is edited and to write it once; the server is also what
// tells the form whether the module is already initialized, which it reads
// from disk on every request so that deleting ontology.jsonld unlocks the
// form again without a restart.
type moduleInitServer struct {
	// dir is the module root, where ontology.jsonld is written.
	dir string
	// namespace is the module's own salmodule:// namespace, which the checks
	// resolve the ontology's relative terms against.
	namespace string
	// title and creator seed the form's ontology node, from the git remote
	// and user.name like the prompts do.
	title   string
	creator string
	// written is called after ontology.jsonld has been written, so the
	// command can say so in its log.
	written func()
}

// moduleInitStatus is what GET /api/init/status answers.
type moduleInitStatus struct {
	// Initialized reports whether ontology.jsonld exists in the module root.
	Initialized  bool   `json:"initialized"`
	OntologyPath string `json:"ontologyPath"`
	Namespace    string `json:"namespace"`
	Title        string `json:"title"`
	Creator      string `json:"creator"`
}

// moduleProblem is one problem with the document the form assembled, on the
// line of the document the form showed, so the form can mark it there.
type moduleProblem struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// ontologyRequest is the body of a validate or write request: the exact text
// the form would write, so that the lines problems are reported on are the
// lines the form shows.
type ontologyRequest struct {
	Document string `json:"document"`
}

// maxOntologyRequest bounds a request body; an ontology document is a few
// kilobytes, and nothing legitimate approaches this.
const maxOntologyRequest = 4 << 20

// handler routes the form's JSON endpoints and serves the embedded page for
// everything else.
func (s *moduleInitServer) handler() (http.Handler, error) {
	page, err := serve.ModuleInitUI()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/init/status", s.status)
	mux.HandleFunc("POST /api/init/validate", s.validate)
	mux.HandleFunc("POST /api/init/write", s.write)
	mux.Handle("/", page)
	return mux, nil
}

func (s *moduleInitServer) ontologyPath() string {
	return filepath.Join(s.dir, salmodule.OntologyFile)
}

func (s *moduleInitServer) initialized() bool {
	info, err := os.Stat(s.ontologyPath())
	return err == nil && !info.IsDir()
}

func (s *moduleInitServer) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, moduleInitStatus{
		Initialized:  s.initialized(),
		OntologyPath: s.ontologyPath(),
		Namespace:    s.namespace,
		Title:        s.title,
		Creator:      s.creator,
	})
}

// readDocument reads the document a validate or write request carries,
// answering the request itself when it cannot.
func readDocument(w http.ResponseWriter, r *http.Request) (string, bool) {
	defer func() {
		if err := r.Body.Close(); err != nil {
			slog.Error("failed to close the request body", "error", err)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxOntologyRequest))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read the request: "+err.Error())
		return "", false
	}
	var request ontologyRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "the request body must be a JSON object with a \"document\" string: "+err.Error())
		return "", false
	}
	if strings.TrimSpace(request.Document) == "" {
		writeJSONError(w, http.StatusBadRequest, "the request names no document")
		return "", false
	}
	return request.Document, true
}

// problems runs every check `sal validate --salmodule` would over the
// document, reported against the lines of the document itself.
func (s *moduleInitServer) problems(document string) []moduleProblem {
	problems := []moduleProblem{}
	for _, err := range salmodule.CheckOntologyDocument(salmodule.OntologyFile, []byte(document), s.namespace) {
		var problem salmodule.ModuleError
		if errors.As(err, &problem) {
			problems = append(problems, moduleProblem{Line: problem.Line, Message: problem.Message})
			continue
		}
		problems = append(problems, moduleProblem{Message: err.Error()})
	}
	return problems
}

func (s *moduleInitServer) validate(w http.ResponseWriter, r *http.Request) {
	document, ok := readDocument(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"problems": s.problems(document)})
}

// write writes the document as ontology.jsonld, with the Dockerfile,
// .dockerignore, and editor schema `sal init --salmodule` scaffolds next to
// it, once the document passes every check. A module that already has an
// ontology.jsonld is refused: the form is for creating one, not editing it.
func (s *moduleInitServer) write(w http.ResponseWriter, r *http.Request) {
	document, ok := readDocument(w, r)
	if !ok {
		return
	}
	if s.initialized() {
		writeJSONError(w, http.StatusConflict, salmodule.OntologyFile+" already exists in "+s.dir+"; delete it to create a new one")
		return
	}
	if problems := s.problems(document); len(problems) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		if err := json.NewEncoder(w).Encode(map[string]any{"error": "the ontology has problems that must be fixed before it is written", "problems": problems}); err != nil {
			slog.Error("failed to write JSON response", "error", err)
		}
		return
	}
	if err := writeSalModuleFiles(s.dir, document); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := writeEditorSchema(s.dir); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.written != nil {
		s.written()
	}
	writeJSON(w, map[string]string{"ontologyPath": s.ontologyPath()})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to write JSON response", "error", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Error("failed to write JSON error response", "error", err)
	}
}

// openBrowser opens url in the user's browser; tests replace it.
var openBrowser = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// serveModuleInitUI serves the module init form on a localhost port until the
// process is interrupted, opening it in the browser. The form writes
// ontology.jsonld, so the server keeps running once it has, showing the
// module as initialized, until the user stops it.
func serveModuleInitUI(dir string) error {
	remote, err := pkg.DefaultGitRemote()
	if err != nil {
		return err
	}
	namespace, err := salmodule.ModuleNamespaceForRemote(remote)
	if err != nil {
		return err
	}
	title, err := pkg.GitProjectName()
	if err != nil {
		return err
	}
	creator, err := pkg.GitUserName()
	if err != nil {
		return err
	}
	server := &moduleInitServer{
		dir:       dir,
		namespace: namespace,
		title:     title,
		creator:   creator,
		written: func() {
			slog.Info("SAL module initialized at " + dir + "; press Ctrl+C to stop the form")
		},
	}
	handler, err := server.handler()
	if err != nil {
		return err
	}
	if server.initialized() {
		slog.Warn(salmodule.OntologyFile + " already exists; the form stays locked until it is deleted")
	}

	// an ephemeral port, so that the form never collides with a `sal serve`
	// on 8080 or with a second module being initialized at the same time
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for the module init form: %w", err)
	}
	url := "http://" + listener.Addr().String() + "/"
	slog.Info("Fill in the SAL module ontology at " + url + " (press Ctrl+C to stop)")
	if err := openBrowser(url); err != nil {
		slog.Warn("could not open the browser; open the address yourself", "error", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if !server.initialized() {
		slog.Warn("the form was closed before " + salmodule.OntologyFile + " was written; run `sal init --salmodule` again to create it")
	}
	return nil
}
