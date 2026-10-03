package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/ident"
	"github.com/4fuu/box/internal/store"
)

const gateCookie = "box_token"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	switch host {
	case ident.Hostname("event", s.svc.Domain):
		s.serveEvent(w, r)
		return
	case ident.Hostname("auth", s.svc.Domain):
		s.serveAuth(w, r)
		return
	}
	p, err := s.store.PortalByHost(host)
	if err != nil {
		w.WriteHeader(http.StatusMisdirectedRequest)
		_, _ = io.WriteString(w, "unknown host\n")
		return
	}
	s.svc.Metrics.Request()
	if p.Private {
		if _, ok := s.presented(r); !ok {
			s.svc.Metrics.Denied()
			if wantsHTML(r) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				http.Redirect(w, r, s.authURL(r), http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "unauthorized\n")
			return
		}
	}
	s.proxyPortal(w, r, p)
}

func (s *Server) serveEvent(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.presented(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized\n")
		return
	}
	// A client never names its own from: it comes from the token.
	from := event.ParseFrom(tok.Comment, tok.ID)
	tokenID := tok.ID
	switch {
	case r.URL.Path == "/api/events" && r.Method == http.MethodGet:
		s.eventRead(w, r)
	case r.URL.Path == "/api/events" && r.Method == http.MethodPost:
		s.eventPostJSON(w, r, from, tokenID)
	case strings.HasPrefix(r.URL.Path, "/api/events/") && r.Method == http.MethodPost:
		s.eventPostRaw(w, r, from, tokenID)
	case r.URL.Path == "/api/events" || strings.HasPrefix(r.URL.Path, "/api/events/"):
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method\n", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

// eventCtx ends a read when the client leaves or the server shuts down.
func (s *Server) eventCtx(r *http.Request) (context.Context, context.CancelFunc, error) {
	select {
	case <-s.done:
		return nil, nil, errShuttingDown
	default:
	}
	ctx, cancel := context.WithCancel(r.Context())
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel, nil
}

var errShuttingDown = errors.New("shutting down")

func (s *Server) eventRead(w http.ResponseWriter, r *http.Request) {
	q, err := parseEventQuery(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error()+"\n", http.StatusBadRequest)
		return
	}
	ctx, cancel, err := s.eventCtx(r)
	if err != nil {
		http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
		return
	}
	defer cancel()
	res, err := s.svc.ReadEvents(ctx, q)
	if err != nil {
		// Canceled means the client or the server went away mid-wait and
		// there is no one left to tell.
		if errors.Is(err, context.Canceled) {
			return
		}
		http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// parseEventQuery reads since, topic (repeatable), from (repeatable),
// limit (1–1000, default 100), and wait (0–25 whole seconds).
func parseEventQuery(v url.Values) (event.Query, error) {
	q := event.Query{Limit: event.DefaultLimit}
	since, err := parseSince(v.Get("since"))
	if err != nil {
		return q, errBadSince
	}
	q.Since = since
	q.Topics = v["topic"]
	for _, f := range v["from"] {
		if !event.ValidFrom(f) {
			return q, errBadFrom
		}
	}
	if _, err := event.ParseFilters(q.Topics); err != nil {
		return q, errBadTopic
	}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > event.MaxLimit {
			return q, errBadLimit
		}
		q.Limit = n
	}
	if raw := v.Get("wait"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > event.MaxWaitSeconds {
			return q, errBadWait
		}
		q.Wait = time.Duration(n) * time.Second
	}
	return q, nil
}

var (
	errBadSince = errors.New("invalid since")
	errBadFrom  = errors.New("invalid from")
	errBadTopic = errors.New("invalid topic filter")
	errBadLimit = errors.New("invalid limit")
	errBadWait  = errors.New("invalid wait")
)

// eventEnvelopeMax bounds one JSON POST envelope. A 1 MiB body is ~1.4 MiB
// base64 and up to ~6 MiB as escaped JSON text; anything larger is a mistake.
const eventEnvelopeMax = 8 << 20

func (s *Server) eventPostJSON(w http.ResponseWriter, r *http.Request, from string, tokenID int64) {
	r.Body = http.MaxBytesReader(w, r.Body, eventEnvelopeMax)
	var req struct {
		Topic   string  `json:"topic"`
		Body    *string `json:"body"`
		BodyB64 *string `json:"body_b64"`
		Key     string  `json:"key"`
		From    string  `json:"from"` // ignored: from comes from the token
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeEventError(w, err)
		return
	}
	var body []byte
	switch {
	case req.Body != nil && req.BodyB64 != nil:
		http.Error(w, "one body field\n", http.StatusBadRequest)
		return
	case req.Body != nil:
		body = []byte(*req.Body)
	case req.BodyB64 != nil:
		b, err := base64.StdEncoding.DecodeString(*req.BodyB64)
		if err != nil {
			http.Error(w, "invalid body_b64\n", http.StatusBadRequest)
			return
		}
		body = b
	}
	s.eventStore(w, from, tokenID, req.Topic, body, req.Key)
}

func (s *Server) eventPostRaw(w http.ResponseWriter, r *http.Request, from string, tokenID int64) {
	topic := strings.TrimPrefix(r.URL.Path, "/api/events/")
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	// The request body is stored verbatim; only its size is checked.
	body, err := io.ReadAll(io.LimitReader(r.Body, event.MaxBody+1))
	if err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	s.eventStore(w, from, tokenID, topic, body, key)
}

func (s *Server) eventStore(w http.ResponseWriter, from string, tokenID int64, topic string, body []byte, key string) {
	if len(body) > event.MaxBody {
		http.Error(w, "body over 1 MiB\n", http.StatusRequestEntityTooLarge)
		return
	}
	item, dup, err := s.svc.PublishEvent(from, &tokenID, topic, body, key)
	if err != nil {
		writeEventError(w, err)
		return
	}
	status := http.StatusCreated
	if dup {
		status = http.StatusOK
	}
	writeJSON(w, status, struct {
		ID        int64 `json:"id"`
		Duplicate bool  `json:"duplicate,omitempty"`
	}{ID: item.ID, Duplicate: dup})
}

// writeEventError maps one publish or envelope failure to a short answer.
func writeEventError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr), errors.Is(err, event.ErrBodyTooLarge):
		http.Error(w, "body over 1 MiB\n", http.StatusRequestEntityTooLarge)
	case errors.Is(err, event.ErrInvalidTopic):
		http.Error(w, "invalid topic\n", http.StatusBadRequest)
	case errors.Is(err, event.ErrInvalidKey):
		http.Error(w, "invalid key\n", http.StatusBadRequest)
	case errors.Is(err, event.ErrInvalidFrom):
		http.Error(w, "invalid from\n", http.StatusBadRequest)
	default:
		http.Error(w, "bad request\n", http.StatusBadRequest)
	}
}

