// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// fetcher downloads resources from the web.
// It also supports caching them locally.
type fetcher struct {
	client    *http.Client
	cacheDir  string
	forTest   bool // if true, always read from cache and never write to cache
	userAgent string
}

// newFetcher returns a new fetcher that will cache resources
// within the supplied directory.
func newFetcher(cacheDir string) (*fetcher, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, err
	}
	return &fetcher{
		client:   &http.Client{},
		cacheDir: cacheDir,
		forTest:  false,
	}, nil
}

// fetchStatusError is returned by fetch if the server returns a non-200 status.
// It implements the error interface.
type fetchStatusError struct {
	err  error
	code int
}

func (e *fetchStatusError) Error() string {
	return e.err.Error()
}

// fetch returns the contents of the supplied URL.
// If useCache is true, the contents are read from disk if possible
// and cached to disk after being downloaded otherwise.
func (ft *fetcher) fetch(u string, useCache bool) ([]byte, error) {
	cp := filepath.Join(ft.cacheDir, url.PathEscape(u))
	if useCache || ft.forTest {
		b, err := ioutil.ReadFile(cp)
		if err == nil {
			debugf("Got %v from cache", u)
			return b, nil
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		} else if ft.forTest {
			return nil, fmt.Errorf("not using network but %v doesn't exist", cp)
		}
	}

	debug("Fetching ", u)
	req, err := makeRequest(u, ft.userAgent)
	if err != nil {
		return nil, err
	}
	resp, err := ft.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, &fetchStatusError{fmt.Errorf("server returned %q", resp.Status), resp.StatusCode}
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if useCache {
		if err := ioutil.WriteFile(cp, b, 0644); err != nil {
			os.Remove(cp)
			return nil, err
		}
	}
	return b, err
}

// makeRequest creates a GET request for the supplied URL.
func makeRequest(url, userAgent string) (*http.Request, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Send different headers depending on the user agent.
	// If we don't explicitly set the User-Agent header, Go uses "Go-http-client/1.1".
	// Go also adds the Host header and "Accept-Encoding: gzip" by default.
	// Chrome sends "gzip, deflate, br", w3m sends "gzip, compress, bzip, bzip2, deflate",
	// and Lynx sends "gzip, compress, bzip2", but only gzip is handled transparently.
	var headers map[string]string
	switch {
	case strings.Contains(userAgent, "Chrome/"):
		headers = map[string]string{
			"Connection":                "keep-alive",
			"DNT":                       "1",
			"Upgrade-Insecure-Requests": "1",
			"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng," +
				"*/*;q=0.8,application/signed-exchange;v=b3;q=0.9",
			"Sec-Fetch-Site":  "none",
			"Sec-Fetch-Mode":  "navigate",
			"Sec-Fetch-User":  "?1",
			"Sec-Fetch-Dest":  "document",
			"Accept-Language": "en-US,en;q=0.9",
		}
	case strings.HasPrefix(userAgent, "w3m/"):
		headers = map[string]string{
			"Accept":          "text/html, text/*;q=0.5, image/*, application/*, audio/*, x-scheme-handler/*, inode/*",
			"Accept-Language": "en;q=1.0",
		}
	case strings.HasPrefix(userAgent, "Lynx/"):
		headers = map[string]string{
			"Accept":          "text/html, text/plain, text/sgml, text/css, */*;q=0.01",
			"Accept-Language": "en",
		}
	default:
		headers = map[string]string{}
	}

	if userAgent != "" {
		headers["User-Agent"] = userAgent
	}

	for n, v := range headers {
		req.Header.Add(n, v)
	}
	return req, nil
}
