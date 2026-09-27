package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A store deployed without security expects an anonymous request, and must get
// one: sending an Authorization header to a store that has none configured is
// at best noise and at worst a rejected request.
func TestNoCredentialsMeansAnAnonymousRequest(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, sawAuth = r.BasicAuth()
	}))
	defer srv.Close()

	c := withStoreAuth(&http.Client{}, "", "")
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sawAuth {
		t.Error("credentials were sent to a store configured without any")
	}
}

func TestCredentialsAreSentWhenConfigured(t *testing.T) {
	var gotUser, gotPass string
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok = r.BasicAuth()
	}))
	defer srv.Close()

	c := withStoreAuth(&http.Client{}, "kgrapher", "s3cret")
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !ok || gotUser != "kgrapher" || gotPass != "s3cret" {
		t.Errorf("basic auth = (%q, %q, ok=%v); want the configured credentials", gotUser, gotPass, ok)
	}
}

// Applied as a transport so that a request added later cannot forget it: any
// method, any path, still authenticated.
func TestEveryMethodIsAuthenticated(t *testing.T) {
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok {
			seen[r.Method] = true
		}
	}))
	defer srv.Close()

	c := withStoreAuth(&http.Client{}, "u", "p")
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		req, _ := http.NewRequest(m, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !seen[m] {
			t.Errorf("%s went out unauthenticated", m)
		}
	}
}

// A RoundTripper must not modify the request it is handed; the caller may
// retry it and a redirect will reuse it.
func TestTheCallersRequestIsNotMutated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := withStoreAuth(&http.Client{}, "u", "p")
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Header.Get("Authorization") != "" {
		t.Error("the caller's own request was given an Authorization header")
	}
}
