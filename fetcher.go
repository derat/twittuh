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
)

// fetcher downloads resources from the web.
// It also supports caching them locally.
type fetcher struct {
	cacheDir string
	forTest  bool // if true, always read from cache and never write to cache
}

// newFetcher returns a new fetcher that will cache resources
// within the supplied directory.
func newFetcher(cacheDir string) (*fetcher, error) {
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, err
	}
	return &fetcher{cacheDir, false}, nil
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
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("server returned %q", resp.Status)
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if useCache {
		if err := ioutil.WriteFile(cp, b, 0700); err != nil {
			os.Remove(cp)
			return nil, err
		}
	}
	return b, err
}
