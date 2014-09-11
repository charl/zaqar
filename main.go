package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/mailgun/mailgun-go"
)

const (
	NAME    = "zaqar"
	VERSION = "0.0.1"
	CONFIG  = "/etc/zaqar/config.toml"
)

var (
	configFile string
	debug      bool
)

// A Mailgun config set.
type mailgunConfig struct {
	Domain       string `toml:"domain"`
	ApiKey       string `toml:"apikey"`
	PublicApiKey string `toml:"publicapikey"`
}

// A two-element string tuple.
type StringTuple [2]string

// A log config set.
type logConfig struct {
	Path     string        `toml:"path"`
	Matchers []StringTuple `toml:'matchers'`
}

// A configuration set.
type Config struct {
	Mailgun mailgunConfig `toml:"mailgun"`
	Logs    map[string]logConfig
}

// Create a new Config.
func NewConfig(file string) (*Config, error) {
	var c Config

	if _, err := os.Stat(file); err != nil {
		file = CONFIG
	}

	if _, err := toml.DecodeFile(file, &c); err != nil {
		return &c, err
	}

	return &c, nil
}

// An output collector.
type collector struct {
	domain       string
	apiKey       string
	publicApiKey string
	errors       map[string][]string
	mu           sync.RWMutex
}

// Create a new collector.
func NewCollector(domain, apiKey, publicApiKey string) *collector {
	return &collector{domain: domain, apiKey: apiKey, publicApiKey: publicApiKey, errors: make(map[string][]string)}
}

// Add an error for a specific log to the collector.
func (c *collector) Add(name, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.errors[name]; !ok {
		c.errors[name] = []string{}
	}

	c.errors[name] = append(c.errors[name], message)
}

// Check if a specific log has any errors listed.
func (c *collector) HasErrors(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.errors[name]

	return ok
}

// Return all errors for a specifig log.
func (c *collector) Errors(name string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.errors[name]
}

// Send an email report for errors in this log.
func (c *collector) Send(name string) {
	if c.HasErrors(name) {
		mg := mailgun.NewMailgun(c.domain, c.apiKey, c.publicApiKey)
		m := mg.NewMessage(
			"Melon Developers <devs@melon.com.au>", // From
			fmt.Sprintf("Log Errors: %s", name),    // Subject
			strings.Join(c.errors[name], "\n"),     // Plain-text body
			"Melon Developers <devs@melon.com.au>", // Recipients (vararg list)
		)

		if _, _, err := mg.Send(m); err != nil {
			log.Printf("Error: could not send log error report for %s: %s", name, err.Error())
		}
		log.Printf("Sending: %s", strings.Join(c.errors[name], "\n"))
	}
}

// A matcher interface.
type matcher interface {
	Match(string) bool
}

// A regexp matcher.
type regexpMatcher struct {
	criteria string
	re       *regexp.Regexp
}

// Create a new regexp matcher.
func NewRegexpMatcher(criteria string) *regexpMatcher {
	return &regexpMatcher{criteria: criteria, re: regexp.MustCompile(criteria)}
}

// Apply the matcher.
func (m *regexpMatcher) Match(text string) bool {
	match := m.re.FindStringIndex(text)

	if match == nil || len(match) < 1 {
		return false
	}
	return true
}

// A substring matcher.
type substringMatcher struct {
	criteria string
}

// Create a new substring matcher.
func NewSubstringMatcher(criteria string) *substringMatcher {
	return &substringMatcher{criteria: criteria}
}

// Apply the matcher.
func (m *substringMatcher) Match(text string) bool {
	return strings.Contains(m.criteria, text)
}

// Package-level init.
func init() {
	// Setup cli flags.
	flag.StringVar(&configFile, "c", "/etc/zaqar/config.toml", "path to the config file")
	flag.BoolVar(&debug, "debug", false, "turn on debugging")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s v%s\nUsage: %s [arguments] \n", NAME, VERSION, NAME)
		flag.PrintDefaults()
	}
}

// Main application logic.
func main() {
	// Clear all log flags.
	log.SetFlags(0)

	// Parse CLI flags.
	flag.Parse()

	// Read config.
	conf, err := NewConfig(configFile)
	if err != nil {
		log.Fatal(err)
	}

	// Create and output collector.
	collector := NewCollector(conf.Mailgun.Domain, conf.Mailgun.ApiKey, conf.Mailgun.PublicApiKey)

	// Start a processing pipeline for each log.
	done := make(chan struct{}, len(conf.Logs))
	for name, log := range conf.Logs {
		go startPipeline(name, log, collector, done)
	}

	// Wait for all pipelines to compelte.
	for i := 0; i < len(conf.Logs); i++ {
		<-done
	}
}

// Start a processing pipeline for a log.
func startPipeline(name string, logc logConfig, collector *collector, done chan struct{}) {
	// log.Printf("Starting Pipeline: name: %s  logc: %#v  collector: %#v ", name, logc, collector)

	// Start a worker for each of the configured matchers.
	wg := &sync.WaitGroup{}
	queues := make(map[int]chan string)
	for i, m := range logc.Matchers {
		queues[i] = make(chan string, 1)
		wg.Add(1)
		go startMatcher(name, m[0], m[1], collector, queues[i], wg)
	}

	// Read the log file line by line.
	file, err := os.Open(logc.Path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// log.Printf("Log Line: %s", scanner.Text())

		// Pass each line to each matcher.
		for _, q := range queues {
			q <- scanner.Text()
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	// Close all matcher input channels.
	for _, q := range queues {
		close(q)
	}

	// Wait for all matchers to finish.
	wg.Wait()

	// Send an email report for this log if there were errors.
	collector.Send(name)

	// Signal main that we're done.
	done <- struct{}{}
}

// Start a matcher.
func startMatcher(name, flavour, criteria string, collector *collector, queue chan string, wg *sync.WaitGroup) {
	defer wg.Done()

	var m matcher
	switch flavour {
	case "regexp":
		m = NewRegexpMatcher(criteria)
	case "substring":
		m = NewSubstringMatcher(criteria)
	default:
		log.Printf("Error: unknown matcher %s", flavour)
		return
	}

	for line := range queue {
		// Process this line.
		if m.Match(line) {
			collector.Add(name, line)
		}
	}
}
