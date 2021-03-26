// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	titleLen                      = 80   // max length of title text in feed, in runes
	defaultMode       os.FileMode = 0644 // default mode for new feed files
	torControlTimeout             = 5 * time.Second
)

var verbose = false // enable verbose logging

func main() {
	var fetchOpts fetchOptions
	var parseOpts parseOptions

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <user> <file>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.")
		fmt.Fprintln(flag.CommandLine.Output(), "Pass '-' for <file> to write feed to stdout.")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	browserSize := flag.String("browser-size", "1024x8192", "Browser viewport size")
	flag.StringVar(&fetchOpts.cacheDir, "cache-dir", "", "Chrome cache directory")
	flag.BoolVar(&fetchOpts.logDebug, "debug-chrome", false, "Log noisy Chrome debug messages")
	debugFile := flag.String("debug-file", "", "HTML timeline file to parse for debugging")
	dumpDOM := flag.Bool("dump-dom", false, "Dump the timeline DOM to stdout for debugging")
	fetchRetries := flag.Int("fetch-retries", 0, "Number of times to retry fetching")
	fetchTimeoutSec := flag.Int("fetch-timeout", 0, "Fetch timeout in seconds")
	force := flag.Bool("force", false, "Write feed even if there are no new tweets")
	formatFlag := flag.String("format", "atom", `Feed format to write ("atom", "json", "rss")`)
	flag.StringVar(&fetchOpts.proxy, "proxy", "", `Optional proxy server (e.g. "socks5://localhost:9050")`)
	pageSettleDelay := flag.Int("page-settle-delay", 2, "Seconds to wait for page render")
	replies := flag.Bool("replies", false, "Include the user's replies")
	flag.BoolVar(&fetchOpts.showSensitive, "show-sensitive", true, "Show sensitive content in tweets")
	serveAddr := flag.String("serve", "", `Listen for requests over HTTP (e.g. "0.0.0.0:8080")`)
	showSensitiveDelay := flag.Int("show-sensitive-delay", 2, "Seconds to wait after showing sensitive content")
	skipUsersStr := flag.String("skip-users", "", "Comma-separated users whose tweets should be skipped")
	flag.BoolVar(&parseOpts.simplify, "simplify", true, "Simplify HTML in feed")
	torControlAddr := flag.String("tor-control", "", `Interface for resetting Tor circuits after fetch fails (e.g. "0.0.0.0:9051")`)
	tweetTimeout := flag.Int("tweet-timeout", 0, "Timeout for loading tweets in seconds")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")
	flag.Parse()

	if *debugFile != "" {
		if err := debugParse(*debugFile, parseOpts, *replies); err != nil {
			log.Fatal("Failed reading timeline: ", err)
		}
		os.Exit(0)
	}

	ps := strings.Split(*browserSize, "x")
	if len(ps) != 2 {
		log.Fatalf("Bad browser size %q", *browserSize)
	}
	var werr, herr error
	fetchOpts.width, werr = strconv.Atoi(ps[0])
	fetchOpts.height, herr = strconv.Atoi(ps[1])
	if werr != nil || herr != nil {
		log.Fatalf("Bad browser size %q", *browserSize)
	}

	fetchOpts.pageSettleDelay = time.Duration(*pageSettleDelay) * time.Second
	fetchOpts.showSensitiveDelay = time.Duration(*showSensitiveDelay) * time.Second
	fetchOpts.tweetTimeout = time.Duration(*tweetTimeout) * time.Second

	format := feedFormat(*formatFlag)
	fetchTimeout := time.Duration(*fetchTimeoutSec) * time.Second

	if *serveAddr != "" {
		// Handle HTTP requests.
		http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			user := bareUser(req.FormValue("user"))
			log.Printf("Got request from %v for %v", req.RemoteAddr, user)
			if user == "" {
				http.Error(w, "No user specified", http.StatusInternalServerError)
				return
			}

			prof, tweets, err := fetchUser(ctx, user, fetchOpts, parseOpts, fetchTimeout, *fetchRetries)
			if err != nil {
				msg := fmt.Sprintf("Failed getting %v: %v", user, err)
				log.Print(msg)
				if err == errTweetsProtected {
					http.Error(w, msg, http.StatusUnauthorized)
				} else {
					http.Error(w, msg, http.StatusInternalServerError)
					if *torControlAddr != "" {
						log.Printf("Sending NEWNYM command to %v to reset Tor circuits", *torControlAddr)
						if err := resetTorCircuits(*torControlAddr); err != nil {
							log.Print("Failed resetting Tor circuits: ", err)
						}
					}
				}
				return
			}

			format := format // shadow value from flag
			if f := req.FormValue("format"); f != "" {
				format = feedFormat(f)
			}
			var skipUsers []string
			if s := req.FormValue("skipUsers"); s != "" {
				skipUsers = strings.Split(s, ",")
			}
			if err := writeFeed(w, format, prof, tweets, *replies, skipUsers); err != nil {
				msg := fmt.Sprintf("Failed writing %v: %v", user, err)
				log.Print(msg)
				http.Error(w, msg, http.StatusInternalServerError)
				return
			}
		})
		log.Printf("Listening on %v", *serveAddr)
		log.Fatal(http.ListenAndServe(*serveAddr, nil))
	} else {
		// Process a single timeline.
		if len(flag.Args()) != 2 && !*dumpDOM {
			flag.Usage()
			os.Exit(2)
		}

		ctx := context.Background()
		user := bareUser(flag.Arg(0))
		feedPath := flag.Arg(1)
		useStdout := feedPath == "-"

		// If we're dumping the DOM, just try to fetch the timeline once.
		if *dumpDOM {
			dom, err := fetchTimeline(ctx, user, fetchOpts)
			if err != nil {
				log.Fatal("Failed fetching timeline: ", err)
			}
			os.Stdout.WriteString(dom)
			os.Exit(0)
		}

		// Get the latest ID from the old copy of the feed so we can check for new
		// tweets before rewriting it.
		var oldLatestID int64
		var err error
		if !*force && !useStdout {
			if oldLatestID, err = getFeedLatestID(feedPath, format); err != nil {
				log.Printf("Couldn't get old latest ID from %v: %v", feedPath, err)
			}
		}

		prof, tweets, err := fetchUser(ctx, user, fetchOpts, parseOpts, fetchTimeout, *fetchRetries)
		if err != nil {
			log.Fatalf("Failed getting %v: %v", user, err)
		}

		if getTweetsLatestID(tweets) == oldLatestID {
			debug("No new tweets; exiting without writing feed")
			os.Exit(0)
		}

		var f *os.File
		if useStdout {
			f = os.Stdout
		} else {
			// Write to a temp file and then replace the feed atomically to preserve the old version if
			// something goes wrong.
			if f, err = ioutil.TempFile(filepath.Dir(feedPath), "."+filepath.Base(feedPath)+"."); err != nil {
				log.Fatal("Failed creating feed file: ", err)
			}
			defer os.Remove(f.Name()) // silently fails if we successfully rename temp file
		}

		var skipUsers []string
		if *skipUsersStr != "" {
			skipUsers = strings.Split(*skipUsersStr, ",")
		}
		if err := writeFeed(f, format, prof, tweets, *replies, skipUsers); err != nil {
			f.Close()
			log.Fatal("Failed writing feed: ", err)
		}
		if err := f.Close(); err != nil {
			log.Fatal("Failed closing feed file: ", err)
		}

		if !useStdout {
			mode := defaultMode // ioutil.TempFile seems to use 0600 by default
			if fi, err := os.Stat(feedPath); err == nil {
				mode = fi.Mode()
			}
			if err := os.Chmod(f.Name(), mode); err != nil {
				log.Print("Failed setting mode: ", err)
			}
			if err := os.Rename(f.Name(), feedPath); err != nil {
				log.Fatal("Failed replacing feed file: ", err)
			}
		}
	}
}

