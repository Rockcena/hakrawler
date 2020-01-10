package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hakluke/hakrawler/pkg/collector"
	"github.com/hakluke/hakrawler/pkg/config"
	"github.com/logrusorgru/aurora"
)

func banner(au aurora.Aurora) {
	fmt.Print(au.BrightRed(`
██╗  ██╗ █████╗ ██╗  ██╗██████╗  █████╗ ██╗    ██╗██╗     ███████╗██████╗
██║  ██║██╔══██╗██║ ██╔╝██╔══██╗██╔══██╗██║    ██║██║     ██╔════╝██╔══██╗
███████║███████║█████╔╝ ██████╔╝███████║██║ █╗ ██║██║     █████╗  ██████╔╝
██╔══██║██╔══██║██╔═██╗ ██╔══██╗██╔══██║██║███╗██║██║     ██╔══╝  ██╔══██╗
██║  ██║██║  ██║██║  ██╗██║  ██║██║  ██║╚███╔███╔╝███████╗███████╗██║  ██║
╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝ ╚══╝╚══╝ ╚══════╝╚══════╝╚═╝  ╚═╝
`))
	fmt.Println(aurora.BgBlue(au.BrightYellow("                        Crafted with <3 by hakluke                        ")))
}

func main() {
	conf := config.NewConfig()
	// define and parse command line flags
	flag.StringVar(&conf.Domain, "domain", "", "The domain that you wish to crawl (for example, google.com)")
	flag.IntVar(&conf.Depth, "depth", 1, "Maximum depth to crawl, the default is 1. Anything above 1 will include URLs from robots, sitemap, waybackurls and the initial crawler as a seed. Higher numbers take longer but yield more results.")
	flag.StringVar(&conf.Outdir, "outdir", "", "Directory to save discovered raw HTTP requests")
	flag.StringVar(&conf.Cookie, "cookie", "", "The value of this will be included as a Cookie header")
	flag.StringVar(&conf.AuthHeader, "auth", "", "The value of this will be included as a Authorization header")
	flag.StringVar(&conf.Scope, "scope", "subs", "Scope to include:\nstrict = specified domain only\nsubs = specified domain and subdomains\nfuzzy = anything containing the supplied domain\nyolo = everything")
	flag.StringVar(&conf.Schema, "schema", "http", "Schema, http or https")
	flag.BoolVar(&conf.Wayback, "usewayback", false, "Query wayback machine for URLs and add them as seeds for the crawler")
	flag.BoolVar(&conf.Plain, "plain", false, "Don't use colours or print the banners to allow for easier parsing")
	flag.BoolVar(&conf.Runlinkfinder, "linkfinder", false, "Run linkfinder on javascript files.")

	// which data to include in output?
	flag.BoolVar(&conf.IncludeJS, "js", false, "Include links to utilised JavaScript files")
	flag.BoolVar(&conf.IncludeSubs, "subs", false, "Include subdomains in output")
	flag.BoolVar(&conf.IncludeURLs, "urls", false, "Include URLs in output")
	flag.BoolVar(&conf.IncludeForms, "forms", false, "Include form actions in output")
	flag.BoolVar(&conf.IncludeRobots, "robots", false, "Include robots.txt entries in output")
	flag.BoolVar(&conf.IncludeSitemap, "sitemap", false, "Include sitemap.xml entries in output")
	flag.BoolVar(&conf.IncludeWayback, "wayback", false, "Include wayback machine entries in output")
	flag.BoolVar(&conf.IncludeAll, "all", true, "Include everything in output - this is the default, so this option is superfluous")
	flag.Parse()

	// set up the bools
	if conf.IncludeJS || conf.IncludeSubs || conf.IncludeURLs || conf.IncludeForms || conf.IncludeRobots || conf.IncludeSitemap {
		conf.IncludeAll = false
	}

	au := aurora.NewAurora(!conf.Plain)

	// print the banner
	if !conf.Plain {
		banner(au)
	}

	stdout := bufio.NewWriter(os.Stdout)

	c := collector.NewCollector(&conf, au, stdout)

	domains := make(chan string, 1)
	var reqsMade []*http.Request
	var crawlErr error
	var domainwg sync.WaitGroup

	// either use conf.Domain or pull domains from stdin
	if conf.Domain != "" {
		domains <- conf.Domain
		close(domains)
	} else {
		ch := readStdin()
		go func() {
			//translate stdin channel to domains channel
			for d := range ch {
				domains <- d
			}
		}()
	}

	// flush to stdout periodically
	t := time.NewTicker(time.Millisecond * 500)
	defer t.Stop()
	go func() {
		for {
			select {
			case <-t.C:
				stdout.Flush()
			}
		}
	}()

	for d := range domains {
		domainwg.Add(1)
		go func(domain string) {
			defer domainwg.Done()
			reqsMade, crawlErr = c.Crawl(domain)

			// Report errors and flush requests to files as we go
			if crawlErr != nil {
				writeErrAndFlush(stdout, crawlErr.Error(), au)
			}
			if conf.Outdir != "" {
				err := printRequestsToRandomFiles(reqsMade, conf.Outdir)
				if err != nil {
					writeErrAndFlush(stdout, err.Error(), au)
				}
			}

		}(d)
	}

	domainwg.Wait()

	// just in case anything is still in buffer
	stdout.Flush()
}

func readStdin() <-chan string {
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			domain := strings.ToLower(sc.Text())
			if domain != "" {
				lines <- domain
			}
		}
	}()
	return lines
}

func writeErrAndFlush(w *bufio.Writer, msg string, au aurora.Aurora) {
	s := fmt.Sprintln(au.BrightRed("[error]"), msg)
	w.Write([]byte(s))
	w.Flush()
}

func printRequestsToRandomFiles(rs []*http.Request, dir string) error {
	for _, r := range rs {
		if r == nil {
			// Skip requests that were malformed
			continue
		}
		raw, err := httputil.DumpRequest(r, true)
		if err != nil {
			return err
		}

		uuid, _ := uuid.NewRandom()
		if dir[len(dir)-1:] != "/" {
			dir = dir + "/"
		}

		err = ioutil.WriteFile(dir+"hakrawler_"+uuid.String()+".req", []byte(raw), 0644)
		if err != nil {
			return err
		}
	}
	return nil
}
