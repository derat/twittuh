// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	hasTweetExpr       = `!!document.querySelector('div[data-testid="tweet"]')`
	hasTweetCheckDelay = time.Second // time to sleep between running hasTweetExpr

	loadFailedExpr = `!!Array.from(document.querySelectorAll('div[role="button"]'))` +
		`.find(e => e.innerText === 'Try again')`
	protectedExpr = `!!Array.from(document.querySelectorAll('span'))` +
		`.find(e => e.innerText === 'These Tweets are protected')`

	showSensitiveExpr = `Array.from(document.querySelectorAll('article div[role=button]'))` +
		`.filter(e => e.innerText === 'View').map(e => e.click() || true).length`
)

type fetchOptions struct {
	width, height      int
	proxy, cacheDir    string
	tweetTimeout       time.Duration
	pageSettleDelay    time.Duration
	showSensitive      bool
	showSensitiveDelay time.Duration
	logDebug           bool
}

// errTweetsProtected is returned by fetchTimeline if tweets cannot be loaded because
// the user limited their account to followers.
var errTweetsProtected = errors.New("tweets are protected")

// fetchTimeline fetches the timeline page for the supplied user and returns its full DOM.
func fetchTimeline(ctx context.Context, user string, opts fetchOptions) (string, error) {
	eopts := chromedp.DefaultExecAllocatorOptions[:]
	if opts.proxy != "" {
		eopts = append(eopts, chromedp.ProxyServer(opts.proxy))
	}
	if opts.cacheDir != "" {
		eopts = append(eopts, chromedp.Flag("disk-cache-dir", opts.cacheDir))
	}
	ctx, cancel := chromedp.NewExecAllocator(ctx, eopts...)
	defer cancel()

	copts := []chromedp.ContextOption{
		chromedp.WithLogf(log.Printf),
		chromedp.WithErrorf(log.Printf),
	}
	if opts.logDebug {
		copts = append(copts, chromedp.WithDebugf(log.Printf))
	}
	ctx, cancel = chromedp.NewContext(ctx, copts...)
	defer cancel()

	debug("Loading page")
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(opts.width), int64(opts.height)),
		chromedp.Navigate(userURL(user))); err != nil {
		return "", err
	}

	debug("Waiting for tweets")
	tctx := ctx
	if opts.tweetTimeout > 0 {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, opts.tweetTimeout)
		defer cancel()
	}
	for {
		// The tctx.Err checks here are ugly, but we want to avoid returning other
		// misleading errors when the core problem was the deadline being reached.
		var exists bool
		if err := chromedp.Run(tctx, chromedp.Evaluate(hasTweetExpr, &exists)); err != nil && tctx.Err() == nil {
			return "", fmt.Errorf("failed checking for tweets: %v", err)
		} else if exists {
			debug("Found tweets")
			break
		}

		if tctx.Err() == nil {
			var failed bool
			if err := chromedp.Run(tctx, chromedp.Evaluate(loadFailedExpr, &failed)); err != nil && tctx.Err() == nil {
				return "", fmt.Errorf("failed checking if load failed: %v", err)
			} else if failed {
				return "", errors.New("didn't receive tweets (rate-limited?)")
			}
		}

		if tctx.Err() == nil {
			var protected bool
			if err := chromedp.Run(tctx, chromedp.Evaluate(protectedExpr, &protected)); err != nil && tctx.Err() == nil {
				return "", fmt.Errorf("failed checking if tweets are protected: %v", err)
			} else if protected {
				return "", errTweetsProtected
			}
		}

		select {
		case <-tctx.Done():
			return "", fmt.Errorf("failed loading tweets: %v", tctx.Err())
		case <-time.After(hasTweetCheckDelay):
		}
	}

	// This is a hack, but wait a bit longer after the first tweet shows up in the hope that
	// additional content (e.g. more tweets and link cards in embeds) will appear.
	if dl, ok := ctx.Deadline(); !ok || time.Now().Add(opts.pageSettleDelay).Before(dl) {
		debug("Waiting for page to settle")
		time.Sleep(opts.pageSettleDelay)
	}

	if opts.showSensitive {
		debug("Showing sensitive content")
		var cnt int
		if err := chromedp.Run(ctx, chromedp.Evaluate(showSensitiveExpr, &cnt)); err != nil {
			return "", fmt.Errorf("failed showing sensitive content: %v", err)
		}
		if cnt > 0 {
			debugf("Showed %d piece(s) of sensitive content", cnt)
			if dl, ok := ctx.Deadline(); !ok || time.Now().Add(opts.showSensitiveDelay).Before(dl) {
				debug("Waiting for sensitive content")
				time.Sleep(opts.showSensitiveDelay)
			}
		}
	}

	// Return the rendered DOM.
	var data string
	err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.outerHTML`, &data))
	return data, err
}
