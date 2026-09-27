package server

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
	if p.Private {
		if _, ok := s.presented(r); !ok {
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
	if r.URL.Path != "/api/events" {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.presented(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized\n")
		return
	}
	switch r.Method {
	case http.MethodGet:
		since, err := parseSince(r.URL.Query().Get("since"))
		if err != nil {
			http.Error(w, "invalid since\n", http.StatusBadRequest)
			return
		}
		list, err := s.svc.ReadEvents(since, r.URL.Query().Get("topic"))
		if err != nil {
			http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Events []event.Item `json:"events"`
		}{Events: list})
	case http.MethodPost:
		var req struct {
			Topic string `json:"topic"`
			Body  string `json:"body"`
			From  string `json:"from"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, event.MaxBody+512)
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "bad request\n", http.StatusBadRequest)
			return
		}
		from := strings.TrimSpace(req.From)
		if from == "" {
			from = "http"
		}
		item, err := s.svc.PublishEvent(from, req.Topic, req.Body)
		if err != nil {
			http.Error(w, "bad request\n", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, item)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method\n", http.StatusMethodNotAllowed)
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

var errBadSince = errors.New("invalid since")
