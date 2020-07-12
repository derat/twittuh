// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/gorilla/feeds"
)

// feedFormat describes different feed formats that can be written.
type feedFormat string

const (
	atomFormat feedFormat = "atom"
	jsonFormat feedFormat = "json"
	rssFormat  feedFormat = "rss"
)

const (
	baseFetchURL = "https://mobile.twitter.com/" // base timeline URL to fetch
	titleLen     = 80                            // max length of title text in feed
)

var verbose = false // enable verbose logging

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <user> <file>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	cacheDir := flag.String("cache-dir", filepath.Join(os.Getenv("HOME"), ".cache/twittuh"), "Directory for caching downloads")
	debugFile := flag.String("debug-file", "", "HTML timeline file to parse for debugging")
	force := flag.Bool("force", false, "Download and write feed even if there are no new tweets")
	formatFlag := flag.String("format", "atom", `Feed format to write ("atom", "json", "rss")`)
	maxRequests := flag.Int("max-requests", 3, "Maximum number of HTTP requests to make to Twitter")
	replies := flag.Bool("replies", false, "Include the user's replies")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")
	flag.Parse()

	ft, err := newFetcher(*cacheDir)
	if err != nil {
		log.Fatal("Failed initializing fetcher: ", err)
	}

	if *debugFile != "" {
		if err := debugParse(ft, *debugFile, *replies); err != nil {
			log.Fatal("Failed reading timeline: ", err)
		}
		os.Exit(0)
	}

	if len(flag.Args()) != 2 {
		flag.Usage()
		os.Exit(2)
	}
	user := bareUser(flag.Arg(0))
	feedPath := flag.Arg(1)
	format := feedFormat(*formatFlag)

	var oldMaxID int64
	if !*force {
		if oldMaxID, err = getMaxID(feedPath, format); err != nil {
			log.Printf("Couldn't get old max ID from %v: %v", feedPath, err)
		}
	}

	debugf("Getting timeline for %v with old max ID %v", user, oldMaxID)
	prof, tweets, err := getTimeline(ft, user, oldMaxID, *maxRequests)
	if err == errUnchanged {
		debug("No new tweets; exiting without writing feed")
		os.Exit(0)
	} else if err == errPossibleGap {
		log.Print("Possible gap in tweets (run more frequently or increase -max-requests)")
	} else if err != nil {
		log.Fatalf("Failed getting tweets for %v: %v", user, err)
	}

	// Write to a temp file and then replace the feed atomically to preserve the old version if
	// something goes wrong.
	f, err := ioutil.TempFile(filepath.Dir(feedPath), "."+filepath.Base(feedPath)+".")
	if err != nil {
		log.Fatal("Failed creating feed file: ", err)
	}
	defer os.Remove(f.Name()) // fails if we successfully rename temp file
	if err := writeFeed(f, format, prof, tweets, user, *replies); err != nil {
		f.Close()
		log.Fatal("Failed writing feed: ", err)
	}
	if err := f.Close(); err != nil {
		log.Fatal("Failed closing feed file: ", err)
	}
	if err := os.Rename(f.Name(), feedPath); err != nil {
		log.Fatal("Failed replacing feed file: ", err)
	}
}

var (
	errUnchanged   = errors.New("no new tweets")
	errPossibleGap = errors.New("possible gap in tweets")
)

// getTweets downloads and returns tweets from the supplied user's timeline.
// At most maxRequests will be issued to Twitter.
//
// If the ID of the latest tweet matches oldMaxID, errUnchanged is returned
// alongside an empty slice. This indicates that no new Tweets are present.
//
// If there is a possible gap between the tweets returned by the last invocation
// and the tweets returned by this invocation, errPossibleGap is returned
// alongside all the tweet that were fetched.
func getTimeline(ft *fetcher, user string, oldMaxID int64, maxRequests int) (profile, []tweet, error) {
	var prof profile
	var tweets []tweet

	baseURL := baseFetchURL + user
	url := baseURL
	for nr := 0; nr < maxRequests; nr++ {
		b, err := ft.fetch(url, false /* useCache */)
		if err != nil {
			return prof, tweets, err
		}
		var newTweets []tweet
		if prof, newTweets, err = parse(bytes.NewReader(b), ft); err != nil {
			return prof, tweets, err
		} else if len(newTweets) == 0 { // Went past the beginning of the feed?
			return prof, tweets, nil
		}

		// Bail out early if there are no new tweets.
		if len(tweets) == 0 && newTweets[0].id == oldMaxID {
			return prof, nil, errUnchanged
		}

		tweets = append(tweets, newTweets...)
		minID := newTweets[len(newTweets)-1].id
		url = fmt.Sprintf("%s?max_id=%v", baseURL, minID-1)
	}
	debugf("Parsed %v tweet(s)", len(tweets))

	var err error
	if oldMaxID > 0 && tweets[len(tweets)-1].id > oldMaxID+1 {
		err = errPossibleGap
	}
	return prof, tweets, err
}

