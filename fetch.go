// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	fetchEvalDelay = time.Second
	pageReadyExpr  = `!!document.querySelector('div[data-testid="tweet"]')`
)

// fetchTimeline fetches the timeline page for the supplied user and returns its full DOM.
func fetchTimeline(ctx context.Context, user string, width, height int,
	proxy, cacheDir string, tweetTimeout time.Duration, logDebug bool) (string, error) {
	eopts := chromedp.DefaultExecAllocatorOptions[:]
	if proxy != "" {
		eopts = append(eopts, chromedp.ProxyServer(proxy))
	}
	if cacheDir != "" {
		eopts = append(eopts, chromedp.Flag("disk-cache-dir", cacheDir))
	}
	ctx, cancel := chromedp.NewExecAllocator(ctx, eopts...)
	defer cancel()

	copts := []chromedp.ContextOption{
		chromedp.WithLogf(log.Printf),
		chromedp.WithErrorf(log.Printf),
	}
	if logDebug {
		copts = append(copts, chromedp.WithDebugf(log.Printf))
	}
	ctx, cancel = chromedp.NewContext(ctx, copts...)
	defer cancel()

	debug("Loading page")
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(userURL(user))); err != nil {
		return "", err
	}

	debug("Loading tweets")
	tctx := ctx
	if tweetTimeout > 0 {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, tweetTimeout)
		defer cancel()
	}
	for {
		var exists bool
		if err := chromedp.Run(tctx, chromedp.Evaluate(pageReadyExpr, &exists)); err != nil {
			return "", fmt.Errorf("failed checking for tweets: %v", err)
		} else if exists {
			debug("Loaded tweets")
			break
		}
		select {
		case <-tctx.Done():
			return "", fmt.Errorf("failed loading tweets: %v", tctx.Err())
		case <-time.After(fetchEvalDelay):
		}
	}

	// Return the rendered DOM.
	var data string
	err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.outerHTML`, &data))
	return data, err
}