func (s *Server) serveAuth(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeAuth(w, http.StatusOK, r.URL.Query().Get("next"), "")
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request\n", http.StatusBadRequest)
			return
		}
		raw := r.PostForm.Get("token")
		next := r.PostForm.Get("next")
		tok, ok, err := s.store.ValidToken(raw)
		if err != nil {
			http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
			return
		}
		if !ok {
			s.writeAuth(w, http.StatusUnauthorized, next, "rejected")
			return
		}
		s.setGateCookie(w, r, tok)
		if dest := safeNext(s.svc.Domain, next); dest != "" {
			http.Redirect(w, r, dest, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "signed in\n")
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method\n", http.StatusMethodNotAllowed)
	}
}

func (s *Server) writeAuth(w http.ResponseWriter, status int, next, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, authPage(next, message))
}

func authPage(next, message string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\"><title>box</title></head><body>\n")
	b.WriteString("<h1>box</h1>\n<p>Enter a token to open private portals.</p>\n")
	if message != "" {
		b.WriteString("<p>")
		b.WriteString(template.HTMLEscapeString(message))
		b.WriteString("</p>\n")
	}
	b.WriteString("<form method=\"post\" action=\"/\">\n<input type=\"hidden\" name=\"next\" value=\"")
	b.WriteString(template.HTMLEscapeString(next))
	b.WriteString("\">\n<label>token <input name=\"token\" type=\"password\" autocomplete=\"current-password\"></label>\n")
	b.WriteString("<button type=\"submit\">continue</button>\n</form>\n</body></html>\n")
	return b.String()
}

func (s *Server) setGateCookie(w http.ResponseWriter, r *http.Request, tok store.Token) {
	c := &http.Cookie{
		Name:     gateCookie,
		Value:    tok.Token,
		Path:     "/",
		Domain:   s.svc.Domain,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestSecure(r),
	}
	if tok.Expiry.IsZero() {
		// Browsers drop a cookie after 400 days. The token itself stays valid.
		c.MaxAge = 400 * 24 * 60 * 60
	} else {
		c.Expires = tok.Expiry
		if sec := int(tok.Expiry.Sub(s.store.Now()).Seconds()); sec > 0 {
			c.MaxAge = sec
		}
	}
	http.SetCookie(w, c)
}

func (s *Server) authURL(r *http.Request) string {
	host := ident.Hostname("auth", s.svc.Domain)
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" && port != "80" && port != "443" {
		host = net.JoinHostPort(host, port)
	}
	next := publicURL(r)
	return requestScheme(r) + "://" + host + "/?next=" + url.QueryEscape(next)
}

func publicURL(r *http.Request) string {
	return requestScheme(r) + "://" + r.Host + r.URL.RequestURI()
}

func requestScheme(r *http.Request) string {
	if requestSecure(r) {
		return "https"
	}
	return "http"
}

func requestSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// safeNext accepts an absolute URL on this server's domain. Anything else is empty.
func safeNext(domain, raw string) string {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\\") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != domain && !strings.HasSuffix(host, "."+domain) {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.RequestURI()
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (s *Server) presented(r *http.Request) (store.Token, bool) {
	raw := strings.TrimSpace(r.Header.Get("X-Box-Token"))
	if raw == "" {
		raw = bearer(r)
	}
	if raw == "" {
		if c, err := r.Cookie(gateCookie); err == nil {
			raw = c.Value
		}
	}
	if raw == "" {
		return store.Token{}, false
	}
	tok, ok, err := s.store.ValidToken(raw)
	if err != nil || !ok {
		return store.Token{}, false
	}
	return tok, true
}

func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(v, "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(raw)
}

// stripGate removes the access credential before a portal request is forwarded.
func (s *Server) stripGate(r *http.Request) {
	r.Header.Del("X-Box-Token")
	if raw := bearer(r); raw != "" {
		if _, ok, err := s.store.ValidToken(raw); err == nil && ok {
			r.Header.Del("Authorization")
		}
	}
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name == gateCookie {
			continue
		}
		r.AddCookie(c)
	}
}

func parseSince(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errBadSince
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}