// writeFeed writes a feed in the supplied format containing tweets from a user's timeline.
// If replies is true, the user's replies will also be included.
func writeFeed(w io.Writer, format feedFormat, prof profile, tweets []tweet, user string, replies bool) error {
	author := prof.displayName()
	feedDesc := "Tweets"
	if replies {
		feedDesc += " and replies"
	}
	feedDesc += fmt.Sprintf(" from @%v's timeline", user)

	feed := &feeds.Feed{
		Title:       author,
		Link:        &feeds.Link{Href: userURL(user)},
		Description: feedDesc,
		Author:      &feeds.Author{Name: author},
		Updated:     time.Now(),
		Copyright:   fmt.Sprintf("© %v %v", time.Now().Year(), author),
	}
	if prof.image != "" {
		feed.Image = &feeds.Image{Url: prof.image}
	}

	for _, t := range tweets {
		if !replies && t.reply() {
			continue
		}

		item := &feeds.Item{
			Title:       t.text,
			Link:        &feeds.Link{Href: t.href}, // Atom's default rel is "alternate"
			Description: t.text,
			Author:      &feeds.Author{Name: t.displayName()},
			Id:          fmt.Sprintf("%v", t.id),
			Created:     t.time,
			Content:     t.content,
		}
		if len(item.Title) > titleLen {
			item.Title = item.Title[:titleLen-1] + "…"
		}
		feed.Add(item)
	}

	var maxID int64
	if len(tweets) > 0 {
		maxID = tweets[0].id
	}

	debugf("Writing feed with %v item(s) and max ID %v", len(feed.Items), maxID)

	switch format {
	case jsonFormat:
		// Embed the max ID in the feed's UserComment field.
		// The marshaling here matches feeds.Feed.WriteJSON().
		jf := (&feeds.JSON{Feed: feed}).JSONFeed()
		jf.UserComment = fmt.Sprintf("max id %v", maxID)
		jf.Favicon = prof.icon
		jf.Icon = prof.image
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(jf)
	case atomFormat, rssFormat:
		var err error
		if format == atomFormat {
			err = feed.WriteAtom(w)
		} else {
			err = feed.WriteRss(w)
		}
		if err != nil {
			return err
		}
		// Embed the max ID in a trailing comment.
		_, err = fmt.Fprintf(w, "\n<!-- max id %v -->\n", maxID)
		return err
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// These match the comments added by writeFeed.
var xmlMaxIDRegexp = regexp.MustCompile(`<!--\s+max\s+id\s+(\d+)\s+-->\s*$`)
var jsonMaxIDRegexp = regexp.MustCompile(`^max id (\d+)$`)

// getMaxID attempts to find a maximum tweet ID embedded in p, a feed written by writeFeed
// in the supplied format. If the file does not exist, 0 is returned with a nil error.
func getMaxID(p string, format feedFormat) (int64, error) {
	b, err := ioutil.ReadFile(p)
	if os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	var matches []string
	switch format {
	case atomFormat, rssFormat:
		matches = xmlMaxIDRegexp.FindStringSubmatch(string(b))
	case jsonFormat:
		var feed feeds.JSONFeed
		if err := json.Unmarshal(b, &feed); err != nil {
			return 0, errors.New("failed unmarshaling feed")
		}
		matches = jsonMaxIDRegexp.FindStringSubmatch(feed.UserComment)
	}
	if matches == nil {
		return 0, errors.New("couldn't find max ID in comment")
	}
	return strconv.ParseInt(matches[1], 10, 64)
}

// debugParse reads an HTML timeline from p and dumps its tweets to stdout.
func debugParse(ft *fetcher, p string, replies bool) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	prof, tweets, err := parse(f, ft)
	if err != nil {
		return err
	}

	fmt.Printf("%+v\n", prof)
	for _, t := range tweets {
		if replies || !t.reply() {
			fmt.Printf("%+v\n", t)
		}
	}
	return nil
}

// debug logs the supplied arguments using log.Print if verbose logging is enabled.
func debug(args ...interface{}) {
	if verbose {
		log.Print(args...)
	}
}

// debugf logs the supplied format string and args using log.Printf if verbose logging is enabled.
func debugf(format string, args ...interface{}) {
	if verbose {
		log.Printf(format, args...)
	}
}