// fetchUser fetches the profile and tweets from the supplied user's timeline.
func fetchUser(ctx context.Context, user string, fetchOpts fetchOptions, parseOpts parseOptions,
	fetchTimeout time.Duration, fetchRetries int) (prof profile, tweets []tweet, err error) {
	debugf("Getting timeline for %v", user)
	var dom string
	var attempts int
	for {
		if fetchTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, fetchTimeout)
			defer cancel()
		}
		attempts++
		if dom, err = fetchTimeline(ctx, user, fetchOpts); err == nil {
			break
		} else {
			if attempts > fetchRetries {
				return prof, nil, fmt.Errorf("failed fetching timeline: %v", err)
			} else {
				debugf("Fetching timeline failed; trying again: %v", err)
			}
		}
	}

	prof, tweets, err = parseTimeline(strings.NewReader(dom), parseOpts)
	if err != nil {
		return prof, nil, fmt.Errorf("failed parsing timeline: %v", err)
	} else if len(tweets) == 0 {
		return prof, nil, errors.New("no tweets found")
	}
	debugf("Parsed %v tweet(s)", len(tweets))
	return prof, tweets, nil
}

// writeFeed writes a feed in the supplied format containing tweets from a user's timeline.
// If replies is true, the user's replies will also be included.
func writeFeed(w io.Writer, format feedFormat, prof profile, tweets []tweet,
	replies bool, skipUsers []string) error {
	author := prof.displayName()
	feedDesc := "Tweets"
	if replies {
		feedDesc += " and replies"
	}
	feedDesc += fmt.Sprintf(" from @%v's timeline", prof.User)

	feed := &feeds.Feed{
		Title:       author,
		Link:        &feeds.Link{Href: userURL(prof.User)},
		Description: feedDesc,
		Author:      &feeds.Author{Name: author},
		Updated:     time.Now(),
		Copyright:   fmt.Sprintf("© %v %v", time.Now().Year(), author),
	}
	if prof.Image != "" {
		feed.Image = &feeds.Image{Url: prof.Image}
	}

	// User-supplied names may not have the canonical casing.
	skipUsersMap := make(map[string]struct{})
	for _, u := range skipUsers {
		skipUsersMap[strings.ToLower(bareUser(u))] = struct{}{}
	}

	for _, t := range tweets {
		if !replies && t.reply() {
			continue
		}
		if _, ok := skipUsersMap[strings.ToLower(t.User)]; ok && t.User != prof.User {
			continue
		}

		item := &feeds.Item{
			Title:       t.Text,
			Link:        &feeds.Link{Href: t.Href}, // Atom's default rel is "alternate"
			Description: t.Text,
			Author:      &feeds.Author{Name: t.displayName()},
			Id:          fmt.Sprintf("%v", t.ID),
			Created:     t.Time,
			Updated:     t.Time,
			Content:     t.Content,
		}
		if ut := []rune(item.Title); len(ut) > titleLen {
			item.Title = string(ut[:titleLen-1]) + "…"
		}
		feed.Add(item)
	}

	latestID := getTweetsLatestID(tweets)
	debugf("Writing feed with %v item(s) and latest ID %v", len(feed.Items), latestID)

	switch format {
	case jsonFormat:
		// Embed the latest ID in the feed's UserComment field.
		// The marshaling here matches feeds.Feed.WriteJSON().
		jf := (&feeds.JSON{Feed: feed}).JSONFeed()
		jf.UserComment = fmt.Sprintf("latest id %v", latestID)
		jf.Favicon = prof.Icon
		jf.Icon = prof.Image
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
		// Embed the latest ID in a trailing comment.
		_, err = fmt.Fprintf(w, "\n<!-- latest id %v -->\n", latestID)
		return err
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// These match the comments added by writeFeed.
var xmlLatestIDRegexp = regexp.MustCompile(`<!--\s+latest\s+id\s+(\d+)\s+-->\s*$`)
var jsonLatestIDRegexp = regexp.MustCompile(`^latest id (\d+)$`)

// getTweetsLatestID returns the greatest ID from the supplied tweets.
func getTweetsLatestID(tweets []tweet) int64 {
	var latest int64
	for _, tw := range tweets {
		if tw.ID > latest {
			latest = tw.ID
		}
	}
	return latest
}

// getFeedLatestID attempts to find the latest tweet ID embedded in p, a feed written by writeFeed
// in the supplied format. If the file does not exist, 0 is returned with a nil error.
func getFeedLatestID(p string, format feedFormat) (int64, error) {
	b, err := ioutil.ReadFile(p)
	if os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	var matches []string
	switch format {
	case atomFormat, rssFormat:
		matches = xmlLatestIDRegexp.FindStringSubmatch(string(b))
	case jsonFormat:
		var feed feeds.JSONFeed
		if err := json.Unmarshal(b, &feed); err != nil {
			return 0, errors.New("failed unmarshaling feed")
		}
		matches = jsonLatestIDRegexp.FindStringSubmatch(feed.UserComment)
	}
	if matches == nil {
		return 0, errors.New("couldn't find latest ID in comment")
	}
	return strconv.ParseInt(matches[1], 10, 64)
}

// resetTorCircuits connects to the supplied host:port (e.g. "localhost:9051")
// and instructs the Tor service there to reset its circuits to hopefully get
// a new exit IP. See https://gitweb.torproject.org/torspec.git/tree/control-spec.txt.
func resetTorCircuits(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, torControlTimeout)
	if err != nil {
		return err
	}

	dl := time.Now().Add(torControlTimeout)
	conn.SetReadDeadline(dl)
	conn.SetWriteDeadline(dl)

	var werr error
	write := func(s string) {
		if werr == nil {
			_, werr = io.WriteString(conn, s)
		}
	}
	// TODO: Add a flag to supply authentication, maybe.
	write("AUTHENTICATE \"\"\r\n")
	write("SIGNAL NEWNYM\r\n")
	write("QUIT\r\n")

	cerr := conn.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// debugParse reads an HTML timeline from p and dumps its tweets to stdout.
func debugParse(p string, opts parseOptions, replies bool) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	prof, tweets, err := parseTimeline(f, opts)
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
